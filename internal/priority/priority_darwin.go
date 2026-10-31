package priority

import "golang.org/x/sys/unix"

const (
	prioDarwinProcess = 4
	prioDarwinBG      = 0x1000
)

// Lowest puts the process in the background band (throttled CPU, disk and
// network, the same treatment macOS gives Time Machine) and sets nice 19.
func Lowest() error {
	if err := unix.Setpriority(prioDarwinProcess, 0, prioDarwinBG); err != nil {
		return err
	}
	return unix.Setpriority(unix.PRIO_PROCESS, 0, 19)
}
