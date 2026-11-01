//go:build darwin && !ios

package engines

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func installed() []Engine {
	var out []Engine
	app := func(name, bundle, rt string) {
		for _, dir := range appDirs() {
			if p := filepath.Join(dir, bundle); exists(p) {
				out = append(out, Engine{Name: name, Runtime: rt, start: openApp(p)})
				return
			}
		}
	}
	app("Docker Desktop", "Docker.app", "docker")
	app("OrbStack", "OrbStack.app", "docker")
	app("Rancher Desktop", "Rancher Desktop.app", "docker")
	if onPath("colima") {
		out = append(out, Engine{Name: "Colima", Runtime: "docker", start: command("colima", "start")})
	}
	if podmanMachine() {
		out = append(out, Engine{Name: "Podman", Runtime: "podman", start: command("podman", "machine", "start")})
	}
	return out
}

func appDirs() []string {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// openApp opens a Mac app in the background. A root service opens it in
// the session of whoever is signed in at the screen, since desktop apps
// can only run there.
func openApp(path string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if os.Geteuid() != 0 {
			return command("open", "-g", "-a", path)(ctx)
		}
		uid, ok := consoleUser()
		if !ok {
			return errNoOneSignedIn
		}
		return command("launchctl", "asuser", strconv.Itoa(uid), "open", "-g", "-a", path)(ctx)
	}
}

type engineError string

func (e engineError) Error() string { return string(e) }

const errNoOneSignedIn = engineError("nobody is signed in to this Mac, and Docker Desktop only runs in someone's session")

// consoleUser is the person signed in at the screen.
func consoleUser() (int, bool) {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return 0, false
	}
	return int(st.Uid), true
}
