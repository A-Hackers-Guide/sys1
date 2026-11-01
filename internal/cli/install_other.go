//go:build !windows

package cli

// systemCopy is Windows-only; elsewhere the service runs the binary where
// the package manager or installer put it.
func systemCopy() (string, error) { return "", nil }
