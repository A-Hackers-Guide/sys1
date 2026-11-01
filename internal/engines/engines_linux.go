//go:build linux && !android

package engines

import (
	"os"
	"os/exec"
	"strings"
)

func installed() []Engine {
	var out []Engine
	root := os.Geteuid() == 0
	switch {
	case root && unitExists("docker.service", false):
		out = append(out, Engine{Name: "Docker", Runtime: "docker", start: command("systemctl", "start", "docker")})
	case root && onPath("snap") && snapHas("docker"):
		out = append(out, Engine{Name: "Docker (snap)", Runtime: "docker", start: command("snap", "start", "docker")})
	case !root && unitExists("docker.service", true):
		out = append(out, Engine{Name: "Docker (rootless)", Runtime: "docker", start: command("systemctl", "--user", "start", "docker")})
	}
	// Podman needs no engine running on Linux.
	return out
}

func unitExists(unit string, user bool) bool {
	if !onPath("systemctl") {
		return false
	}
	args := []string{"list-unit-files", unit, "--no-legend"}
	if user {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command("systemctl", args...).Output()
	return err == nil && strings.Contains(string(out), unit)
}

func snapHas(name string) bool {
	out, err := exec.Command("snap", "list", name).Output()
	return err == nil && strings.Contains(string(out), name)
}
