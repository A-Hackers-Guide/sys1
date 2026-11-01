//go:build !linux && !darwin && !windows && !freebsd && !dragonfly && !openbsd && !netbsd

package resources

// onBattery has no portable implementation on Solaris, illumos and AIX;
// they are almost always plugged-in servers.
func onBattery() bool { return false }
