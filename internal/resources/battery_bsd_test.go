//go:build freebsd || dragonfly || openbsd || netbsd

package resources

import "testing"

func TestACOffline(t *testing.T) {
	for _, c := range []struct {
		out  string
		ok   bool
		kind string
		want bool
	}{
		{"0", true, "freebsd", true}, {"1", true, "freebsd", false}, {"", false, "freebsd", false},
		{"0", true, "openbsd", true}, {"1", true, "openbsd", false}, {"255", true, "openbsd", false},
		{"[acpiacad0]\n  connected:  FALSE\n", true, "netbsd", true},
		{"[acpiacad0]\n  connected:   TRUE\n", true, "netbsd", false},
	} {
		if got := acOffline(c.out, c.ok, c.kind); got != c.want {
			t.Errorf("acOffline(%q, %v, %s) = %v", c.out, c.ok, c.kind, got)
		}
	}
}
