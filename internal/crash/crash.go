// Package crash turns crashes of sys0 and sys1 into reports the project
// can act on, with nothing personal in them.
//
// Go writes a crashing program's panic and stack traces to a file set with
// debug.SetCrashOutput. On the next start, Harvest reads that file and
// keeps only what helps fix the bug: the app, version, OS, CPU, the panic
// message with anything that could identify a person or machine removed,
// and the function names and source lines of the crashing goroutine. No
// arguments, memory addresses, other goroutines, logs, paths, names or
// addresses.
package crash

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Report is one crash, ready to share.
type Report struct {
	App       string    `json:"app"`
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Signature string    `json:"signature"` // same bug, same signature, across machines and versions
	Message   string    `json:"message"`
	Frames    []string  `json:"frames"`
	Count     int       `json:"count"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
}

const maxFrames = 32

func dir(dataDir string) string { return filepath.Join(dataDir, "crash") }

func outputPath(dataDir, app string) string { return filepath.Join(dir(dataDir), app+"-output.txt") }

// Capture makes the Go runtime write this process's crash output to a file
// in dataDir, for Harvest to read on the next start. Call it early, after
// Harvest.
func Capture(dataDir, app string) {
	if err := os.MkdirAll(dir(dataDir), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(outputPath(dataDir, app), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	debug.SetCrashOutput(f, debug.CrashOptions{})
	f.Close() // SetCrashOutput keeps its own copy of the file
}

// Harvest turns the crash output left by the previous run, if any, into a
// report and clears it.
func Harvest(dataDir, app, version string) *Report {
	p := outputPath(dataDir, app)
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil
	}
	os.Truncate(p, 0)
	fi, _ := os.Stat(p)
	when := time.Now()
	if fi != nil {
		when = fi.ModTime()
	}
	r := Parse(string(b), NewScrubber(dataDir))
	if r == nil {
		return nil
	}
	r.App, r.Version, r.OS, r.Arch = app, version, runtime.GOOS, runtime.GOARCH
	r.Signature = signature(app, r)
	r.Count, r.First, r.Last = 1, when.UTC(), when.UTC()
	return r
}

var (
	goroutineLine = regexp.MustCompile(`^goroutine \d+ \[`)
	frameArgs     = regexp.MustCompile(`\((0x[0-9a-f]+|\.\.\.|\{.*?\}|, )*\)$`)
	fileLine      = regexp.MustCompile(`^\s+(.+\.(go|s)):(\d+)`)
)

// Parse reads Go crash output: the panic or fatal error message, then the
// first goroutine's stack (the one that crashed).
func Parse(out string, s *Scrubber) *Report {
	r := &Report{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var msg []string
	state := 0 // 0: message, 1: in the crashing goroutine, 2: done
	var fn string
	for sc.Scan() && state < 2 {
		line := sc.Text()
		switch state {
		case 0:
			if goroutineLine.MatchString(line) {
				state = 1
				continue
			}
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "[signal") {
				msg = append(msg, t)
			}
		case 1:
			if strings.TrimSpace(line) == "" {
				state = 2
				continue
			}
			if m := fileLine.FindStringSubmatch(line); m != nil {
				if fn != "" && len(r.Frames) < maxFrames {
					r.Frames = append(r.Frames, fn+" ("+shortFile(m[1])+":"+m[3]+")")
				}
				fn = ""
				continue
			}
			fn = frameArgs.ReplaceAllString(strings.TrimSpace(line), "")
			fn = strings.TrimPrefix(fn, "created by ")
			if i := strings.Index(fn, " in goroutine "); i > 0 {
				fn = fn[:i]
			}
		}
	}
	if len(msg) == 0 && len(r.Frames) == 0 {
		return nil
	}
	r.Message = s.Scrub(strings.Join(msg, " "))
	return r
}

// shortFile keeps source paths inside Go modules and the standard library
// and cuts any local directory (builds without -trimpath).
func shortFile(f string) string {
	f = filepath.ToSlash(f)
	for _, marker := range []string{"/pkg/mod/", "/src/"} {
		if i := strings.LastIndex(f, marker); i >= 0 {
			return f[i+len(marker):]
		}
	}
	if strings.HasPrefix(f, "/") || (len(f) > 2 && f[1] == ':') {
		parts := strings.Split(f, "/")
		if len(parts) > 2 {
			return strings.Join(parts[len(parts)-2:], "/")
		}
	}
	return f
}

// signature identifies a bug by the functions it crashed in, ignoring line
// numbers so the same bug matches across versions.
func signature(app string, r *Report) string {
	h := sha256.New()
	h.Write([]byte(app))
	n := 0
	for _, f := range r.Frames {
		name, _, _ := strings.Cut(f, " (")
		if strings.HasPrefix(name, "runtime.") || strings.HasPrefix(name, "panic(") {
			continue // where the runtime raised it, not where the bug is
		}
		h.Write([]byte("\n" + name))
		if n++; n == 6 {
			break
		}
	}
	if n == 0 {
		h.Write([]byte("\n" + messageShape(r.Message)))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

var digits = regexp.MustCompile(`\d+`)

// frameShape is all a frame may look like: a Go function and a source line.
var frameShape = regexp.MustCompile(`^[A-Za-z0-9_.*()\[\]{},/-]+ \([A-Za-z0-9_./-]+\.(go|s):\d+\)$`)

func messageShape(m string) string { return digits.ReplaceAllString(m, "N") }

// A Scrubber removes anything from text that could identify a person or
// machine.
type Scrubber struct{ literals []string }

// NewScrubber knows this machine's own names, so it can remove them even
// where no pattern would catch them.
func NewScrubber(dataDir string) *Scrubber {
	var lit []string
	add := func(v string) {
		if len(v) >= 3 {
			lit = append(lit, v)
		}
	}
	if h, err := os.Hostname(); err == nil {
		add(h)
		add(strings.Split(h, ".")[0])
	}
	if u, err := user.Current(); err == nil {
		add(u.HomeDir)
		add(u.Username)
		add(u.Name)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	add(dataDir)
	// Longest first, so a home folder goes before the user name inside it.
	for i := range lit {
		for j := i + 1; j < len(lit); j++ {
			if len(lit[j]) > len(lit[i]) {
				lit[i], lit[j] = lit[j], lit[i]
			}
		}
	}
	return &Scrubber{literals: lit}
}

var patterns = []struct {
	re  *regexp.Regexp
	put string
}{
	{regexp.MustCompile(`sys1-(join|cluster):[A-Za-z0-9_=-]+`), "<code>"},
	{regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`), "<url>"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "<email>"},
	{regexp.MustCompile(`\[?(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}(?:%\w+)?\]?(?::\d+)?`), "<ip>"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`), "<ip>"},
	{regexp.MustCompile(`\b(?:[0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}\b`), "<mac>"},
	{regexp.MustCompile(`(?:[A-Za-z]:\\|\\\\)[^\s"':]+`), "<path>"},
	{regexp.MustCompile(`(?:^|[\s"'=(])(/[^\s"':,)]+)`), " <path>"},
	{regexp.MustCompile(`\b[A-Za-z0-9+/_=-]{24,}\b`), "<redacted>"},
	{regexp.MustCompile(`\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:local|lan|home|internal|localdomain|com|net|org|io|dev|app|me|de|uk|fr|nl|jp|cn|ru|br|in|au|ca|us|eu|tv|cloud|xyz)\b`), "<host>"},
	{regexp.MustCompile(`@`), "(at)"}, // no @mentions in GitHub issues
}

const maxMessage = 400

// Scrub returns s without names, addresses, paths, codes or secrets.
func (sc *Scrubber) Scrub(s string) string {
	// Patterns first, so a path or address goes whole; then this machine's
	// own names wherever they're left.
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.put)
	}
	for _, l := range sc.literals {
		s = replaceFold(s, l, "<redacted>")
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxMessage {
		s = s[:maxMessage] + "…"
	}
	return s
}

func replaceFold(s, old, repl string) string {
	lower, lowOld := strings.ToLower(s), strings.ToLower(old)
	var b strings.Builder
	for {
		i := strings.Index(lower, lowOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

// Valid checks a report that arrived from elsewhere and scrubs it again,
// so nothing personal passes even from a modified client.
func (r *Report) Valid(s *Scrubber) bool {
	if r.App != "sys0" && r.App != "sys1" || len(r.Frames) > maxFrames || len(r.Signature) != 12 {
		return false
	}
	if _, err := hex.DecodeString(r.Signature); err != nil {
		return false
	}
	for _, f := range []*string{&r.Version, &r.OS, &r.Arch} {
		if len(*f) > 32 || strings.ContainsAny(*f, " \n/\\") {
			return false
		}
	}
	r.Message = s.Scrub(r.Message)
	for _, f := range r.Frames {
		if len(f) > 300 || !frameShape.MatchString(f) {
			return false
		}
	}
	if r.Count < 1 {
		r.Count = 1
	}
	return true
}

// JSON is the report as it is sent.
func (r *Report) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}

func queueDir(dataDir string) string { return filepath.Join(dir(dataDir), "queue") }

// Queue keeps a report until it reaches the node.
func Queue(dataDir string, r *Report) error {
	d := queueDir(dataDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if n, _ := os.ReadDir(d); len(n) >= 50 {
		return nil // a machine stuck crashing doesn't need to keep every one
	}
	name := filepath.Join(d, r.Signature+"-"+r.Last.Format("20060102T150405.000000000")+".json")
	return os.WriteFile(name, r.JSON(), 0o600)
}

// Queued returns the reports waiting to be sent, each with a function
// that removes it once sent.
func Queued(dataDir string) (reports []Report, done []func()) {
	entries, _ := os.ReadDir(queueDir(dataDir))
	for _, e := range entries {
		p := filepath.Join(queueDir(dataDir), e.Name())
		var r Report
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &r) != nil {
			os.Remove(p)
			continue
		}
		reports = append(reports, r)
		done = append(done, func() { os.Remove(p) })
	}
	return reports, done
}
