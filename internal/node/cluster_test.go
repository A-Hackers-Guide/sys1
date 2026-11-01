package node

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type clusterNode struct {
	n    *Node
	stop func()
}

func startClusterNode(t *testing.T, dir, api, raftAddr string) *clusterNode {
	t.Helper()
	n, err := New(Config{DataDir: dir, ListenAddr: api, GUIAddr: "127.0.0.1:0", Cluster: true, RaftAddr: raftAddr, FastRaft: true, Copies: 2}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		if err := n.Serve(ctx, func(string, string) { close(ready) }); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-ready:
	case <-done:
		t.Fatal("node did not start")
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Cleanup(stop)
	return &clusterNode{n, stop}
}

func waitTrue(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestClusterSurvivesLeaderLoss: three nodes; the leader dies; another
// takes over with the same state, and machines follow it on their own.
func TestClusterSurvivesLeaderLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	repairWarmup = 0
	root := t.TempDir()
	a := startClusterNode(t, filepath.Join(root, "a"), freePort(t), freePort(t))
	waitTrue(t, 10*time.Second, "node a to lead", a.n.leading)
	var code string
	waitTrue(t, 10*time.Second, "a cluster code", func() bool { c, err := a.n.ClusterCode(); code = c; return err == nil })

	nodes := []*clusterNode{a}
	for _, name := range []string{"b", "c"} {
		dir, api, raftAddr := filepath.Join(root, name), freePort(t), freePort(t)
		if err := JoinCluster(context.Background(), dir, code, raftAddr, []string{api}, false); err != nil {
			t.Fatalf("%s join: %v", name, err)
		}
		nodes = append(nodes, startClusterNode(t, dir, api, raftAddr))
		want := len(nodes)
		if want%2 == 0 {
			want-- // voters stay odd: the second node is a copy without a vote
		}
		waitTrue(t, 20*time.Second, name+" to join with the right votes", func() bool {
			voters := 0
			for _, m := range a.n.ClusterStatus().Members {
				if m.Voter {
					voters++
				}
			}
			return voters == want && a.n.leading()
		})
	}
	b, c := nodes[1].n, nodes[2].n
	waitTrue(t, 20*time.Second, "all three nodes to be voters with the cluster's state", func() bool {
		st := a.n.ClusterStatus()
		voters := 0
		for _, m := range st.Members {
			if m.Voter {
				voters++
			}
		}
		b.mu.Lock()
		bt := b.joinToken
		b.mu.Unlock()
		c.mu.Lock()
		ct := c.joinToken
		c.mu.Unlock()
		return voters == 3 && bt == a.n.joinToken && ct == a.n.joinToken
	})
	if b.fp != a.n.fp || c.fp != a.n.fp {
		t.Fatal("joined nodes don't share the cluster's identity")
	}

	// A machine joins with a code naming all three nodes.
	jc := a.n.OwnerJoinCode()
	if len(jc.Addrs()) != 3 {
		t.Fatalf("join code should list all 3 nodes: %v", jc.Addrs())
	}
	res := protocol.Resources{OS: "test", CPUCores: 4, CPUIdlePercent: 90, MemFree: 8 << 30, Runtimes: []string{"exec"}, Addr: "127.0.0.1"}
	cfg, err := agent.Register(context.Background(), jc, "worker", protocol.TrustOwner, res, "test")
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(filepath.Join(root, "worker"), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ag.Sample = func(bool) protocol.Resources { return res }
	actx, acancel := context.WithCancel(context.Background())
	adone := make(chan struct{})
	go func() { ag.Run(actx); close(adone) }()
	t.Cleanup(func() { acancel(); <-adone })

	exe, _ := os.Executable()
	a.n.mu.Lock()
	task, err := a.n.queueLocked("test", "keeps running", protocol.TaskSpec{Kind: protocol.KindExec, Command: []string{exe, "-test.run=^$"},
		OS: []string{"any"}, Env: map[string]string{"SYS1_TEST_HELPER": "worker", "SYS1_PEER_PORT": freePortNum(t), "SYS1_SHARD_INDEX": "0"}, Service: true})
	a.n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	id := task.ID
	waitTrue(t, 30*time.Second, "the app to run", func() bool {
		a.n.mu.Lock()
		defer a.n.mu.Unlock()
		return task.State == StateRunning
	})

	nodes[0].stop() // the leader dies
	var leader *Node
	waitTrue(t, 20*time.Second, "a new leader", func() bool {
		for _, x := range []*Node{b, c} {
			if x.leading() {
				leader = x
				return true
			}
		}
		return false
	})
	waitTrue(t, 30*time.Second, "the machine to report to the new leader", func() bool {
		leader.mu.Lock()
		defer leader.mu.Unlock()
		ag := leader.agents[cfg.AgentID]
		return ag != nil && time.Since(ag.LastSeen) < 3*time.Second && ag.Resources.CPUCores == 4
	})
	leader.mu.Lock()
	t2 := leader.tasks[id]
	leader.mu.Unlock()
	if t2 == nil || t2.State != StateRunning || t2.AgentID != cfg.AgentID {
		t.Fatalf("app after failover: %+v", t2)
	}
	if err := leader.StopTask(id); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, 30*time.Second, "the new leader to stop the app", func() bool {
		leader.mu.Lock()
		defer leader.mu.Unlock()
		return leader.tasks[id].State == StateStopped
	})
	// The change reached the remaining standby too.
	other := b
	if leader == b {
		other = c
	}
	waitTrue(t, 10*time.Second, "the standby to see the change", func() bool {
		other.mu.Lock()
		defer other.mu.Unlock()
		return other.tasks[id] != nil && other.tasks[id].State == StateStopped
	})
}

func freePortNum(t *testing.T) string {
	_, p, _ := net.SplitHostPort(freePort(t))
	return p
}
