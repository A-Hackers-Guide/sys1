//go:build !windows

package cli

// ensureRecovery is Windows-only; launchd and systemd restart from the
// service definition itself.
func ensureRecovery(string) {}
