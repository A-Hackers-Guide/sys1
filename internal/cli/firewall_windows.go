package cli

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// allowInbound lets other machines on private and work networks reach
// this program through Windows Firewall. Windows asks about a program
// that starts listening only when someone is signed in to answer; for a
// service it silently blocks, and an allow rule is tied to one path, so a
// program that moved (to Program Files, in 0.9.1) was cut off.
func allowInbound(name string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	rule := name + " (" + exe + ")"
	if exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name="+rule).Run() == nil {
		return // already there
	}
	out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name="+rule,
		"dir=in", "action=allow", "program="+exe, "profile=private,domain", "enable=yes").CombinedOutput()
	if err != nil {
		slog.Warn("couldn't allow other machines through Windows Firewall", "err", strings.TrimSpace(string(out)))
	}
}
