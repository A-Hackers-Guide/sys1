package priority

import "golang.org/x/sys/windows"

const processModeBackgroundBegin = 0x00100000

// Lowest sets the idle priority class and background mode, which also
// lowers disk and memory priority.
func Lowest() error {
	h := windows.CurrentProcess()
	if err := windows.SetPriorityClass(h, windows.IDLE_PRIORITY_CLASS); err != nil {
		return err
	}
	windows.SetPriorityClass(h, processModeBackgroundBegin)
	return nil
}
