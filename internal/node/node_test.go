package node

import (
	"bytes"
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
	if os.Getenv("SYS1_TEST_HELPER") == "volume" {
		// Finds its data if it was restored here; otherwise writes it and
		// keeps running until its machine goes away.
		f := filepath.Join(os.Getenv("SYS1_VOLUME_0"), "hello.txt")
		if b, err := os.ReadFile(f); err == nil {
			fmt.Printf("found: %s\n", b)
			os.Exit(0)
		}
		here, _ := os.Getwd()
		os.WriteFile(f, []byte("written at "+here), 0o644)
		time.Sleep(time.Hour)
		os.Exit(0)
	}
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
	repairWarmup = 0
	n, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", GUIAddr: "127.0.0.1:0", Copies: 2}, quiet)
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

func joinAgent(t *testing.T, ctx context.Context, n *Node, name string) (agentID string, stop func()) {
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
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.Run(actx); close(done) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	return cfg.AgentID, stop
}

// waitFor polls cond (under the node lock) until it holds or time runs out.
func waitFor(t *testing.T, n *Node, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		ok := cond()
		n.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t.Fatalf("timed out waiting for %s (pool health %+v)", what, n.pool.health)
}

// TestAppDataMovesWithApp: an app writes to its volume, its machine
// vanishes, and the app comes back on another machine with its data.
func TestAppDataMovesWithApp(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	stops := map[string]func(){}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		id, stop := joinAgent(t, ctx, n, name)
		stops[id] = stop
	}
	exe, _ := os.Executable()
	n.mu.Lock()
	task, err := n.queueLocked("test", "stateful", protocol.TaskSpec{
		Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"},
		Env: map[string]string{"SYS1_TEST_HELPER": "volume"}, Volumes: []string{"/data"}, SnapshotSeconds: 1,
	})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 60*time.Second, "a replicated snapshot", func() bool {
		return task.Snapshot != "" && n.pool.health.Chunks > 0 && n.pool.health.Healthy == n.pool.health.Chunks
	})
	n.mu.Lock()
	first := task.AgentID
	n.mu.Unlock()
	stops[first]() // the machine running the app disappears

	waitFor(t, n, 90*time.Second, "the app to finish on another machine", func() bool {
		return task.State == StateDone || task.State == StateFailed
	})
	n.mu.Lock()
	defer n.mu.Unlock()
	if task.State != StateDone || !strings.Contains(task.Output, "found: written at") {
		t.Fatalf("state=%s output=%q message=%q", task.State, task.Output, task.Message)
	}
	if task.AgentID == first {
		t.Fatal("app finished on the machine that was stopped")
	}
}

// TestBackupRepairRestore: a folder is backed up with two copies, a machine
// holding a copy is removed, the pool re-makes the copy, and the folder is
// restored on another machine, byte for byte.
func TestBackupRepairRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	var ids []string
	stops := map[string]func(){}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		id, stop := joinAgent(t, ctx, n, name)
		ids = append(ids, id)
		stops[id] = stop
	}
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "photos"), 0o755)
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("remember the milk"), 0o644)
	big := make([]byte, 9<<20) // spans three chunks
	for i := range big {
		big[i] = byte(i * 7)
	}
	os.WriteFile(filepath.Join(src, "photos", "big.raw"), big, 0o600)

	waitFor(t, n, 20*time.Second, "machines to report storage", func() bool {
		for _, id := range ids {
			if n.agents[id].Storage.QuotaBytes == 0 {
				return false
			}
		}
		return true
	})
	b, err := n.AddBackup(ids[0], src, 2, 60, 3)
	if err != nil {
		t.Fatal(err)
	}
	var root string
	waitFor(t, n, 60*time.Second, "the backup to be fully replicated", func() bool {
		root = n.latestSnapshotLocked("backup:" + b.ID)
		return root != "" && n.pool.health.Chunks > 0 && n.pool.health.Healthy == n.pool.health.Chunks
	})

	// Backing up again with nothing changed must not make a new version.
	if err := n.RunBackup(b.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second) // several heartbeats: the run is seen and finishes
	n.mu.Lock()
	versions := 0
	for _, s := range n.pool.snapshots {
		if s.Owner == "backup:"+b.ID {
			versions++
		}
	}
	n.mu.Unlock()
	if versions != 1 {
		t.Fatalf("unchanged folder produced %d versions, want 1", versions)
	}

	// Remove a machine that holds copies; the pool must re-make them.
	var victim string
	n.mu.Lock()
	for _, id := range ids[1:] {
		if len(n.pool.held[id]) > 0 {
			victim = id
		}
	}
	n.mu.Unlock()
	if victim == "" {
		t.Fatal("no second machine holds a copy")
	}
	stops[victim]()
	if err := n.RemoveAgent(victim); err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 60*time.Second, "copies to be re-made", func() bool {
		return n.pool.health.Healthy == n.pool.health.Chunks
	})

	var target string
	for _, id := range ids[1:] {
		if id != victim {
			target = id
		}
	}
	dest := filepath.Join(t.TempDir(), "restored")
	rs, err := n.StartRestore(root, target, dest)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 60*time.Second, "the restore", func() bool { return rs.State != "pending" })
	if rs.State != "done" {
		t.Fatalf("restore %s: %s", rs.State, rs.Message)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "notes.txt")); string(got) != "remember the milk" {
		t.Fatalf("notes.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "photos", "big.raw")); !bytes.Equal(got, big) {
		t.Fatal("big.raw differs after restore")
	}
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
