//go:build freebsd || dragonfly || openbsd || netbsd

package resources

import (
	"runtime"
	"strings"
)

// onBattery asks the system's own tool whether the AC adapter is
// connected. Machines without one (most servers) are never on battery.
func onBattery() bool {
	switch runtime.GOOS {
	case "freebsd", "dragonfly":
		out, ok := run("sysctl", "-n", "hw.acpi.acline")
		return acOffline(out, ok, "freebsd")
	case "openbsd":
		out, ok := run("apm", "-a")
		return acOffline(out, ok, "openbsd")
	default:
		out, ok := run("envstat", "-d", "acpiacad0")
		return acOffline(out, ok, "netbsd")
	}
}

func acOffline(out string, ok bool, kind string) bool {
	if !ok {
		return false
	}
	switch kind {
	case "freebsd", "openbsd":
		return strings.TrimSpace(out) == "0" // 1 = on AC; OpenBSD's 255 = unknown
	default:
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "connected:") {
				return strings.Contains(line, "FALSE")
			}
		}
		return false
	}
}
