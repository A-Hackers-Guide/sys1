// Package resources measures a machine's spare capacity.
package resources

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// Sampler caches the slow probes (GPUs, container runtimes) and re-runs
// them once a minute.
type Sampler struct {
	DataDir string

	mu       sync.Mutex
	probed   time.Time
	gpus     []protocol.GPU
	runtimes []string
	problems []string
}

func (s *Sampler) Sample(allowExec bool) protocol.Resources {
	r := protocol.Resources{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU()}
	if r.OS == "linux" && isAndroid() {
		r.OS = "android" // a static Linux build running on Android
	}
	r.Hostname, _ = os.Hostname()
	if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
		r.CPUIdlePercent = 100 - p[0]
	}
	if m, err := mem.VirtualMemory(); err == nil {
		r.MemTotal, r.MemFree = m.Total, m.Available
	}
	dir := s.DataDir
	if dir == "" {
		dir = "/"
	}
	if d, err := disk.Usage(dir); err == nil {
		r.DiskTotal, r.DiskFree = d.Total, d.Free
	}
	r.OnBattery = onBattery()
	r.Addr = lanAddr()
	s.mu.Lock()
	if time.Since(s.probed) > time.Minute {
		s.gpus = probeGPUs()
		s.runtimes, s.problems = probeRuntimes()
		s.probed = time.Now()
	}
	r.GPUInfo = append([]protocol.GPU(nil), s.gpus...)
	for _, g := range s.gpus {
		r.GPUs = append(r.GPUs, g.Name)
	}
	r.Runtimes = append([]string(nil), s.runtimes...)
	r.Problems = append([]string(nil), s.problems...)
	s.mu.Unlock()
	if allowExec {
		r.Runtimes = append(r.Runtimes, "exec")
	}
	return r
}

func run(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(out)), err == nil
}

// probeRuntimes reports container runtimes whose daemon actually answers.
func probeRuntimes() (rts, problems []string) {
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var stderr strings.Builder
		cmd := exec.CommandContext(ctx, rt, "info", "--format", "{{.ID}}")
		cmd.Stderr = &stderr
		err := cmd.Run()
		cancel()
		if err == nil {
			rts = append(rts, rt)
			continue
		}
		problems = append(problems, runtimeProblem(rt, stderr.String()))
	}
	if len(rts) > 0 {
		problems = nil // one working runtime is all that's needed
	} else if p := dockerAppWithoutCLI(); p != "" {
		problems = append(problems, p)
	}
	// QEMU, for virtual machines: one runtime per guest architecture.
	if _, err := exec.LookPath("qemu-img"); err == nil {
		for _, arch := range []string{"x86_64", "aarch64"} {
			if _, err := exec.LookPath("qemu-system-" + arch); err == nil {
				rts = append(rts, "qemu-"+arch)
			}
		}
	}
	return rts, problems
}

// runtimeProblem turns a failed `docker info` into advice.
func runtimeProblem(rt, stderr string) string {
	name := map[string]string{"docker": "Docker", "podman": "Podman"}[rt]
	e := strings.ToLower(stderr)
	switch {
	case strings.Contains(e, "permission denied") || strings.Contains(e, "access is denied"):
		switch runtime.GOOS {
		case "windows":
			return name + " is running, but sys0 isn't allowed to use it. Run `sys0 install` again from your own account so sys0 runs as you, or add sys0's account to the docker-users group."
		case "linux":
			return name + " is running, but sys0 isn't allowed to use it. Add the user sys0 runs as to the docker group, or run sys0 as root."
		}
		return name + " is running, but sys0 isn't allowed to use it."
	case strings.Contains(e, "cannot connect") || strings.Contains(e, "error during connect") ||
		strings.Contains(e, "no such file") || strings.Contains(e, "cannot find the file") || strings.Contains(e, "is the docker daemon running"):
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			return name + " is installed but not running. Open " + name + " Desktop and turn on \"Start when you sign in\"."
		}
		return name + " is installed but not running. Start its service (for example: sudo systemctl enable --now " + rt + ")."
	}
	first, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	if len(first) > 160 {
		first = first[:160]
	}
	return name + " is installed but doesn't answer: " + first
}

// dockerAppWithoutCLI notices Docker Desktop installed where its command
// can't be found.
func dockerAppWithoutCLI() string {
	if _, err := exec.LookPath("docker"); err == nil {
		return ""
	}
	for _, p := range []string{"/Applications/Docker.app", `C:\Program Files\Docker\Docker\Docker Desktop.exe`} {
		if _, err := os.Stat(p); err == nil {
			return "Docker Desktop is installed, but its docker command isn't. Open Docker Desktop once so it finishes setting up."
		}
	}
	return ""
}

// lanAddr is the address other machines on the network reach this one on.
// The UDP "dial" sends nothing; it only asks which interface would be used.
func lanAddr() string {
	c, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// isAndroid recognizes Android by files every Android system has.
func isAndroid() bool {
	_, err := os.Stat("/system/build.prop")
	return err == nil
}
