package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestStandbyTakesOverAndHandsBack: a node, a standby and a witness. The
// witness never leads and isn't offered to machines. When the node stops,
// the standby takes over; when the node comes back, it leads again with
// the changes made meanwhile.
func TestStandbyTakesOverAndHandsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("slow end-to-end test")
	}
	repairWarmup = 0
	root := t.TempDir()
	adir, aapi, araft := filepath.Join(root, "main"), freePort(t), freePort(t)
	a := startClusterNode(t, adir, aapi, araft)
	waitTrue(t, 10*time.Second, "the main node to lead", a.n.leading)
	var code string
	waitTrue(t, 10*time.Second, "a cluster code", func() bool { c, err := a.n.ClusterCode(); code = c; return err == nil })

	join := func(name string, witness bool) *clusterNode {
		dir, api, raftAddr := filepath.Join(root, name), freePort(t), freePort(t)
		if err := JoinCluster(context.Background(), dir, code, raftAddr, []string{api}, witness); err != nil {
			t.Fatalf("%s join: %v", name, err)
		}
		return startClusterNode(t, dir, api, raftAddr)
	}
	b := join("standby", false)
	w := join("witness", true)
	if !w.n.Witness() || b.n.Witness() {
		t.Fatal("witness flag not kept")
	}
	waitTrue(t, 20*time.Second, "three voters, the main node preferred", func() bool {
		st := a.n.ClusterStatus()
		voters := 0
		for _, m := range st.Members {
			if m.Voter {
				voters++
			}
		}
		return voters == 3 && st.Preferred == a.n.cl.id
	})
	a.n.mu.Lock()
	waddrs := a.n.cl.members[w.n.cl.id].APIAddrs
	a.n.mu.Unlock()
	if len(waddrs) == 0 {
		t.Fatal("the witness's addresses aren't known")
	}
	for _, addr := range a.n.OwnerJoinCode().Addrs() {
		for _, wa := range waddrs {
			if addr == wa {
				t.Fatal("join codes must not send machines to a witness")
			}
		}
	}

	a.stop() // the main node goes down
	waitTrue(t, 20*time.Second, "the standby to take over", b.n.leading)
	if w.n.leading() {
		t.Fatal("a witness must never lead")
	}
	// A change while the main node is away.
	b.n.RotateJoinCode()
	b.n.mu.Lock()
	want := b.n.joinToken
	b.n.mu.Unlock()

	// The main node comes back and, once steady, leads again.
	a2 := startClusterNode(t, adir, aapi, araft)
	waitTrue(t, 30*time.Second, "the main node to lead again", a2.n.leading)
	if b.n.leading() || w.n.leading() {
		t.Fatal("two leaders")
	}
	a2.n.mu.Lock()
	got := a2.n.joinToken
	a2.n.mu.Unlock()
	if got != want {
		t.Fatalf("the main node lost the change made while it was away: %q != %q", got, want)
	}

	// Preferring the witness is refused; preferring the standby works.
	if err := a2.n.Prefer(w.n.cl.id); err == nil {
		t.Fatal("a witness can't be preferred")
	}
	if err := a2.n.Prefer(b.n.cl.id); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, 30*time.Second, "leadership to move to the newly preferred node", b.n.leading)
}
