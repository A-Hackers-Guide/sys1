package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/cli"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Standbys and witnesses chosen by the pool: when the node asks, one of
// the owner's machines joins the node's cluster with the sys1 program
// installed beside sys0, and installs it as a service (see docs/CLUSTER.md).

// sys1Beside is the sys1 program installed next to this sys0, if any.
func sys1Beside() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, _ = filepath.EvalSymlinks(exe)
	name := "sys1"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}

// canHost reports whether sys0 may install a system service here: it runs
// as root, or as a Windows service (SYSTEM). A per-user sys0 can't.
func canHost() bool {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return false
	}
	if runtime.GOOS == "windows" {
		return !cli.Interactive()
	}
	return os.Geteuid() == 0
}

// defaultHost is runSys1 when sys1 is installed beside sys0.
func defaultHost() func(ctx context.Context, args ...string) error {
	if sys1Beside() == "" || !canHost() {
		return nil
	}
	return runSys1
}

// runSys1 is the default Host: it runs the sys1 beside sys0.
func runSys1(ctx context.Context, args ...string) error {
	p := sys1Beside()
	if p == "" {
		return fmt.Errorf("sys1 isn't installed beside sys0")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("sys1 %s: %s", args[0]+" "+args[1], msg)
	}
	return nil
}

// host carries out a HostOrder.
func (a *Agent) host(ctx context.Context, o protocol.HostOrder) {
	if o.Role != protocol.RoleStandby && o.Role != protocol.RoleWitness {
		return
	}
	a.log.Info("the pool asked this machine to be its "+o.Role, "role", o.Role)
	join := []string{"cluster", "join"}
	if o.Role == protocol.RoleWitness {
		join = append(join, "--witness")
	}
	err := a.Host(ctx, append(join, o.Code)...)
	if err == nil {
		err = a.Host(ctx, "install", "--cluster")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.hostProblem = "Couldn't become the pool's " + o.Role + ": " + err.Error()
		a.log.Warn("couldn't become the pool's "+o.Role, "err", err)
		return
	}
	a.hostProblem = ""
	a.cfg.Hosting = o.Role
	if err := SaveConfig(a.dataDir, a.cfg); err != nil {
		a.log.Error("cannot save config", "err", err)
	}
	a.log.Info("this machine is now the pool's "+o.Role, "role", o.Role)
}
