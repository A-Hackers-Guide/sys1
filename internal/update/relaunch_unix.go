//go:build !windows

package update

import (
	"os"
	"syscall"
)

// relaunch replaces this process with path, same arguments, same PID, so
// the service manager doesn't notice.
func relaunch(path string) {
	syscall.Exec(path, append([]string{path}, os.Args[1:]...), os.Environ())
}
