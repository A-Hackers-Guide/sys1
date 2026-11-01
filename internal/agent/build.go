package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// buildTimeout bounds one image build.
const buildTimeout = 30 * time.Minute

// buildImage builds the task's image from its repository at the commit the
// node chose, unless this machine already has it, and returns its tag.
// Older builds of the same app are removed.
func (a *Agent) buildImage(ctx context.Context, rt string, t protocol.Task, out io.Writer) (string, error) {
	b := t.Spec.Build
	if b.Commit == "" {
		return "", errors.New("the node hasn't picked a commit to build yet")
	}
	repo := "sys1-build-" + strings.ToLower(t.ID)
	tag := repo + ":" + b.Commit[:12]
	if exec.CommandContext(ctx, rt, "image", "inspect", tag).Run() == nil {
		return tag, nil
	}
	fmt.Fprintf(out, "sys1: building %s at %s\n", b.Repo, b.Commit[:12])
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	args := []string{"build", "--pull", "-t", tag, "--label", "sys1.task=" + t.ID}
	if b.Dockerfile != "" {
		args = append(args, "-f", b.Dockerfile)
	}
	args = append(args, b.GitContext())
	cmd := exec.CommandContext(bctx, rt, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if bctx.Err() != nil {
			return "", fmt.Errorf("the build took longer than %s", buildTimeout)
		}
		return "", fmt.Errorf("building %s failed: %w", b.Repo, err)
	}
	// Keep only this build.
	if list, err := exec.Command(rt, "image", "ls", repo, "--format", "{{.Repository}}:{{.Tag}}").Output(); err == nil {
		for _, img := range strings.Fields(string(list)) {
			if img != tag {
				exec.Command(rt, "image", "rm", img).Run()
			}
		}
	}
	return tag, nil
}
