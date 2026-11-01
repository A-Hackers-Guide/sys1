//go:build darwin && !ios

package resources

import "strings"

func onBattery() bool {
	out, ok := run("pmset", "-g", "ps")
	return ok && strings.Contains(out, "'Battery Power'")
}
