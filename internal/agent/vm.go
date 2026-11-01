package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Virtual machines run under QEMU on the owner's own machines. The disk
// is a copy-on-write layer over a downloaded, checksum-verified base
// image; with a pooled volume that layer moves with the VM. QEMU's control
// socket (QMP) is used to set the screen password, pause the VM while a
// snapshot reads its disk, and ask the guest to shut down cleanly.

type vmProc struct {
	qmpAddr string
	screen  *protocol.Screen
	meta    *http.Server
}

// vmArch maps a VM's architecture to QEMU's binary suffix.
func vmArch(v *protocol.VMSpec) string {
	if v.Arch == "" {
		return "x86_64"
	}
	return v.Arch
}

// prepareVM gets the image and disk ready and builds the QEMU command.
func (a *Agent) prepareVM(ctx context.Context, t protocol.Task, dir string) (*exec.Cmd, *vmProc, error) {
	v := t.Spec.VM
	arch := vmArch(v)
	qemu, err := exec.LookPath("qemu-system-" + arch)
	if err != nil {
		return nil, nil, fmt.Errorf("QEMU for %s isn't installed on this machine", arch)
	}
	base, err := a.vmImage(ctx, v)
	if err != nil {
		return nil, nil, err
	}
	format, err := imageFormat(base)
	if err != nil {
		return nil, nil, err
	}
	diskDir := filepath.Join(dir, "state")
	if len(t.Spec.Volumes) > 0 {
		diskDir = filepath.Join(dir, "volumes", "vol0")
	}
	os.MkdirAll(diskDir, 0o700)
	disk := filepath.Join(diskDir, "disk.qcow2")
	size := v.DiskGB
	if size == 0 {
		size = 20
	}
	if _, err := os.Stat(disk); err != nil {
		if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-F", format, "-b", base, disk, strconv.Itoa(size)+"G").CombinedOutput(); err != nil {
			return nil, nil, fmt.Errorf("creating the VM disk: %v: %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		// The disk came from another machine, where the base image lived
		// elsewhere: point it at this machine's copy (same checksum).
		if out, err := exec.Command("qemu-img", "rebase", "-u", "-F", format, "-b", base, disk).CombinedOutput(); err != nil {
			return nil, nil, fmt.Errorf("attaching the VM disk: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}

	p := &vmProc{}
	qmpPort, err := freeLocalPort()
	if err != nil {
		return nil, nil, err
	}
	p.qmpAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(qmpPort))
	cpus := v.CPUs
	if cpus == 0 {
		cpus = 2
	}
	args := []string{"-name", "sys1-" + t.ID, "-cpu", "max", "-smp", strconv.Itoa(cpus), "-m", strconv.Itoa(t.Spec.MemoryMB),
		"-drive", "file=" + disk + ",if=virtio,cache=writeback",
		"-serial", "stdio", "-monitor", "none", "-qmp", "tcp:" + p.qmpAddr + ",server=on,wait=off"}
	args = append(args, accelArgs()...)
	if arch == "aarch64" {
		fw, err := aarch64Firmware(qemu)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "-machine", "virt", "-bios", fw)
	} else {
		args = append(args, "-machine", "q35")
	}
	nic := "user,model=virtio-net-pci"
	for _, port := range t.Spec.Ports {
		host, guest, _ := strings.Cut(strings.TrimSuffix(port, "/tcp"), ":")
		nic += ",hostfwd=tcp::" + host + "-:" + guest
	}
	args = append(args, "-nic", nic)
	if v.CloudInit != "" {
		url, srv, err := cloudInitServer(t.ID, v.CloudInit)
		if err != nil {
			return nil, nil, err
		}
		p.meta = srv
		args = append(args, "-smbios", "type=1,serial=ds=nocloud;s="+url)
	}
	if v.Screen {
		display, ws, err := freeVNC()
		if err != nil {
			return nil, nil, err
		}
		pw := make([]byte, 4)
		rand.Read(pw)
		p.screen = &protocol.Screen{Port: ws, Password: hex.EncodeToString(pw)} // VNC passwords are at most 8 characters
		args = append(args, "-vnc", fmt.Sprintf("0.0.0.0:%d,websocket=%d,password=on", display, ws), "-device", "qemu-xhci", "-device", "usb-tablet")
		if arch == "aarch64" {
			args = append(args, "-device", "virtio-gpu-pci", "-device", "usb-kbd")
		}
	} else {
		args = append(args, "-display", "none")
	}
	return exec.Command(qemu, args...), p, nil
}

// accelArgs lists hardware accelerators to try, with software emulation
// as the last resort; QEMU uses the first one that works.
func accelArgs() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{"-accel", "kvm", "-accel", "tcg"}
	case "darwin":
		return []string{"-accel", "hvf", "-accel", "tcg"}
	case "windows":
		return []string{"-accel", "whpx,kernel-irqchip=off", "-accel", "tcg"}
	}
	return []string{"-accel", "tcg"}
}

