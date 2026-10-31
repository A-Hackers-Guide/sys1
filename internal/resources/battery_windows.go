package resources

import (
	"syscall"
	"unsafe"
)

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var getSystemPowerStatus = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

func onBattery() bool {
	var s systemPowerStatus
	if r, _, _ := getSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return false
	}
	const noBattery = 128
	return s.ACLineStatus == 0 && s.BatteryFlag&noBattery == 0
}
