//go:build (!darwin && !linux && !windows) || ios || android

package engines

// Other systems have no engine sys0 knows how to start.
func installed() []Engine { return nil }
