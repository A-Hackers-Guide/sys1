// Package paths picks where sys0 and sys1 keep their data on each OS.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir returns the default data directory for app ("sys0" or "sys1").
// Services run as root/SYSTEM and use the system-wide location; a normal
// user gets a per-user one.
func DataDir(app string) string {
	if d := os.Getenv("SYS1_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	root := os.Geteuid() == 0
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, app)
	case "darwin":
		if root {
			return filepath.Join("/Library/Application Support", app)
		}
		return filepath.Join(home, "Library", "Application Support", app)
	case "android":
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, app)
		}
		return filepath.Join(os.TempDir(), app)
	default:
		if root {
			return filepath.Join("/var/lib", app)
		}
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, app)
		}
		return filepath.Join(home, ".local", "share", app)
	}
}
