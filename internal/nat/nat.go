// Package nat asks the home router to forward a port to this machine, so
// machines elsewhere on the internet can reach the node without anyone
// configuring port forwarding by hand. It speaks NAT-PMP (Apple and many
// other routers) and UPnP IGD (most others), renews the mapping before it
// expires, and removes it when done.
package nat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"
)

const lease = time.Hour

// Mapping is a live port forward on the router.
type Mapping struct {
	Method   string // "NAT-PMP" or "UPnP"
	External string // public ip:port
	// CGNAT is set when the router's own "public" address is private or
	// shared: the provider puts several customers behind one address, and
	// no router setting can make this machine reachable from outside.
	CGNAT bool

	mu     sync.Mutex
	remove func()
	stop   chan struct{}
}

// Close stops renewing and removes the mapping from the router.
func (m *Mapping) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
		m.remove()
	}
}

// mapper is one protocol's way to add, remove and look up a mapping.
type mapper struct {
	method   string
	add      func(ctx context.Context) (externalPort int, err error)
	remove   func()
	external func(ctx context.Context) (string, error)
}

// Map forwards TCP port on the router to this machine and keeps it
// forwarded until Close.
func Map(ctx context.Context, port int, description string) (*Mapping, error) {
	var errs []error
	for _, try := range []func(context.Context, int, string) (*mapper, error){natPMP, upnp} {
		mp, err := try(ctx, port, description)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ext, err := mp.add(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mp.method, err))
			continue
		}
		ip, err := mp.external(ctx)
		if err != nil {
			mp.remove()
			errs = append(errs, fmt.Errorf("%s: %w", mp.method, err))
			continue
		}
		m := &Mapping{Method: mp.method, External: net.JoinHostPort(ip, strconv.Itoa(ext)), CGNAT: shared(ip), remove: mp.remove, stop: make(chan struct{})}
		go m.renew(mp)
		return m, nil
	}
	return nil, fmt.Errorf("the router didn't accept a port mapping (is UPnP or NAT-PMP turned on?): %w", errors.Join(errs...))
}

func (m *Mapping) renew(mp *mapper) {
	t := time.NewTicker(lease / 2)
	defer t.Stop()
	for {
		m.mu.Lock()
		stop := m.stop
		m.mu.Unlock()
		if stop == nil {
			return
		}
		select {
		case <-stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			mp.add(ctx)
			cancel()
		}
	}
}

// shared reports whether ip can't be reached from the internet: private
// ranges, and 100.64.0.0/10, which providers use for carrier-grade NAT.
func shared(ip string) bool {
	a := net.ParseIP(ip)
	if a == nil {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

func natPMP(ctx context.Context, port int, _ string) (*mapper, error) {
	gw, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("NAT-PMP: no gateway: %w", err)
	}
	c := natpmp.NewClientWithTimeout(gw, 2*time.Second)
	if _, err := c.GetExternalAddress(); err != nil {
		return nil, fmt.Errorf("NAT-PMP: %w", err)
	}
	var mu sync.Mutex
	external := port
	return &mapper{
		method: "NAT-PMP",
		add: func(context.Context) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			r, err := c.AddPortMapping("tcp", port, external, int(lease/time.Second))
			if err != nil {
				return 0, err
			}
			external = int(r.MappedExternalPort)
			return external, nil
		},
		remove: func() { c.AddPortMapping("tcp", port, 0, 0) }, // lifetime 0 deletes
		external: func(context.Context) (string, error) {
			r, err := c.GetExternalAddress()
			if err != nil {
				return "", err
			}
			return net.IP(r.ExternalIPAddress[:]).String(), nil
		},
	}, nil
}

// igd is what the three UPnP service versions have in common.
type igd interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string, internalPort uint16, internalClient string, enabled bool, description string, leaseSeconds uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
}

func upnp(ctx context.Context, port int, description string) (*mapper, error) {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var found []igd
	if cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(dctx); err == nil {
		for _, c := range cs {
			found = append(found, c)
		}
	}
	if cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(dctx); err == nil {
		for _, c := range cs {
			found = append(found, c)
		}
	}
	if cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(dctx); err == nil {
		for _, c := range cs {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("UPnP: no internet gateway device found")
	}
	c := found[0]
	local, err := localIP()
	if err != nil {
		return nil, err
	}
	return &mapper{
		method: "UPnP",
		add: func(ctx context.Context) (int, error) {
			return port, c.AddPortMappingCtx(ctx, "", uint16(port), "TCP", uint16(port), local, true, description, uint32(lease/time.Second))
		},
		remove: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.DeletePortMappingCtx(ctx, "", uint16(port), "TCP")
		},
		external: c.GetExternalIPAddressCtx,
	}, nil
}

// localIP is this machine's address on the network the router serves.
func localIP() (string, error) {
	c, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// Probe finds the router's protocol and public address without changing
// anything on it.
func Probe(ctx context.Context) (method, external string, cgnat bool, err error) {
	var errs []error
	for _, try := range []func(context.Context, int, string) (*mapper, error){natPMP, upnp} {
		mp, err := try(ctx, 0, "")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ip, err := mp.external(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return mp.method, ip, shared(ip), nil
	}
	return "", "", false, errors.Join(errs...)
}
