package resources

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/shirou/gopsutil/v4/mem"
)

// probeGPUs finds GPUs and their memory with whatever this OS offers:
// nvidia-smi for NVIDIA anywhere, the kernel's DRM files for AMD and Intel
// on Linux, the video controller list on Windows, and unified memory on
// Apple Silicon.
func probeGPUs() []protocol.GPU {
	var gpus []protocol.GPU
	nvidia := false
	if out, ok := run("nvidia-smi", "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits"); ok {
		gpus = append(gpus, parseNvidiaSMI(out)...)
		nvidia = len(gpus) > 0
	}
	switch runtime.GOOS {
	case "linux":
		gpus = append(gpus, probeDRM("/sys/class/drm", nvidia)...)
	case "windows":
		if out, ok := run("powershell", "-NoProfile", "-Command",
			"Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name + '|' + $_.AdapterRAM }"); ok {
			gpus = append(gpus, parseWindowsVideo(out, nvidia)...)
		}
	case "darwin":
		if runtime.GOARCH == "arm64" {
			// Apple GPUs share memory with the CPU; macOS lets the GPU use
			// roughly two thirds of it.
			if m, err := mem.VirtualMemory(); err == nil {
				gpus = append(gpus, protocol.GPU{Vendor: "apple", Name: "Apple Silicon GPU", VRAMMB: int(m.Total>>20) * 2 / 3})
			}
		}
	}
	return gpus
}

func parseNvidiaSMI(out string) []protocol.GPU {
	var gpus []protocol.GPU
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		total, _ := strconv.Atoi(strings.TrimSpace(f[1]))
		free, _ := strconv.Atoi(strings.TrimSpace(f[2]))
		name := strings.TrimSpace(f[0])
		if !strings.HasPrefix(name, "NVIDIA") {
			name = "NVIDIA " + name
		}
		gpus = append(gpus, protocol.GPU{Vendor: "nvidia", Name: name, VRAMMB: total, FreeMB: free})
	}
	return gpus
}

// probeDRM reads /sys/class/drm/card*/device. AMD reports its memory
// there; Intel GPUs on Linux are mostly integrated and share system RAM.
func probeDRM(root string, haveNvidia bool) []protocol.GPU {
	var gpus []protocol.GPU
	cards, _ := filepath.Glob(filepath.Join(root, "card[0-9]*"))
	for _, c := range cards {
		if strings.Contains(filepath.Base(c), "-") {
			continue // connectors like card0-HDMI-A-1
		}
		dev := filepath.Join(c, "device")
		read := func(name string) string {
			b, _ := os.ReadFile(filepath.Join(dev, name))
			return strings.TrimSpace(string(b))
		}
		mb := func(name string) int {
			n, _ := strconv.ParseInt(read(name), 10, 64)
			return int(n >> 20)
		}
		name := read("product_name")
		switch read("vendor") {
		case "0x1002":
			if name == "" {
				name = "AMD GPU"
			}
			total := mb("mem_info_vram_total")
			free := total - mb("mem_info_vram_used")
			if free <= 0 || free > total {
				free = 0
			}
			gpus = append(gpus, protocol.GPU{Vendor: "amd", Name: name, VRAMMB: total, FreeMB: free})
		case "0x8086":
			if name == "" {
				name = "Intel GPU"
			}
			gpus = append(gpus, protocol.GPU{Vendor: "intel", Name: name})
		case "0x10de":
			if !haveNvidia { // driver tools missing: still worth knowing
				gpus = append(gpus, protocol.GPU{Vendor: "nvidia", Name: "NVIDIA GPU"})
			}
		}
	}
	return gpus
}

func parseWindowsVideo(out string, haveNvidia bool) []protocol.GPU {
	var gpus []protocol.GPU
	for _, line := range strings.Split(out, "\n") {
		name, ram, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || name == "" {
			continue
		}
		lower := strings.ToLower(name)
		var vendor string
		switch {
		case strings.Contains(lower, "nvidia"):
			if haveNvidia {
				continue // nvidia-smi already gave exact numbers
			}
			vendor = "nvidia"
		case strings.Contains(lower, "amd") || strings.Contains(lower, "radeon"):
			vendor = "amd"
		case strings.Contains(lower, "intel"):
			vendor = "intel"
		case strings.Contains(lower, "microsoft basic") || strings.Contains(lower, "remote"):
			continue
		default:
			vendor = "other"
		}
		// AdapterRAM is a 32-bit field, so it tops out at 4 GB.
		n, _ := strconv.ParseInt(ram, 10, 64)
		gpus = append(gpus, protocol.GPU{Vendor: vendor, Name: name, VRAMMB: int(n >> 20)})
	}
	return gpus
}

// HasNvidia reports whether r has an NVIDIA GPU.
func HasNvidia(r protocol.Resources) bool {
	for _, g := range r.GPUInfo {
		if g.Vendor == "nvidia" {
			return true
		}
	}
	return false
}
