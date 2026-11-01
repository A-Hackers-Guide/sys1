package crash

import "testing"

// The crash intake (intake/worker.js) computes signatures the same way;
// intake/worker.test.mjs checks the same value.
func TestSignatureMatchesTheIntake(t *testing.T) {
	r := &Report{Frames: []string{"github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:12)"}}
	if got := signature("sys0", r); got != "8b49c6d1a16e" {
		t.Fatalf("signature %s, the intake expects 8b49c6d1a16e", got)
	}
}
