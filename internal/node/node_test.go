package node

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// The test binary doubles as the workload: with SYS1_TEST_HELPER set it
// saves its machine's directory as a checkpoint and crashes, until it
// starts on a different machine with that checkpoint, where it finishes.
func TestMain(m *testing.M) {
	if os.Getenv("SYS1_TEST_HELPER") == "1" {
		cp := os.Getenv("SYS1_CHECKPOINT")
		here, _ := os.Getwd()
		if b, err := os.ReadFile(cp); err == nil && string(b) != here {
			fmt.Println("resumed from checkpoint on another machine")
			os.Exit(0)
		}
		os.WriteFile(cp, []byte(here), 0o600)
		fmt.Println("crashing on purpose")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func startNode(t *testing.T) (*Node, context.Context) {
	t.Helper()
	n, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", GUIAddr: "127.0.0.1:0", PublicAddr: ""}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		n.Serve(ctx, func(string, string) { close(ready) })
	}()
	<-ready
	t.Cleanup(func() {
		cancel()
		<-done
		time.Sleep(100 * time.Millisecond) // let clients finish closing files
	})
	return n, ctx
}

func joinAgent(t *testing.T, ctx context.Context, n *Node, name string) {
	t.Helper()
	code := n.JoinCode()
	code.Addr = n.LocalJoinCode().Addr
	res := protocol.Resources{OS: "test", CPUCores: 4, CPUIdlePercent: 90, MemFree: 8 << 30, MemTotal: 16 << 30, Runtimes: []string{"exec"}}
	cfg, err := agent.Register(ctx, code, name, protocol.TrustOwner, res, "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(n.cfg.DataDir, "clients", name)
	a, err := agent.New(dir, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	a.Sample = func(bool) protocol.Resources { return res }
	go a.Run(ctx)
}

func TestWrongJoinTokenRejected(t *testing.T) {
	n, ctx := startNode(t)
	code := n.LocalJoinCode()
	code.Token = "nope"
	if _, err := agent.Register(ctx, code, "x", protocol.TrustOwner, protocol.Resources{}, "test"); err == nil {
		t.Fatal("bad token accepted")
	}
	code = n.LocalJoinCode()
	code.Fingerprint = fmt.Sprintf("%064d", 0)
	if _, err := agent.Register(ctx, code, "x", protocol.TrustOwner, protocol.Resources{}, "test"); err == nil {
		t.Fatal("wrong fingerprint accepted (impersonation possible)")
	}
}

// TestCrashLoopMovesTaskWithCheckpoint is the whole reliability story end
// to end: a task crashes 5 times on one machine, the watchdog gives up and
// uploads its fast-saved checkpoint, the node moves it to the other
// machine, and it resumes there from the checkpoint.
func TestCrashLoopMovesTaskWithCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	joinAgent(t, ctx, n, "alpha")
	joinAgent(t, ctx, n, "beta")

	exe, _ := os.Executable()
	n.mu.Lock()
	task, err := n.queueLocked("test", "crashy", protocol.TaskSpec{
		Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"},
		Env: map[string]string{"SYS1_TEST_HELPER": "1"},
	})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		state, out, excluded, msg := task.State, task.Output, len(task.Excluded), task.Message
		n.mu.Unlock()
		if state == StateDone {
			if excluded != 1 {
				t.Fatalf("expected the first machine to be excluded, got %d", excluded)
			}
			if want := "resumed from checkpoint on another machine"; !strings.Contains(out, want) {
				t.Fatalf("output %q does not contain %q", out, want)
			}
			return
		}
		if state == StateFailed {
			t.Fatalf("task failed: %s", msg)
		}
		time.Sleep(200 * time.Millisecond)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		t.Logf("machine %s: online=%v idle=%.0f%% memFree=%dMB battery=%v runtimes=%v",
			a.Name, n.online(a, time.Now()), a.Resources.CPUIdlePercent, a.Resources.MemFree>>20, a.Resources.OnBattery, a.Resources.Runtimes)
	}
	t.Fatalf("task never finished: state=%s attempt=%d excluded=%v message=%q", task.State, task.Attempt, task.Excluded, task.Message)
}

func TestOfflineMachineWorkMoves(t *testing.T) {
	n, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.j.Close() }) // Windows cannot delete open files
	now := time.Now()
	res := protocol.Resources{CPUCores: 4, CPUIdlePercent: 80, MemFree: 4 << 30, Runtimes: []string{"docker"}}
	n.agents["a"] = &Agent{ID: "a", Name: "a", Resources: res, LastSeen: now.Add(-time.Minute)}
	n.agents["b"] = &Agent{ID: "b", Name: "b", Resources: res, LastSeen: now}
	n.tasks["t"] = &Task{ID: "t", State: StateRunning, AgentID: "a", Attempt: 1, Spec: protocol.TaskSpec{Kind: protocol.KindContainer, Image: "x"}}

	n.mu.Lock()
	n.scheduleLocked(now)
	n.mu.Unlock()
	if tk := n.tasks["t"]; tk.AgentID != "b" || tk.State != StateAssigned || tk.Attempt != 2 {
		t.Fatalf("task not moved to b: %+v", tk)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0"}, quiet)
	n.mu.Lock()
	n.queueLocked("p", "persisted", protocol.TaskSpec{Kind: protocol.KindContainer, Image: "alpine"})
	token := n.joinToken
	n.mu.Unlock()
	n.j.Close() // simulate a power cut: no clean shutdown

	n2, err := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n2.j.Close() })
	if len(n2.tasks) != 1 || n2.joinToken != token {
		t.Fatalf("state lost: %d tasks, token match %v", len(n2.tasks), n2.joinToken == token)
	}
}

func TestSchedulerRespectsTrustAndFit(t *testing.T) {
	community := &Agent{Trust: protocol.TrustCommunity, Resources: protocol.Resources{OS: "linux", Runtimes: []string{"docker", "exec"}}}
	exec := protocol.TaskSpec{Kind: protocol.KindExec, Command: []string{"x"}, OS: []string{"linux"}}
	if canRun(community, exec) {
		t.Fatal("native program allowed on a community machine")
	}
	busy := &Agent{Resources: protocol.Resources{CPUCores: 8, CPUIdlePercent: 5, MemFree: 1 << 30}}
	if score(busy, protocol.TaskSpec{}, 0) >= 0 {
		t.Fatal("busy machine accepted work")
	}
	small := &Agent{Resources: protocol.Resources{CPUCores: 8, CPUIdlePercent: 90, MemFree: 1 << 30}}
	if score(small, protocol.TaskSpec{MemoryMB: 4096}, 0) >= 0 {
		t.Fatal("task placed on a machine without enough memory")
	}
}
