package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExtendPath adds the folders container runtimes and QEMU install into to
// this process's PATH. Services don't get the PATH a terminal has: launchd
// gives /usr/bin:/bin:/usr/sbin:/sbin (no /usr/local/bin, no Homebrew), and
// a Windows service keeps the PATH from when it started, so Docker
// installed afterwards would stay invisible until a reboot.
func ExtendPath() {
	var extra []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		extra = []string{"/usr/local/bin", "/opt/homebrew/bin", "/opt/local/bin", "/opt/podman/bin",
			"/Applications/Docker.app/Contents/Resources/bin"}
		if home != "" {
			extra = append(extra, filepath.Join(home, ".docker/bin"), filepath.Join(home, ".orbstack/bin"), filepath.Join(home, ".rd/bin"))
		}
		// A root service also looks in the signed-in users' tool folders.
		if os.Geteuid() == 0 {
			for _, u := range consoleHomes() {
				extra = append(extra, filepath.Join(u, ".docker/bin"), filepath.Join(u, ".orbstack/bin"), filepath.Join(u, ".rd/bin"))
			}
		}
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		extra = []string{filepath.Join(pf, `Docker\Docker\resources\bin`), filepath.Join(pf, `RedHat\Podman`),
			filepath.Join(pf, `qemu`), filepath.Join(pf, `Rancher Desktop\resources\resources\win32\bin`)}
	default:
		extra = []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/snap/bin"}
		if home != "" {
			extra = append(extra, filepath.Join(home, "bin"), filepath.Join(home, ".local/bin"))
		}
	}
	have := map[string]bool{}
	parts := filepath.SplitList(os.Getenv("PATH"))
	for _, p := range parts {
		have[strings.ToLower(filepath.Clean(p))] = true
	}
	for _, p := range extra {
		if have[strings.ToLower(filepath.Clean(p))] {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			parts = append(parts, p)
			have[strings.ToLower(filepath.Clean(p))] = true
		}
	}
	os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
	findDockerSocket(home)
}

// findDockerSocket points Docker at a running engine's socket when the
// default one doesn't exist, as with Docker Desktop on a Mac without its
// "default socket" option, OrbStack or Colima, and for a root service whose
// own home has no Docker settings.
func findDockerSocket(home string) {
	if runtime.GOOS == "windows" || os.Getenv("DOCKER_HOST") != "" {
		return
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return
	}
	homes := []string{home}
	if os.Geteuid() == 0 {
		homes = append(homes, consoleHomes()...)
	}
	for _, h := range homes {
		if h == "" {
			continue
		}
		for _, s := range []string{".docker/run/docker.sock", ".orbstack/run/docker.sock", ".colima/default/docker.sock", ".rd/docker.sock"} {
			p := filepath.Join(h, s)
			if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
				os.Setenv("DOCKER_HOST", "unix://"+p)
				return
			}
		}
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" { // rootless Docker
		if _, err := os.Stat(filepath.Join(d, "docker.sock")); err == nil {
			os.Setenv("DOCKER_HOST", "unix://"+filepath.Join(d, "docker.sock"))
		}
	}
}

// consoleHomes lists the home folders of people who use this Mac.
func consoleHomes() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	entries, _ := os.ReadDir("/Users")
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "Shared" && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join("/Users", e.Name()))
		}
	}
	return out
}
