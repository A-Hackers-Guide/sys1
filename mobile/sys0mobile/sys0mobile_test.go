package sys0mobile

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/node"
)

// TestJoinStartStop runs the mobile client against a real node, the way
// the iOS app uses it.
func TestJoinStartStop(t *testing.T) {
	root := t.TempDir()
	n, err := node.New(node.Config{DataDir: filepath.Join(root, "node"), ListenAddr: "127.0.0.1:0", GUIAddr: "127.0.0.1:0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready, served := make(chan struct{}), make(chan struct{})
	go func() { n.Serve(ctx, func(string, string) { close(ready) }); close(served) }()
	<-ready
	t.Cleanup(func() { cancel(); <-served })

	dir := filepath.Join(root, "phone")
	if Joined(dir) {
		t.Fatal("joined before joining")
	}
	if err := Join(dir, "not a code", "phone", false, 1); err == nil {
		t.Fatal("accepted a bad join code")
	}
	if err := Join(dir, n.JoinCode().String(), "test phone", false, 1); err != nil {
		t.Fatal(err)
	}
	if !Joined(dir) {
		t.Fatal("not joined after joining")
	}
	if err := Start(dir); err != nil {
		t.Fatal(err)
	}
	Start(dir) // a second start does nothing
	if !Running() {
		t.Fatal("not running")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !n.HasOnlineMachine("test phone") {
		time.Sleep(200 * time.Millisecond)
	}
	if !n.HasOnlineMachine("test phone") {
		t.Fatal("the node never saw the device")
	}
	Stop()
	if Running() {
		t.Fatal("still running after Stop")
	}
	if err := Leave(dir); err != nil || Joined(dir) {
		t.Fatalf("leave: %v, joined=%v", err, Joined(dir))
	}
}
