// Package update keeps sys0 and sys1 up to date by themselves.
//
// About once a day it asks GitHub for the latest release. A new version is
// installed only when both checks pass:
//
//  1. checksums.txt carries a Sigstore signature made by this repository's
//     release workflow on a version tag (keyless: there is no signing key
//     anyone could steal), and
//  2. the archive for this OS and CPU matches its SHA-256 in checksums.txt.
//
// The new binary replaces the old one with a rename, the old one is kept,
// and the service manager restarts the app. If the new version keeps
// crashing on start, Startup puts the old one back and that version is
// skipped. Installs owned by a package manager are left to it.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases come from.
const Repo = "A-Hackers-Guide/sys1"

const (
	checksumsName = "checksums.txt"
	bundleName    = "checksums.txt.sigstore.json"
	maxArchive    = 200 << 20
	// A new version that restarts this many times in quick succession
	// without running healthy for HealthyAfter is rolled back.
	maxStarts    = 3
	HealthyAfter = 10 * time.Minute
)

// ErrRestart is returned by Loop after an update is installed: the caller
// exits so the service manager starts the new binary.
var ErrRestart = errors.New("updated; restarting into the new version")

// Updater checks for, verifies and installs new versions of one app.
type Updater struct {
	App     string // "sys0" or "sys1"
	Current string // the running version, e.g. "0.7.0"
	DataDir string
	Log     *slog.Logger
	HTTP    *http.Client
	// API is the GitHub API base URL (tests point it elsewhere).
	API string
	// Verify checks the Sigstore bundle for checksums.txt. Tests replace it.
	Verify func(ctx context.Context, checksums, bundle []byte) error
	// Exe is the binary to replace (default: the running one).
	Exe string
}

