package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

func TestParseNvidiaSMI(t *testing.T) {
	g := parseNvidiaSMI("NVIDIA GeForce RTX 3060, 12288, 11000\nTesla T4, 15360, 15000\n")
	if len(g) != 2 || g[0].VRAMMB != 12288 || g[0].FreeMB != 11000 || g[1].Name != "NVIDIA Tesla T4" {
		t.Fatalf("%+v", g)
	}
}

func TestProbeDRM(t *testing.T) {
	root := t.TempDir()
	card := func(n, vendor string, files map[string]string) {
		dev := filepath.Join(root, n, "device")
		os.MkdirAll(dev, 0o755)
		os.WriteFile(filepath.Join(dev, "vendor"), []byte(vendor+"\n"), 0o644)
		for k, v := range files {
			os.WriteFile(filepath.Join(dev, k), []byte(v), 0o644)
		}
	}
	card("card0", "0x1002", map[string]string{"mem_info_vram_total": "17163091968", "mem_info_vram_used": "1073741824", "product_name": "Radeon RX 7800 XT"})
	card("card1", "0x8086", nil)
	card("card0-HDMI-A-1", "0x1002", nil)
	g := probeDRM(root, false)
	if len(g) != 2 || g[0].Vendor != "amd" || g[0].VRAMMB != 16368 || g[0].FreeMB != 15344 || g[1].Vendor != "intel" {
		t.Fatalf("%+v", g)
	}
}

func TestParseWindowsVideo(t *testing.T) {
	g := parseWindowsVideo("NVIDIA GeForce RTX 4070|4293918720\nAMD Radeon(TM) Graphics|536870912\nMicrosoft Basic Display Adapter|0\n", true)
	if len(g) != 1 || g[0].Vendor != "amd" || g[0].VRAMMB != 512 {
		t.Fatalf("%+v", g)
	}
}

func TestUsableGPUs(t *testing.T) {
	mac := protocol.Resources{OS: "darwin", GPUInfo: []protocol.GPU{{Vendor: "apple", VRAMMB: 16000}}}
	if _, ok := mac.BestGPU(protocol.KindContainer); ok {
		t.Fatal("Apple GPU offered to a container (Docker on macOS can't pass it through)")
	}
	if g, ok := mac.BestGPU(protocol.KindExec); !ok || g.VRAMMB != 16000 {
		t.Fatal("Apple GPU not offered to a native program")
	}
	linux := protocol.Resources{OS: "linux", GPUInfo: []protocol.GPU{{Vendor: "amd", VRAMMB: 8000, FreeMB: 2000}, {Vendor: "nvidia", VRAMMB: 12000, FreeMB: 9000}}}
	if g, _ := linux.BestGPU(protocol.KindContainer); g.Vendor != "nvidia" {
		t.Fatalf("best GPU = %+v", g)
	}
}
