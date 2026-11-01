package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// These tests drive real Docker. CI runs them on Linux with
// SYS1_DOCKER_TESTS=1 and SYS1_GATEWAY_IMAGE set to a freshly built image.
func dockerAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	if os.Getenv("SYS1_DOCKER_TESTS") != "1" {
		t.Skip("set SYS1_DOCKER_TESTS=1 to run against Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	a := &Agent{
		cfg:  Config{Trust: protocol.TrustCommunity},
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		last: protocol.Resources{Runtimes: []string{"docker"}},
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "state"), 0o777)
	return a, dir
}

func TestSandboxEgress(t *testing.T) {
	a, dir := dockerAgent(t)
	// busybox wget: -Y off ignores the proxy, so it tries to go direct.
	script := `wget -q -O /dev/null http://example.com && echo ALLOWED_OK || echo ALLOWED_FAIL
wget -q -O /dev/null http://neverssl.com && echo BLOCKED_LEAK || echo BLOCKED_OK
wget -q -T 5 -Y off -O /dev/null http://example.com && echo DIRECT_LEAK || echo DIRECT_OK
wget -q -T 5 -O /dev/null http://172.17.0.1 && echo LAN_LEAK || echo LAN_OK`
	task := protocol.Task{ID: "sbx" + strings.ToLower(time.Now().Format("150405")), Attempt: 1, Spec: protocol.TaskSpec{
		Kind: protocol.KindContainer, Image: "busybox:1.36", Command: []string{"sh", "-c", script},
		Network: true, Egress: []string{"example.com"},
	}}
	out := &tail{max: 4096}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := a.runOnce(ctx, task, dir, out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	got := out.String()
	t.Log(got)
	for _, want := range []string{"ALLOWED_OK", "BLOCKED_OK", "DIRECT_OK", "LAN_OK"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if out, _ := exec.Command("docker", "ps", "-a", "--filter", "label=sys1.task="+task.ID, "-q").Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Error("sandbox containers left behind")
	}
	if err := exec.Command("docker", "network", "inspect", "sys1-n-"+task.ID).Run(); err == nil {
		t.Error("sandbox network left behind")
	}
}

func TestSandboxInbound(t *testing.T) {
	a, dir := dockerAgent(t)
	task := protocol.Task{ID: "sbi" + strings.ToLower(time.Now().Format("150405")), Attempt: 1, Spec: protocol.TaskSpec{
		Kind: protocol.KindContainer, Image: "nginx:alpine", Network: true, Ports: []string{"18080:80"}, Service: true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runOnce(ctx, task, dir, &tail{max: 4096}); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://127.0.0.1:18080/"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(b), "nginx") {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("the app's port never answered through the gateway")
}
