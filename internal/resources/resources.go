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
}

func (s *Sampler) Sample(allowExec bool) protocol.Resources {
	r := protocol.Resources{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU()}
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
		s.gpus, s.runtimes = probeGPUs(), probeRuntimes()
		s.probed = time.Now()
	}
	r.GPUInfo = append([]protocol.GPU(nil), s.gpus...)
	for _, g := range s.gpus {
		r.GPUs = append(r.GPUs, g.Name)
	}
	r.Runtimes = append([]string(nil), s.runtimes...)
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
func probeRuntimes() []string {
	var rts []string
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err != nil {
			continue
		}
		if _, ok := run(rt, "info", "--format", "{{.ID}}"); ok {
			rts = append(rts, rt)
		}
	}
	return rts
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
