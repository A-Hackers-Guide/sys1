//go:build !windows

package cli

// allowInbound is Windows-only.
func allowInbound(string) {}
