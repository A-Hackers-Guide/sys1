package paths

import (
	"context"
	"net"
	"os"
	"time"
)

// FixDNS gives Go's resolver servers to ask on systems without
// /etc/resolv.conf, such as Android, where it would otherwise only try
// 127.0.0.1. Node addresses are usually IPs, so this only matters when
// one is a name.
func FixDNS() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}
	servers := []string{"1.1.1.1:53", "8.8.8.8:53"}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			var err error
			for _, s := range servers {
				var c net.Conn
				if c, err = d.DialContext(ctx, network, s); err == nil {
					return c, nil
				}
			}
			return nil, err
		},
	}
}
