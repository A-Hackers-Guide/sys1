package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

func TestCloudInitServer(t *testing.T) {
	url, srv, err := cloudInitServer("abc", "#cloud-config\nhostname: test\n")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if !strings.HasPrefix(url, "http://10.0.2.2:") {
		t.Fatalf("guest URL %q", url)
	}
	local := strings.Replace(url, "10.0.2.2", "127.0.0.1", 1)
	for path, want := range map[string]string{"meta-data": "instance-id: sys1-abc", "user-data": "hostname: test"} {
		resp, err := http.Get(local + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), want) {
			t.Errorf("%s = %q", path, b)
		}
	}
}

// TestVMBoots boots a real (tiny) VM. CI runs it on Linux with QEMU
// installed and SYS1_QEMU_TESTS=1.
func TestVMBoots(t *testing.T) {
	if os.Getenv("SYS1_QEMU_TESTS") != "1" {
		t.Skip("set SYS1_QEMU_TESTS=1 to boot a real VM with QEMU")
	}
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("QEMU not installed")
	}
	dataDir := t.TempDir()
	a := &Agent{
		cfg: Config{Trust: protocol.TrustOwner}, dataDir: dataDir, vms: map[string]*vmProc{},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), last: protocol.Resources{Runtimes: []string{"qemu-x86_64"}},
	}
	sshPort, _ := freeLocalPort()
	task := protocol.Task{ID: "vmtest", Attempt: 1, Spec: protocol.TaskSpec{
		Kind: protocol.KindVM, MemoryMB: 256, Network: true, Service: true, Ports: []string{strconv.Itoa(sshPort) + ":22"},
		VM: &protocol.VMSpec{
			Image:  "https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-x86_64-disk.img",
			SHA256: "07e44a73e54c94d988028515403c1ed762055e01b83a767edf3c2b387f78ce00",
			CPUs:   1, DiskGB: 1, Screen: true,
		},
	}}
	dir := t.TempDir()
	os.MkdirAll(dir+"/state", 0o700)
	out := &tail{max: 64 << 10}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.runOnce(ctx, task, dir, out); done <- err }()

	waitFor := func(d time.Duration, what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				t.Fatalf("VM exited early (%v) while waiting for %s:\n%s", err, what, out.String())
			default:
			}
			if cond() {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("timed out waiting for %s:\n%s", what, out.String())
	}
	waitFor(5*time.Minute, "a login prompt on the console", func() bool { return strings.Contains(out.String(), "login:") })

	waitFor(time.Minute, "SSH through the port forward", func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(sshPort), 2*time.Second)
		if err != nil {
			return false
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		line, _ := bufio.NewReader(c).ReadString('\n')
		return strings.HasPrefix(line, "SSH-2.0")
	})

	p := a.vmFor(task.ID)
	if p == nil || p.screen == nil || len(p.screen.Password) != 8 {
		t.Fatalf("no screen: %+v", p)
	}
	if err := checkVNCWebSocket("127.0.0.1:" + strconv.Itoa(p.screen.Port)); err != nil {
		t.Fatalf("screen: %v", err)
	}
	resume := p.pause()
	resume()

	cancel() // asks the guest to power down
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("VM didn't stop")
	}
	t.Logf("console tail:\n%s", out.String()[max(0, len(out.String())-600):])
}

// checkVNCWebSocket does what noVNC does first: a WebSocket upgrade, then
// the VNC handshake, and expects password authentication to be required.
func checkVNCWebSocket(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n\r\n", addr)
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "101") {
		return fmt.Errorf("websocket upgrade refused: %q", status)
	}
	for line, _ := r.ReadString('\n'); line != "\r\n" && line != ""; line, _ = r.ReadString('\n') {
	}
	readFrame := func() ([]byte, error) {
		h := make([]byte, 2)
		if _, err := io.ReadFull(r, h); err != nil {
			return nil, err
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			ext := make([]byte, 2)
			io.ReadFull(r, ext)
			n = int(ext[0])<<8 | int(ext[1])
		}
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	writeFrame := func(p []byte) {
		mask := []byte{1, 2, 3, 4}
		f := []byte{0x82, byte(0x80 | len(p))}
		f = append(f, mask...)
		for i, x := range p {
			f = append(f, x^mask[i%4])
		}
		c.Write(f)
	}
	ver, err := readFrame()
	if err != nil || !strings.HasPrefix(string(ver), "RFB 003.") {
		return fmt.Errorf("no VNC greeting: %q %v", ver, err)
	}
	writeFrame([]byte("RFB 003.008\n"))
	sec, err := readFrame()
	if err != nil || len(sec) < 2 {
		return fmt.Errorf("no security types: %v", err)
	}
	for _, t := range sec[1:] {
		if t == 1 {
			return errors.New("the screen allows access without a password")
		}
	}
	for _, t := range sec[1:] {
		if t == 2 {
			return nil // VNC password authentication
		}
	}
	return fmt.Errorf("unexpected security types %v", sec[1:])
}
