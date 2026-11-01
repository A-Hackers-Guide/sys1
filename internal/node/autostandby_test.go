package node

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// TestNodePicksStandbyAndWitness: a pool of one node and two of the
// owner's machines that can host. The node asks the bigger one to be its
// standby and the other to be its witness; each sets itself up; then the
// pool survives losing the node.
func TestNodePicksStandbyAndWitness(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	repairWarmup = 0
	root := t.TempDir()
	a := startClusterNode(t, filepath.Join(root, "main"), freePort(t), freePort(t))
	waitTrue(t, 10*time.Second, "the node to lead", a.n.leading)

	var mu sync.Mutex
	hosted := map[string]*clusterNode{}
	machine := func(name string, mem uint64) {
		res := protocol.Resources{OS: "test", CPUCores: 2, CPUIdlePercent: 90, MemTotal: mem, MemFree: mem / 2, Runtimes: []string{"exec"}, Addr: "127.0.0.1"}
		cfg, err := agent.Register(context.Background(), a.n.OwnerJoinCode(), name, protocol.TrustOwner, res, "test")
		if err != nil {
			t.Fatal(err)
		}
		ag, err := agent.New(filepath.Join(root, name, "sys0"), cfg, quiet)
		if err != nil {
			t.Fatal(err)
		}
		ag.Sample = func(bool) protocol.Resources { return res }
		dir, api, raftAddr := filepath.Join(root, name, "sys1"), freePort(t), freePort(t)
		ag.Host = func(ctx context.Context, args ...string) error {
			switch strings.Join(args[:2], " ") {
			case "cluster join":
				witness := args[2] == "--witness"
				return JoinCluster(ctx, dir, args[len(args)-1], raftAddr, []string{api}, witness)
			case "install --cluster":
				node := startClusterNode(t, dir, api, raftAddr)
				mu.Lock()
				hosted[name] = node
				mu.Unlock()
			}
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { ag.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	machine("big", 32<<30)
	machine("small", 4<<30)

	waitTrue(t, 60*time.Second, "a standby and a witness, all three voting", func() bool {
		st := a.n.ClusterStatus()
		voters, witnesses := 0, 0
		for _, m := range st.Members {
			if m.Voter {
				voters++
			}
			if m.Witness {
				witnesses++
			}
		}
		return voters == 3 && witnesses == 1
	})
	mu.Lock()
	big, small := hosted["big"], hosted["small"]
	mu.Unlock()
	if big == nil || small == nil || big.n.Witness() || !small.n.Witness() {
		t.Fatal("the bigger machine should be the standby and the other the witness")
	}

	a.stop()
	waitTrue(t, 20*time.Second, "the standby to take over", big.n.leading)
	if small.n.leading() {
		t.Fatal("the witness must not lead")
	}
}
