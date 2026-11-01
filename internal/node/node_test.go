package node

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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
	switch os.Getenv("SYS1_TEST_HELPER") {
	case "worker":
		// Answers every connection with its index, until stopped.
		ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("SYS1_PEER_PORT"))
		if err != nil {
			os.Exit(5)
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(6)
			}
			fmt.Fprintf(c, "worker %s\n", os.Getenv("SYS1_SHARD_INDEX"))
			c.Close()
		}
	case "honest-or-not":
		// Machine "gamma" lies: it writes a different answer.
		here, _ := os.Getwd()
		answer := "sum of piece " + os.Getenv("SYS1_SHARD_INDEX") + " is 42"
		if strings.Contains(filepath.ToSlash(here), "/clients/gamma/") {
			answer = "sum of piece " + os.Getenv("SYS1_SHARD_INDEX") + " is 41"
		}
		os.WriteFile(filepath.Join(os.Getenv("SYS1_VOLUME_0"), "answer.txt"), []byte(answer), 0o644)
		os.Exit(0)
	case "head":
		// Talks to every peer, prints what they said, and finishes.
		var said []string
		for _, p := range strings.Split(os.Getenv("SYS1_PEERS"), ",") {
			c, err := net.DialTimeout("tcp", p, 5*time.Second)
			if err != nil {
				fmt.Println("cannot reach", p, err)
				os.Exit(7)
			}
			b, _ := io.ReadAll(c)
			c.Close()
			said = append(said, strings.TrimSpace(string(b)))
		}
		sort.Strings(said)
		fmt.Println("peers:", strings.Join(said, ", "))
		os.Exit(0)
	}
	if os.Getenv("SYS1_TEST_HELPER") == "shard" {
		// Writes its piece of the result. Shard 2 crashes on every machine
		// until it has been moved once, to prove shards retry elsewhere.
		i, n := os.Getenv("SYS1_SHARD_INDEX"), os.Getenv("SYS1_SHARD_COUNT")
		if i == "2" {
			cp := os.Getenv("SYS1_CHECKPOINT")
			here, _ := os.Getwd()
			if b, err := os.ReadFile(cp); err != nil || string(b) == here {
				os.WriteFile(cp, []byte(here), 0o600)
				os.Exit(4)
			}
		}
		os.WriteFile(filepath.Join(os.Getenv("SYS1_VOLUME_0"), "result.txt"), []byte("piece "+i+" of "+n), 0o644)
		fmt.Println("piece", i, "done")
		os.Exit(0)
	}
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
	res := protocol.Resources{OS: "test", CPUCores: 4, CPUIdlePercent: 90, MemFree: 8 << 30, MemTotal: 16 << 30, Runtimes: []string{"exec"}, Addr: "127.0.0.1"}
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

// TestBatchJob splits a job into 6 shards over 3 machines; one shard keeps
// crashing on its first machine and must finish on another; then every
// shard's output is collected into one folder.
func TestBatchJob(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	var ids []string
	for _, name := range []string{"alpha", "beta", "gamma"} {
		id, _ := joinAgent(t, ctx, n, name)
		ids = append(ids, id)
	}
	exe, _ := os.Executable()
	n.mu.Lock()
	j, err := n.queueJobLocked("test", "pieces", protocol.TaskSpec{
		Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"},
		Env: map[string]string{"SYS1_TEST_HELPER": "shard"}, Volumes: []string{"/out"}, Shards: 6,
	})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 90*time.Second, "all shards to finish with saved output", func() bool {
		p := n.progressLocked(j)
		return p.Failed > 0 || (p.Done == 6 && p.Saved == 6)
	})
	n.mu.Lock()
	p := n.progressLocked(j)
	used := map[string]bool{}
	for _, c := range j.Children {
		used[n.tasks[c].AgentID] = true
	}
	moved := n.tasks[j.Children[2]].Attempt > 1
	n.mu.Unlock()
	if p.Done != 6 {
		t.Fatalf("progress %+v", p)
	}
	if len(used) < 2 {
		t.Fatalf("all shards ran on %d machine(s); expected them spread out", len(used))
	}
	if !moved {
		t.Fatal("the crashing shard was never moved to another machine")
	}

	dest := filepath.Join(t.TempDir(), "results")
	count, err := n.CollectJob(j.ID, ids[0], dest)
	if err != nil || count != 6 {
		t.Fatalf("collect: %d, %v", count, err)
	}
	waitFor(t, n, 60*time.Second, "results to be restored", func() bool {
		for _, r := range n.pool.restores {
			if r.State == "pending" {
				return false
			}
		}
		return true
	})
	for i := 0; i < 6; i++ {
		f := filepath.Join(dest, fmt.Sprintf("shard-%04d", i), "vol0", "result.txt")
		got, err := os.ReadFile(f)
		if want := fmt.Sprintf("piece %d of 6", i); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v (want %q)", f, got, err, want)
		}
	}
}

