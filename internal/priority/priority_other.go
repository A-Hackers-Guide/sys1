//go:build !unix && !windows

package priority

// Lowest is a no-op on platforms without a priority API (wasm, plan9).
func Lowest() error { return nil }
