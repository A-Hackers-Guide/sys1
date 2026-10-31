//go:build unix && !linux && !darwin

package priority

import "golang.org/x/sys/unix"

// Lowest sets nice 19 for the whole process (FreeBSD and other BSDs).
func Lowest() error { return unix.Setpriority(unix.PRIO_PROCESS, 0, 19) }