func TestSchedulerGPUMemory(t *testing.T) {
	small := &Agent{Trust: protocol.TrustOwner, Resources: protocol.Resources{OS: "linux", Runtimes: []string{"docker"},
		GPUInfo: []protocol.GPU{{Vendor: "nvidia", VRAMMB: 8192, FreeMB: 6000}}}}
	big := &Agent{Trust: protocol.TrustOwner, Resources: protocol.Resources{OS: "linux", Runtimes: []string{"docker"},
		GPUInfo: []protocol.GPU{{Vendor: "nvidia", VRAMMB: 24576, FreeMB: 20000}}}}
	mac := &Agent{Trust: protocol.TrustOwner, Resources: protocol.Resources{OS: "darwin", Runtimes: []string{"docker"},
		GPUInfo: []protocol.GPU{{Vendor: "apple", VRAMMB: 64000}}}}
	spec := protocol.TaskSpec{Kind: protocol.KindContainer, Image: "x", MinVRAMMB: 12000}
	if canRun(small, spec) || !canRun(big, spec) {
		t.Fatal("GPU memory requirement not enforced")
	}
	if canRun(mac, spec) {
		t.Fatal("container sent to an Apple GPU it can't use")
	}
}

// TestGroup runs two workers and a head on three different machines; the
// head reaches both workers through SYS1_PEERS.
func TestGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		joinAgent(t, ctx, n, name)
	}
	exe, _ := os.Executable()
	port := 30000 + int(time.Now().UnixNano()%20000)
	n.mu.Lock()
	j, err := n.queueGroupLocked("test", "group", protocol.TaskSpec{
		Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"}, Network: true,
		Env: map[string]string{"SYS1_TEST_HELPER": "head"},
		Group: &protocol.GroupSpec{Workers: 2, PeerPort: port, Worker: protocol.TaskSpec{
			Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"},
			Env: map[string]string{"SYS1_TEST_HELPER": "worker"},
		}},
	})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	head := n.tasks[j.Head]
	waitFor(t, n, 60*time.Second, "the head to finish", func() bool { return terminal(head.State) })
	n.mu.Lock()
	defer n.mu.Unlock()
	if head.State != StateDone || !strings.Contains(head.Output, "peers: worker 0, worker 1") {
		t.Fatalf("head %s: %q (%s)", head.State, head.Output, head.Message)
	}
	machines := map[string]bool{}
	for _, c := range j.Children {
		machines[n.tasks[c].AgentID] = true
	}
	if len(machines) != 3 {
		t.Fatalf("group ran on %d machines, want 3 different ones", len(machines))
	}
}

func TestGroupHeadRestartsWhenAWorkerMoves(t *testing.T) {
	n, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.j.Close() })
	now := time.Now()
	res := func(addr string) protocol.Resources {
		return protocol.Resources{CPUCores: 4, CPUIdlePercent: 90, MemFree: 8 << 30, Runtimes: []string{"docker"}, Addr: addr}
	}
	for _, id := range []string{"a", "b", "c"} {
		n.agents[id] = &Agent{ID: id, Name: id, Resources: res("10.0.0." + id), LastSeen: now}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	j, err := n.queueGroupLocked("p", "g", protocol.TaskSpec{Kind: protocol.KindContainer, Image: "head", Network: true,
		Command: []string{"--rpc", "${SYS1_PEERS}"},
		Group:   &protocol.GroupSpec{Workers: 2, PeerPort: 50052, Worker: protocol.TaskSpec{Kind: protocol.KindContainer, Image: "w"}}})
	if err != nil {
		t.Fatal(err)
	}
	w0, w1, head := n.tasks[j.Children[0]], n.tasks[j.Children[1]], n.tasks[j.Head]
	if w0.AgentID == w1.AgentID {
		t.Fatal("two workers on one machine")
	}
	if head.State != StateWaiting {
		t.Fatalf("head started before its workers: %s", head.State)
	}
	w0.State, w1.State = StateRunning, StateRunning
	n.scheduleLocked(now)
	if head.State != StateAssigned || !strings.Contains(head.Spec.Command[1], ":50052") || !strings.Contains(head.Spec.Command[1], ":50053") {
		t.Fatalf("head not started with both peers: %s %v", head.State, head.Spec.Command)
	}
	if head.AgentID == w0.AgentID || head.AgentID == w1.AgentID {
		t.Fatal("head shares a machine with a worker")
	}
	attempt := head.Attempt
	n.requeueLocked(w1, "moved", false) // a worker's machine went away
	n.scheduleLocked(now)
	if head.State != StateWaiting || head.Attempt != attempt+1 {
		t.Fatalf("head kept running with a stale worker: %s attempt %d", head.State, head.Attempt)
	}
}

