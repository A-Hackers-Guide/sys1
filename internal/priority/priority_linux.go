package priority

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// Lowest sets nice 19 and the idle I/O class on every thread of the process.
func Lowest() error {
	tids, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return unix.Setpriority(unix.PRIO_PROCESS, 0, 19)
	}
	var errs []error
	for _, t := range tids {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		if err := unix.Setpriority(unix.PRIO_PROCESS, tid, 19); err != nil {
			errs = append(errs, err)
		}
		unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), ioprioClassIdle<<ioprioClassShift)
	}
	return errors.Join(errs...)
}
