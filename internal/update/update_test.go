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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func archive(t *testing.T, bin string, content []byte) []byte {
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("sys1/" + bin)
		w.Write(content)
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: bin, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeGitHub serves a release of version v whose archive holds newBin.
func fakeGitHub(t *testing.T, v string, newBin []byte, tamper bool) *httptest.Server {
	bin := "sys0"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	name := ArchiveName(v, runtime.GOOS, runtime.GOARCH)
	arc := archive(t, bin, newBin)
	sum := sha256.Sum256(arc)
	sums := fmt.Sprintf("%s  %s\n%s  other.tar.gz\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64))
	if tamper {
		arc = append(arc, 0)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			json.NewEncoder(w).Encode(Release{Tag: "v" + v, Assets: []Asset{
				{Name: checksumsName, URL: srv.URL + "/sums"},
				{Name: bundleName, URL: srv.URL + "/bundle"},
				{Name: name, URL: srv.URL + "/arc"},
			}})
		case "/sums":
			w.Write([]byte(sums))
		case "/bundle":
			w.Write([]byte("signed"))
		case "/arc":
			w.Write(arc)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newUpdater(t *testing.T, srv *httptest.Server, verifyErr error) *Updater {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "sys0")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old binary"), 0o755)
	return &Updater{
		App: "sys0", Current: "0.7.0", DataDir: dir, API: srv.URL, Exe: exe,
		Verify: func(ctx context.Context, sums, bundle []byte) error {
			if string(bundle) != "signed" {
				return errors.New("no bundle")
			}
			return verifyErr
		},
	}
}

func TestUpdateInstallsAVerifiedRelease(t *testing.T) {
	u := newUpdater(t, fakeGitHub(t, "0.8.0", []byte("new binary"), false), nil)
	v, err := u.Once(context.Background())
	if err != nil || v != "0.8.0" {
		t.Fatalf("Once = %q, %v", v, err)
	}
	if b, _ := os.ReadFile(u.Exe); string(b) != "new binary" {
		t.Fatalf("binary is %q", b)
	}
	if b, _ := os.ReadFile(u.Exe + ".old"); string(b) != "old binary" {
		t.Fatalf("old binary not kept: %q", b)
	}
}

func TestUpdateRefusesBadSignatureOrChecksum(t *testing.T) {
	u := newUpdater(t, fakeGitHub(t, "0.8.0", []byte("evil"), false), errors.New("wrong signer"))
	if _, err := u.Once(context.Background()); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("bad signature accepted: %v", err)
	}
	u = newUpdater(t, fakeGitHub(t, "0.8.0", []byte("evil"), true), nil)
	if _, err := u.Once(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	if b, _ := os.ReadFile(u.Exe); string(b) != "old binary" {
		t.Fatalf("binary changed: %q", b)
	}
}

func TestUpdateIgnoresOlderAndSameVersions(t *testing.T) {
	for _, v := range []string{"0.7.0", "0.6.9"} {
		u := newUpdater(t, fakeGitHub(t, v, []byte("x"), false), nil)
		if got, err := u.Once(context.Background()); got != "" || err != nil {
			t.Fatalf("%s: Once = %q, %v", v, got, err)
		}
	}
}

func TestRollbackAfterRepeatedFailedStarts(t *testing.T) {
	u := newUpdater(t, fakeGitHub(t, "0.8.0", []byte("new binary"), false), nil)
	if _, err := u.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The new version starts and dies before it's healthy, again and again.
	next := *u
	next.Current = "0.8.0"
	for i := 0; i < maxStarts; i++ {
		if next.Startup() {
			t.Fatalf("rolled back after %d starts", i+1)
		}
	}
	if !next.Startup() {
		t.Fatal("no rollback")
	}
	if b, _ := os.ReadFile(u.Exe); string(b) != "old binary" {
		t.Fatalf("old binary not restored: %q", b)
	}
	// 0.8.0 is skipped from now on.
	if v, err := u.Once(context.Background()); v != "" || err != nil {
		t.Fatalf("bad version installed again: %q, %v", v, err)
	}
}

func TestHealthyKeepsTheNewVersion(t *testing.T) {
	u := newUpdater(t, fakeGitHub(t, "0.8.0", []byte("new binary"), false), nil)
	u.Once(context.Background())
	next := *u
	next.Current = "0.8.0"
	next.Startup()
	next.Healthy()
	for i := 0; i < maxStarts+2; i++ {
		if next.Startup() {
			t.Fatal("rolled back a healthy version")
		}
	}
	if _, err := os.Stat(u.Exe + ".old"); err == nil {
		t.Fatal("old binary kept after the new one proved healthy")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.8.0", "0.7.0", true}, {"1.0.0", "0.99.99", true}, {"0.7.0", "0.7.0", false}, {"0.7.0", "0.8.0", false},
		{"0.10.0", "0.9.0", true}, {"dev", "0.7.0", false}, {"0.8.0-rc1", "0.7.0", false}} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestManagedInstallsAreLeftAlone(t *testing.T) {
	for _, p := range []string{"/opt/homebrew/Cellar/sys1/0.7.0/bin/sys0", `C:\Users\x\scoop\apps\sys1\current\sys0.exe`, "/usr/bin/sys0", "/snap/sys1/12/bin/sys0"} {
		if managedBy(p) == "" {
			t.Errorf("%s not seen as managed", p)
		}
	}
	for _, p := range []string{"/usr/local/bin/sys0", `C:\Program Files\sys1\sys0.exe`, "/home/me/.local/bin/sys0"} {
		if m := managedBy(p); m != "" {
			t.Errorf("%s seen as managed by %s", p, m)
		}
	}
}
