// Package egress is the gateway between an app running on a machine lent
// by someone else and the outside world. The app's container sits on a
// network with no route anywhere; this gateway is the only way in or out:
//
//   - outbound: an HTTP proxy that lets the app reach only the hostnames
//     its package declares, over HTTP and HTTPS, and never any private,
//     loopback or link-local address (the lender's home network)
//   - inbound: plain TCP forwards for the ports the package declares
//
// See docs/SECURITY-MODEL.md.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Policy decides which destinations are allowed.
type Policy struct {
	Hosts []string // "example.com", "*.example.com"
	// Resolve is net.DefaultResolver.LookupIPAddr; tests replace it.
	Resolve func(ctx context.Context, host string) ([]net.IPAddr, error)

	allowAddr func(net.IP) bool // tests: let loopback through
	port      map[string]string // tests: where ports 80/443 really are
}

var ErrDenied = errors.New("egress: destination not allowed")

// ValidHost reports whether h is an acceptable entry for a package's
// egress list.
func ValidHost(h string) bool {
	h = strings.TrimPrefix(h, "*.")
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return net.ParseIP(h) == nil
}

func (p Policy) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range p.Hosts {
		h = strings.ToLower(h)
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == h {
			return true
		}
	}
	return false
}

// publicIP reports whether ip is on the public internet.
func publicIP(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) || ip.IsInterfaceLocalMulticast())
}

// Dial connects to host:port if the policy allows it. The address is
// resolved here and checked before connecting, so a name that resolves to
// a private address (DNS rebinding) is refused too.
func (p Policy) Dial(ctx context.Context, host, port string) (net.Conn, error) {
	if port != "80" && port != "443" {
		return nil, fmt.Errorf("%w: only ports 80 and 443 (asked for %s)", ErrDenied, port)
	}
	if net.ParseIP(host) != nil || !p.hostAllowed(host) {
		return nil, fmt.Errorf("%w: %s is not in this app's list", ErrDenied, host)
	}
	resolve := p.Resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupIPAddr
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	allowed := publicIP
	if p.allowAddr != nil {
		allowed = p.allowAddr
	}
	if real, ok := p.port[port]; ok {
		port = real
	}
	var d net.Dialer
	for _, ip := range ips {
		if !allowed(ip.IP) {
			continue
		}
		if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.IP.String(), port)); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: %s has no reachable public address", ErrDenied, host)
}

// Proxy is an HTTP proxy that applies a Policy: CONNECT for HTTPS, and
// absolute-URL requests for plain HTTP.
type Proxy struct {
	Policy Policy
	Log    *slog.Logger
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" || r.URL.Scheme != "http" {
		http.Error(w, "this is an egress proxy", http.StatusBadRequest)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return p.Policy.Dial(ctx, host, port) },
		Proxy:       nil,
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := tr.RoundTrip(out)
	if err != nil {
		p.deny(w, host, err)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := p.Policy.Dial(ctx, host, port)
	cancel()
	if err != nil {
		p.deny(w, host, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "can't tunnel", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Reader.Peek(n)
		up.Write(b)
	}
	splice(down, up)
}

func (p *Proxy) deny(w http.ResponseWriter, host string, err error) {
	if p.Log != nil {
		p.Log.Warn("blocked", "host", host, "reason", err)
	}
	status := http.StatusBadGateway
	if errors.Is(err, ErrDenied) {
		status = http.StatusForbidden
	}
	http.Error(w, err.Error(), status)
}

func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// Forward accepts TCP connections on listen and passes each to target:
// how the outside world reaches the app's declared ports.
func Forward(ctx context.Context, ln net.Listener, target string) error {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			var d net.Dialer
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			up, err := d.DialContext(dctx, "tcp", target)
			cancel()
			if err != nil {
				c.Close()
				return
			}
			splice(c, up)
		}()
	}
}

// ParseForward reads "port:host:port".
func ParseForward(s string) (listen, target string, err error) {
	p, rest, ok := strings.Cut(s, ":")
	if _, err := strconv.Atoi(p); !ok || err != nil {
		return "", "", fmt.Errorf("forward %q: want port:host:port", s)
	}
	if _, _, err := net.SplitHostPort(rest); err != nil {
		return "", "", fmt.Errorf("forward %q: %w", s, err)
	}
	return ":" + p, rest, nil
}