func aarch64Firmware(qemu string) (string, error) {
	share := filepath.Join(filepath.Dir(filepath.Dir(qemu)), "share", "qemu")
	for _, p := range []string{filepath.Join(share, "edk2-aarch64-code.fd"), "/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/qemu-efi-aarch64/QEMU_EFI.fd"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("UEFI firmware for aarch64 VMs not found (install QEMU's edk2 firmware)")
}

// started finishes setup once QEMU runs: the screen password.
func (p *vmProc) started() error {
	if p.screen == nil {
		return nil
	}
	return p.qmp("change-vnc-password", map[string]string{"password": p.screen.Password})
}

func (p *vmProc) close() {
	if p.meta != nil {
		p.meta.Close()
	}
}

// powerdown asks the guest to shut down, as pressing its power button.
func (p *vmProc) powerdown() { p.qmp("system_powerdown", nil) }

// pause and resume freeze the VM while a snapshot reads its disk.
func (p *vmProc) pause() func() {
	if p.qmp("stop", nil) != nil {
		return func() {}
	}
	return func() { p.qmp("cont", nil) }
}

// qmp runs one QMP command, retrying the connection while QEMU starts.
func (p *vmProc) qmp(cmd string, args any) error {
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if c, err = net.DialTimeout("tcp", p.qmpAddr, time.Second); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("QEMU control socket: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	if _, err := r.ReadBytes('\n'); err != nil { // greeting
		return err
	}
	send := func(command string, arguments any) error {
		m := map[string]any{"execute": command}
		if arguments != nil {
			m["arguments"] = arguments
		}
		if err := json.NewEncoder(c).Encode(m); err != nil {
			return err
		}
		for { // skip asynchronous events until our answer
			line, err := r.ReadBytes('\n')
			if err != nil {
				return err
			}
			var resp struct {
				Return json.RawMessage        `json:"return"`
				Error  *struct{ Desc string } `json:"error"`
			}
			json.Unmarshal(line, &resp)
			if resp.Error != nil {
				return fmt.Errorf("QEMU: %s", resp.Error.Desc)
			}
			if resp.Return != nil {
				return nil
			}
		}
	}
	if err := send("qmp_capabilities", nil); err != nil {
		return err
	}
	return send(cmd, args)
}

// ---- images ----

var imageLocks sync.Map

// vmImage downloads the base image once per machine, keeping it only if
// its SHA-256 matches.
func (a *Agent) vmImage(ctx context.Context, v *protocol.VMSpec) (string, error) {
	dir := filepath.Join(a.dataDir, "images")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, v.SHA256+".img")
	mu, _ := imageLocks.LoadOrStore(path, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	a.log.Info("downloading VM image", "url", v.Image)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.Image, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading the VM image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading the VM image: %s", resp.Status)
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("downloading the VM image: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != v.SHA256 {
		os.Remove(tmp)
		return "", fmt.Errorf("the VM image's checksum doesn't match (got %s): refusing to use it", got[:16])
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func imageFormat(path string) (string, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return "", fmt.Errorf("reading the VM image: %w", err)
	}
	var info struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(out, &info); err != nil || info.Format == "" {
		return "", errors.New("unrecognized VM image")
	}
	return info.Format, nil
}

// ---- small helpers ----

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// freeVNC finds a free VNC display (port 5900+N) and WebSocket port.
func freeVNC() (display, ws int, err error) {
	for d := 1; d < 100; d++ {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(5900+d))
		if err != nil {
			continue
		}
		ln.Close()
		ws, err := freeLocalPort()
		if err != nil {
			return 0, 0, err
		}
		return d, ws, nil
	}
	return 0, 0, errors.New("no free VNC display")
}

// cloudInitServer serves cloud-init's NoCloud files to the guest, which
// reaches the host's loopback as 10.0.2.2 through QEMU's user network.
func cloudInitServer(id, userData string) (string, *http.Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/meta-data", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "instance-id: sys1-%s\nlocal-hostname: sys1-%s\n", id, id)
	})
	mux.HandleFunc("/user-data", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, userData) })
	mux.HandleFunc("/vendor-data", func(w http.ResponseWriter, r *http.Request) {})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return fmt.Sprintf("http://10.0.2.2:%d/", ln.Addr().(*net.TCPAddr).Port), srv, nil
}
