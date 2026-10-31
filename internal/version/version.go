// Package version holds build metadata, stamped in by GoReleaser.
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string { return Version + " (" + Commit + ", " + Date + ")" }
