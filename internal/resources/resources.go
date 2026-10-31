// Package resources measures a machine's spare capacity.
package resources

import (
	"context"
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
	gpus     []string
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
	s.mu.Lock()
	if time.Since(s.probed) > time.Minute {
		s.gpus, s.runtimes = probeGPUs(), probeRuntimes()
		s.probed = time.Now()
	}
	r.GPUs = s.gpus
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

func probeGPUs() []string {
	var gpus []string
	if out, ok := run("nvidia-smi", "--query-gpu=name", "--format=csv,noheader"); ok {
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				gpus = append(gpus, "NVIDIA "+strings.TrimPrefix(l, "NVIDIA "))
			}
		}
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		gpus = append(gpus, "Apple Silicon GPU")
	}
	return gpus
}

// NvidiaGPU reports whether r includes an NVIDIA GPU (usable by containers).
func NvidiaGPU(r protocol.Resources) bool {
	for _, g := range r.GPUs {
		if strings.HasPrefix(g, "NVIDIA") {
			return true
		}
	}
	return false
}
