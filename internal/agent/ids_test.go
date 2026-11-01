package agent

import (
	"testing"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// TestRefusesPathsAsIDs: task IDs name folders on this machine, so a node
// can't use one to reach outside them.
func TestRefusesPathsAsIDs(t *testing.T) {
	a := &Agent{cfg: Config{Trust: protocol.TrustCommunity}, tasksDir: t.TempDir()}
	spec := protocol.TaskSpec{Kind: protocol.KindContainer, Image: "alpine:3"}
	for _, id := range []string{"../../home/alice", "..", "a/b", `a\b`, "", "-rf", "x\x00"} {
		if a.refuse(protocol.Task{ID: id, Spec: spec}) == "" {
			t.Errorf("accepted task ID %q", id)
		}
		if res := a.execute(t.Context(), protocol.Task{ID: id, Spec: spec}); res.Outcome != protocol.OutcomeRefused {
			t.Errorf("executed task ID %q: %+v", id, res)
		}
	}
	if r := a.refuse(protocol.Task{ID: "0275faa725f2", Spec: spec}); r == "invalid task ID" {
		t.Error("refused a normal ID")
	}
}
