package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/crash"
)

// TestCrashReportsReachTheIntake: a machine's crash report from its last
// run goes to the node on the next heartbeat, and from the node to the
// intake once, and again only when it keeps happening.
func TestCrashReportsReachTheIntake(t *testing.T) {
	var mu sync.Mutex
	var got []crash.Payload
	intake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p crash.Payload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.Write([]byte(`{"issue":"https://github.com/A-Hackers-Guide/sys1/issues/99"}`))
	}))
	defer intake.Close()
	t.Setenv("SYS1_CRASH_INTAKE", intake.URL)

	n, ctx := startNode(t)
	now := time.Now().UTC()
	rep := &crash.Report{App: "sys0", Version: "0.8.0", OS: "linux", Arch: "amd64", Signature: "0123456789ab",
		Message: "panic: dial 10.1.2.3:7443 failed", Count: 1, First: now, Last: now,
		Frames: []string{"github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:120)"}}
	if err := crash.Queue(filepath.Join(n.cfg.DataDir, "clients", "pc"), rep); err != nil {
		t.Fatal(err)
	}
	joinAgent(t, ctx, n, "pc")
	deadline := time.Now().Add(10 * time.Second)
	for len(n.crashes.List()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	list := n.crashes.List()
	if len(list) != 1 || list[0].Message != "panic: dial <ip> failed" {
		t.Fatalf("node has %+v", list)
	}

	s := &crash.Sender{Store: n.crashes, Intake: n.crashIntake(), Log: quiet}
	if s.SendDue(context.Background()) != 1 || s.SendDue(context.Background()) != 0 {
		t.Fatal("a crash wasn't sent exactly once")
	}
	if e := n.crashes.List()[0]; e.Issue != "https://github.com/A-Hackers-Guide/sys1/issues/99" {
		t.Fatalf("issue link not kept: %+v", e)
	}
	rep.Signature = list[0].Signature // the node's own, computed from the frames
	n.AddCrash(*rep)                  // it happens again, but within a day: not sent yet
	if s.SendDue(context.Background()) != 0 {
		t.Fatal("a repeat was sent within a day")
	}
	n.crashes.SetSending(false)
	n.crashes.Forget(rep.Signature)
	n.AddCrash(crash.Report{App: "sys1", Signature: "ba9876543210", Version: "0.8.0", Count: 1})
	if s.SendDue(context.Background()) != 0 {
		t.Fatal("sent with sending off")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Signature != rep.Signature {
		t.Fatalf("intake got %+v", got)
	}
}
