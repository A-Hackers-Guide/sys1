package resources

import (
	"os"
	"path/filepath"
	"strings"
)

// onBattery reports whether the machine has a battery and no charger
// connected. Also used on Android, which shares the Linux sysfs layout.
func onBattery() bool {
	supplies, _ := filepath.Glob("/sys/class/power_supply/*")
	hasBattery, charging := false, false
	read := func(dir, name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	for _, s := range supplies {
		switch read(s, "type") {
		case "Battery":
			hasBattery = true
			if st := read(s, "status"); st == "Charging" || st == "Full" {
				charging = true
			}
		case "Mains", "USB", "USB_C", "USB_PD", "Wireless":
			if read(s, "online") == "1" {
				charging = true
			}
		}
	}
	return hasBattery && !charging
}