func (u *Updater) defaults() {
	if u.HTTP == nil {
		u.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if u.API == "" {
		u.API = "https://api.github.com"
	}
	if u.Verify == nil {
		u.Verify = func(ctx context.Context, c, b []byte) error {
			return VerifySigstore(ctx, filepath.Join(u.DataDir, "sigstore"), c, b)
		}
	}
	if u.Log == nil {
		u.Log = slog.New(slog.DiscardHandler)
	}
}

// Release is the part of GitHub's release JSON the updater uses.
type Release struct {
	Tag    string  `json:"tag_name"`
	Draft  bool    `json:"draft"`
	Pre    bool    `json:"prerelease"`
	Assets []Asset `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (r *Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// ArchiveName is the release archive holding the binaries for an OS/CPU,
// as GoReleaser names it.
func ArchiveName(version, goos, goarch string) string {
	arch := goarch
	if goarch == "arm" {
		arch = "armv7"
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("sys1_%s_%s_%s%s", version, goos, arch, ext)
}

// Newer reports whether version a is newer than b (both "x.y.z").
// Anything that isn't a plain release version is never newer.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA {
		return false
	}
	if !okB {
		return true
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Allowed reports why this install can't update itself, or "" if it can.
func (u *Updater) Allowed() string {
	if _, ok := parse(u.Current); !ok {
		return "development build"
	}
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return "updated by the app or package manager"
	}
	exe, err := u.exe()
	if err != nil {
		return "can't find its own binary"
	}
	if m := managedBy(exe); m != "" {
		return "installed by " + m + ", which updates it"
	}
	if inContainer() {
		return "running in a container; pull the new image instead"
	}
	// The binary's folder must be writable to swap it.
	f, err := os.CreateTemp(filepath.Dir(exe), ".sys1-update-check-*")
	if err != nil {
		return "can't write to " + filepath.Dir(exe)
	}
	f.Close()
	os.Remove(f.Name())
	return ""
}

func (u *Updater) exe() (string, error) {
	if u.Exe != "" {
		return u.Exe, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// managedBy names the package manager that owns exe, if any.
func managedBy(exe string) string {
	p := strings.ReplaceAll(strings.ToLower(exe), `\`, "/")
	for _, m := range []struct{ part, name string }{
		{"/cellar/", "Homebrew"}, {"/homebrew/", "Homebrew"}, {"/linuxbrew/", "Homebrew"},
		{"/scoop/", "Scoop"}, {"/winget/", "winget"}, {"/chocolatey/", "Chocolatey"},
		{"/snap/", "snap"}, {"/nix/store/", "Nix"}, {"/var/jb/", "the jailbreak package manager"},
		{"/data/adb/", "Magisk"}, {"/.mount_", "AppImage"}, {"/usr/bin/", "your system's package manager"},
		{"/usr/sbin/", "your system's package manager"}, {"/opt/local/", "MacPorts"},
	} {
		if strings.Contains(p, m.part) {
			return m.name
		}
	}
	return ""
}

func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	return os.Getenv("container") != ""
}

// Latest fetches the latest release.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	u.defaults()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.API+"/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", u.App+"/"+u.Current)
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (u *Updater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", u.App+"/"+u.Current)
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: too large", url)
	}
	return b, nil
}

// Fetch downloads and verifies the new binary from r without installing it.
func (u *Updater) Fetch(ctx context.Context, r *Release) ([]byte, error) {
	u.defaults()
	v := r.Version()
	name := ArchiveName(v, runtime.GOOS, runtime.GOARCH)
	sumAsset, bundleAsset, archive := r.asset(checksumsName), r.asset(bundleName), r.asset(name)
	if sumAsset == nil || bundleAsset == nil || archive == nil {
		return nil, fmt.Errorf("release %s isn't complete or signed yet", r.Tag)
	}
	sums, err := u.download(ctx, sumAsset.URL, 1<<20)
	if err != nil {
		return nil, err
	}
	bundle, err := u.download(ctx, bundleAsset.URL, 1<<20)
	if err != nil {
		return nil, err
	}
	if err := u.Verify(ctx, sums, bundle); err != nil {
		return nil, fmt.Errorf("signature check failed, not updating: %w", err)
	}
	want, ok := checksumFor(sums, name)
	if !ok {
		return nil, fmt.Errorf("%s isn't in %s", name, checksumsName)
	}
	data, err := u.download(ctx, archive.URL, maxArchive)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s doesn't match its checksum, not updating", name)
	}
	bin := u.App
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return extract(data, strings.HasSuffix(name, ".zip"), bin)
}

func checksumFor(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

func extract(archive []byte, isZip bool, bin string) ([]byte, error) {
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxArchive))
			}
		}
		return nil, fmt.Errorf("%s isn't in the archive", bin)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s isn't in the archive", bin)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == bin {
			return io.ReadAll(io.LimitReader(tr, maxArchive))
		}
	}
}

// state is kept in <data>/update.json across the restart.
type state struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Starts    int       `json:"starts"`
	LastStart time.Time `json:"lastStart,omitzero"`
	// SkipUntil holds versions that were rolled back; they're tried again
	// after a while, so a rollback can't keep a fix away for good.
	SkipUntil map[string]time.Time `json:"skipUntil,omitempty"`
	Checked   time.Time            `json:"checked"`
}

// startWindow: starts further apart than this aren't a crash loop (a
// reboot, a restart by hand), so they don't count toward a rollback.
const startWindow = 5 * time.Minute

// skipFor is how long a rolled-back version is left alone.
const skipFor = 72 * time.Hour

func (u *Updater) statePath() string { return filepath.Join(u.DataDir, "update.json") }

func (u *Updater) load() state {
	var s state
	if b, err := os.ReadFile(u.statePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (u *Updater) save(s state) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := u.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.statePath())
}

// Install swaps in the new binary, keeping the old one next to it.
func (u *Updater) Install(newBin []byte, to string) error {
	exe, err := u.exe()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".sys1-update-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(newBin)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	// A running binary can be renamed on every OS, Windows included.
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	s := u.load()
	s.From, s.To, s.Starts = u.Current, to, 0
	return u.save(s)
}

// Startup runs first thing in every start. It counts quick restarts of a
// freshly installed version and rolls back to the previous binary when
// that version keeps failing; the caller then exits so the old one starts.
// It reports whether it rolled back.
func (u *Updater) Startup() (rolledBack bool) {
	u.defaults()
	s := u.load()
	if s.To == "" || s.To != u.Current {
		return false
	}
	now := time.Now()
	if now.Sub(s.LastStart) > startWindow {
		s.Starts = 0
	}
	s.Starts++
	s.LastStart = now
	if s.Starts <= maxStarts {
		u.save(s)
		return false
	}
	exe, err := u.exe()
	if err != nil {
		return false
	}
	if _, err := os.Stat(exe + ".old"); err != nil {
		return false
	}
	bad := exe + ".bad"
	os.Remove(bad)
	if os.Rename(exe, bad) != nil || os.Rename(exe+".old", exe) != nil {
		os.Rename(bad, exe)
		return false
	}
	u.Log.Error("the new version keeps failing; went back to the previous one", "bad", s.To, "back to", s.From)
	if s.SkipUntil == nil {
		s.SkipUntil = map[string]time.Time{}
	}
	s.SkipUntil[s.To] = now.Add(skipFor)
	s.From, s.To, s.Starts = "", "", 0
	u.save(s)
	return true
}

// Healthy marks the running version as good: it ran long enough. The
// previous binary is no longer needed.
func (u *Updater) Healthy() {
	s := u.load()
	if s.To == "" || s.To != u.Current {
		return
	}
	s.From, s.To, s.Starts = "", "", 0
	u.save(s)
	if exe, err := u.exe(); err == nil {
		os.Remove(exe + ".old")
		os.Remove(exe + ".bad")
	}
}

// Once checks for a new version and installs it. It reports whether a new
// binary was installed.
func (u *Updater) Once(ctx context.Context) (string, error) {
	u.defaults()
	r, err := u.Latest(ctx)
	if err != nil {
		return "", err
	}
	v := r.Version()
	if r.Draft || r.Pre || !Newer(v, u.Current) {
		return "", nil
	}
	if until, ok := u.load().SkipUntil[v]; ok && time.Now().Before(until) {
		return "", nil
	}
	bin, err := u.Fetch(ctx, r)
	if err != nil {
		return "", err
	}
	if err := u.Install(bin, v); err != nil {
		return "", err
	}
	u.Log.Info("installed a new version", "from", u.Current, "to", v)
	return v, nil
}

// Loop checks about once a day, starting a random while after launch so
// a whole pool doesn't hit GitHub at once. It returns ErrRestart once a new
// version is installed, or nil when ctx ends.
func (u *Updater) Loop(ctx context.Context) error {
	u.defaults()
	if why := u.Allowed(); why != "" {
		u.Log.Info("automatic updates are off for this install", "why", why)
		return nil
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(HealthyAfter):
			u.Healthy()
		}
	}()
	wait := 10*time.Minute + rand.N(50*time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		v, err := u.Once(ctx)
		switch {
		case err != nil:
			u.Log.Warn("update check failed", "err", err)
		case v != "":
			return ErrRestart
		}
		wait = 20*time.Hour + rand.N(8*time.Hour)
	}
}

// Run runs work alongside Loop. When an update is installed and restart is
// true, work is stopped and Run returns ErrRestart so the service manager
// starts the new binary; with restart false (someone runs the app in a
// terminal) the new version is used from the next start.
func (u *Updater) Run(ctx context.Context, restart bool, work func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updated := make(chan struct{})
	go func() {
		if u.Loop(ctx) == ErrRestart {
			if !restart {
				u.Log.Info("a new version is installed; it takes over the next time this starts")
				return
			}
			close(updated)
			cancel()
		}
	}()
	err := work(ctx)
	select {
	case <-updated:
		return ErrRestart
	default:
		return err
	}
}
