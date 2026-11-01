package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// TestBuildFromGitHub builds testdata/build-app straight from this
// repository on GitHub, at the commit CI is testing, and runs it.
func TestBuildFromGitHub(t *testing.T) {
	a, dir := dockerAgent(t)
	commit := os.Getenv("SYS1_BUILD_COMMIT")
	if commit == "" {
		t.Skip("set SYS1_BUILD_COMMIT to a pushed commit of A-Hackers-Guide/sys1")
	}
	a.cfg.Trust = protocol.TrustOwner
	task := protocol.Task{ID: "bld" + strings.ToLower(time.Now().Format("150405")), Attempt: 1, Spec: protocol.TaskSpec{
		Kind: protocol.KindContainer, OwnerOnly: true,
		Build: &protocol.BuildSpec{Repo: "A-Hackers-Guide/sys1", Commit: commit, Dir: "internal/agent/testdata/build-app"},
	}}
	if err := task.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	out := &tail{max: 64 << 10}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := a.runOnce(ctx, task, dir, out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "built from GitHub by sys1") {
		t.Fatalf("the built app didn't run:\n%s", out.String())
	}
	// A second run reuses the image instead of building again.
	out2 := &tail{max: 64 << 10}
	if _, err := a.runOnce(ctx, task, dir, out2); err != nil || strings.Contains(out2.String(), "sys1: building") {
		t.Fatalf("rebuilt an image it already had (%v):\n%s", err, out2.String())
	}
	exec.Command("docker", "image", "rm", "sys1-build-"+task.ID+":"+commit[:12]).Run()
}
