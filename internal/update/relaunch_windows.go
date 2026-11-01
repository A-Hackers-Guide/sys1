package update

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/windows/svc"
)

// relaunch runs path with the same arguments and exits with its exit code.
// Windows can't replace a process, and a Windows service runs as SYSTEM and
// updates in place, so this is only for apps started by a person.
func relaunch(path string) {
	if isSvc, err := svc.IsWindowsService(); err != nil || isSvc {
		return
	}
	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exit):
		os.Exit(exit.ExitCode())
	}
	// It couldn't start at all: carry on with this binary.
}