// TestVerifiedJob runs each piece on two machines and accepts only matching
// output. Machine gamma lies; its answers must never be accepted, and it
// must be marked for disagreeing.
func TestVerifiedJob(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	n, ctx := startNode(t)
	ids := map[string]string{}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		id, _ := joinAgent(t, ctx, n, name)
		ids[name] = id
	}
	exe, _ := os.Executable()
	n.mu.Lock()
	j, err := n.queueJobLocked("test", "verified", protocol.TaskSpec{
		Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"}, OS: []string{"any"},
		Env: map[string]string{"SYS1_TEST_HELPER": "honest-or-not"}, Volumes: []string{"/out"}, Shards: 4, Verify: 2,
	})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 120*time.Second, "every piece to be verified", func() bool {
		p := n.progressLocked(j)
		return p.Done+p.Failed == 4
	})
	n.mu.Lock()
	p := n.progressLocked(j)
	gammaRan := false
	for _, c := range j.Children {
		if tk := n.tasks[c]; tk.AgentID == ids["gamma"] && tk.State == StateDone {
			gammaRan = true
		}
	}
	disagreed := n.agents[ids["gamma"]].Disagreed
	honest := n.agents[ids["alpha"]].Disagreed + n.agents[ids["beta"]].Disagreed
	n.mu.Unlock()
	if p.Done != 4 {
		t.Fatalf("progress %+v, rejected %v", p, j.Rejected)
	}
	if gammaRan && disagreed == 0 {
		t.Fatal("gamma's wrong answers went unnoticed")
	}
	if honest != 0 {
		t.Fatalf("honest machines marked as disagreeing %d times", honest)
	}

	dest := filepath.Join(t.TempDir(), "results")
	if _, err := n.CollectJob(j.ID, ids["alpha"], dest); err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 60*time.Second, "results to be restored", func() bool {
		for _, r := range n.pool.restores {
			if r.State == "pending" {
				return false
			}
		}
		return true
	})
	for i := 0; i < 4; i++ {
		got, _ := os.ReadFile(filepath.Join(dest, fmt.Sprintf("shard-%04d", i), "vol0", "answer.txt"))
		if want := fmt.Sprintf("sum of piece %d is 42", i); string(got) != want {
			t.Fatalf("piece %d accepted %q, want %q", i, got, want)
		}
	}
	t.Logf("gamma ran a piece: %v, disagreements recorded: %d", gammaRan, disagreed)
}

// TestClientFailsOverBetweenAddresses gives a client a dead address first;
// it must reach the node on the next one and keep working.
func TestClientFailsOverBetweenAddresses(t *testing.T) {
	n, ctx := startNode(t)
	code := n.LocalJoinCode()
	good := code.Addr
	code.Addr, code.More = "127.0.0.1:1", []string{good} // port 1: nothing listens
	cfg, err := agent.Register(ctx, code, "roamer", protocol.TrustOwner, protocol.Resources{OS: "test", CPUCores: 1}, "test")
	if err != nil {
		t.Fatalf("register with a dead first address: %v", err)
	}
	if cfg.NodeAddr != good || cfg.NodeAddrs[0] != good {
		t.Fatalf("config should start with the address that worked: %+v", cfg.NodeAddrs)
	}
	cfg.NodeAddrs = []string{"127.0.0.1:1", good} // dead one first again
	a, err := agent.New(filepath.Join(n.cfg.DataDir, "clients", "roamer"), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.Run(actx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, n, 20*time.Second, "the client to report in", func() bool {
		ag := n.agents[cfg.AgentID]
		return ag != nil && n.online(ag, time.Now()) && ag.Version == "dev"
	})
}

func TestScreenPageMayConnectToMachines(t *testing.T) {
	n, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.j.Close() })
	h := n.guiHandler("127.0.0.1:7700")
	csp := func(path string) string {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "127.0.0.1:7700"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Header().Get("Content-Security-Policy")
	}
	if !strings.Contains(csp("/screen.html"), "connect-src 'self' ws: wss:") {
		t.Fatal("the screen page can't open WebSockets to machines")
	}
	if strings.Contains(csp("/"), "ws:") {
		t.Fatal("the dashboard itself shouldn't allow WebSockets elsewhere")
	}
}
