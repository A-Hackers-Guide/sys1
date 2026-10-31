//go:build !linux && !darwin && !windows

package resources

// onBattery has no portable implementation on the BSDs and UNIX yet;
// they are almost always plugged-in servers.
func onBattery() bool { return false }
