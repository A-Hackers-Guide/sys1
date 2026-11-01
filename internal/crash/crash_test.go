package crash

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain doubles as the crashing program: with SYS1_CRASH_DIR set, the
// test binary captures crash output there and panics in a goroutine, as a
// real bug would.
func TestMain(m *testing.M) {
	if d := os.Getenv("SYS1_CRASH_DIR"); d != "" {
		Capture(d, "sys0")
		done := make(chan bool)
		go func() {
			crashForTest(os.Getenv("SYS1_CRASH_MSG"))
			done <- true
		}()
		<-done
		return
	}
	os.Exit(m.Run())
}

func crashForTest(msg string) { panic(msg) }

func crashOnce(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SYS1_CRASH_DIR="+dir, "SYS1_CRASH_MSG="+msg)
	if err := cmd.Run(); err == nil {
		t.Fatal("the program didn't crash")
	}
}

func TestHarvestARealCrash(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	crashOnce(t, dir, "couldn't open "+dir+"/secret.txt on "+host+" from 192.168.1.20:7443 for me@example.com using sys1-join:eyJhIjoiMTAuMC4wLjE6NzQ0MyJ9")
	r := Harvest(dir, "sys0", "0.8.0")
	if r == nil {
		t.Fatal("no report")
	}
	b, _ := json.Marshal(r)
	for _, leak := range []string{dir, "secret.txt", "192.168.1.20", "me@example.com", "eyJhIjoi", "0x"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("report contains %q:\n%s", leak, r.JSON())
		}
	}
	if host != "" && len(host) >= 3 && strings.Contains(strings.ToLower(string(b)), strings.ToLower(host)) {
		t.Errorf("report contains the host name:\n%s", r.JSON())
	}
	if !strings.HasPrefix(r.Message, "panic: couldn't open") {
		t.Errorf("message: %q", r.Message)
	}
	found := false
	for _, f := range r.Frames {
		if strings.HasPrefix(f, "github.com/A-Hackers-Guide/sys1/internal/crash.crashForTest (") {
			found = true
		}
		if !frameShape.MatchString(f) {
			t.Errorf("frame %q has an unexpected shape", f)
		}
	}
	if !found {
		t.Errorf("crashing function missing from frames: %v", r.Frames)
	}
	if !r.Valid(NewScrubber(dir)) {
		t.Fatalf("own report is invalid:\n%s", r.JSON())
	}
	if Harvest(dir, "sys0", "0.8.0") != nil {
		t.Fatal("the same crash was reported twice")
	}

	// The same bug with a different message has the same signature.
	crashOnce(t, dir, "something else")
	if r2 := Harvest(dir, "sys0", "0.8.1"); r2 == nil || r2.Signature != r.Signature {
		t.Fatalf("signature changed for the same bug: %v", r2)
	}
}

func TestScrub(t *testing.T) {
	s := &Scrubber{literals: []string{"alices-macbook", "alice"}}
	for in, bad := range map[string]string{
		"dial tcp 10.0.0.5:7443: connection refused":          "10.0.0.5",
		"dial tcp [fe80::1%en0]:7443: no route":               "fe80",
		"open C:\\Users\\Bob\\AppData\\sys0\\x.json: denied":  "Bob",
		"lookup ALICES-MACBOOK.local: no such host":           "MACBOOK",
		"Get https://node.example.org/api?token=abc: timeout": "example",
		"bad key 3q2+7w0FyZ1T0aXdIdHlFeHBsYWluZWQ=":           "3q2+7w",
		"mac 00:1a:2b:3c:4d:5e down":                          "4d:5e",
		"stat /home/alice/.config/sys0: not found":            "alice",
	} {
		if out := s.Scrub(in); strings.Contains(out, bad) {
			t.Errorf("Scrub(%q) = %q still has %q", in, out, bad)
		}
	}
	if out := s.Scrub("runtime error: index out of range [3] with length 3"); out != "runtime error: index out of range [3] with length 3" {
		t.Errorf("harmless message changed: %q", out)
	}
}

func TestValidSetsTheSignature(t *testing.T) {
	r := Report{App: "sys0", Version: "0.8.0", OS: "linux", Arch: "amd64", Signature: "ffffffffffff", Message: "close ``` and [x](y)",
		Frames: []string{"github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:12)"}}
	if !r.Valid(&Scrubber{}) || r.Signature == "ffffffffffff" || len(r.Signature) != 12 {
		t.Fatalf("signature not recomputed: %q", r.Signature)
	}
	if strings.Contains(r.Message, "`") || strings.Contains(r.Message, "](") {
		t.Fatalf("message can still break out of its code block: %q", r.Message)
	}
}

func TestValidRejectsForgedReports(t *testing.T) {
	s := &Scrubber{}
	good := Report{App: "sys0", Version: "0.8.0", OS: "linux", Arch: "amd64", Signature: "0123456789ab",
		Frames: []string{"github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:12)"}}
	if r := good; !r.Valid(s) {
		t.Fatal("good report rejected")
	}
	for name, mutate := range map[string]func(*Report){
		"app":       func(r *Report) { r.App = "evil" },
		"link":      func(r *Report) { r.Frames = []string{"[Download_fix](//evil.example/x) (a.go:1)"} },
		"frame":     func(r *Report) { r.Frames = []string{"my password is hunter2"} },
		"version":   func(r *Report) { r.Version = "1.0 see http://x" },
	} {
		r := good
		r.Frames = append([]string(nil), good.Frames...)
		mutate(&r)
		if r.Valid(s) {
			t.Errorf("%s: forged report accepted", name)
		}
	}
}
