package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func resolveTo(ip string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil }
}

func TestPolicyRefuses(t *testing.T) {
	p := Policy{Hosts: []string{"api.example.com", "*.cdn.example.com"}, Resolve: resolveTo("93.184.216.34")}
	ctx := context.Background()
	cases := map[string][2]string{
		"unlisted host":        {"evil.example.net", "443"},
		"parent of a wildcard": {"cdn.example.com", "443"},
		"lookalike suffix":     {"api.example.com.evil.net", "443"},
		"raw IP":               {"93.184.216.34", "443"},
		"SMTP":                 {"api.example.com", "25"},
		"SSH":                  {"api.example.com", "22"},
	}
	for name, c := range cases {
		if _, err := p.Dial(ctx, c[0], c[1]); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, private := range []string{"192.168.1.1", "10.0.0.5", "127.0.0.1", "169.254.169.254", "100.100.100.100", "::1", "fd00::1"} {
		p.Resolve = resolveTo(private)
		if _, err := p.Dial(ctx, "api.example.com", "443"); !errors.Is(err, ErrDenied) {
			t.Errorf("name resolving to %s was let through: %v", private, err)
		}
	}
	if !p.hostAllowed("img.cdn.example.com") || !p.hostAllowed("API.EXAMPLE.COM.") {
		t.Error("allowed host refused")
	}
}

func TestValidHost(t *testing.T) {
	for _, h := range []string{"example.com", "*.example.com", "a-b.c0.io"} {
		if !ValidHost(h) {
			t.Errorf("%q rejected", h)
		}
	}
	for _, h := range []string{"", "*", "localhost", "1.2.3.4", "exa mple.com", "*.", "a..b", "ex_ample.com"} {
		if ValidHost(h) {
			t.Errorf("%q accepted", h)
		}
	}
}

// testProxy serves the proxy with a policy that maps "allowed.test" to a
// local test server (loopback is let through only in tests).
func testProxy(t *testing.T, backend *httptest.Server) *httptest.Server {
	u, _ := url.Parse(backend.URL)
	p := &Proxy{Policy: Policy{
		Hosts: []string{"allowed.test"}, Resolve: resolveTo("127.0.0.1"),
		allowAddr: func(net.IP) bool { return true }, port: map[string]string{"80": u.Port(), "443": u.Port()},
	}}
	s := httptest.NewServer(p)
	t.Cleanup(s.Close)
	return s
}

func TestProxyHTTPAndConnect(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from ", r.Host) }))
	defer backend.Close()
	px := testProxy(t, backend)
	pu, _ := url.Parse(px.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}

	resp, err := client.Get("http://allowed.test/x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(b), "hello from") {
		t.Fatalf("allowed HTTP: %d %q", resp.StatusCode, b)
	}
	resp, _ = client.Get("http://blocked.test/x")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked HTTP got %d", resp.StatusCode)
	}

	// HTTPS goes through CONNECT; check the tunnel carries bytes both ways.
	c, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\n\r\n")
	br := bufio.NewReader(c)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT: %q", status)
	}
	for line, _ := br.ReadString('\n'); line != "\r\n" && line != ""; line, _ = br.ReadString('\n') {
	}
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n")
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "hello from") {
		t.Fatalf("tunnel: %q", rest)
	}

	c2, _ := net.Dial("tcp", pu.Host)
	defer c2.Close()
	fmt.Fprint(c2, "CONNECT blocked.test:443 HTTP/1.1\r\nHost: blocked.test:443\r\n\r\n")
	status, _ = bufio.NewReader(c2).ReadString('\n')
	if !strings.Contains(status, "403") {
		t.Fatalf("blocked CONNECT: %q", status)
	}
}

func TestForward(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "app") }))
	defer backend.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u, _ := url.Parse(backend.URL)
	go Forward(ctx, ln, u.Host)
	resp, err := http.Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "app" {
		t.Fatalf("forwarded %q", b)
	}
	if l, tg, err := ParseForward("8096:sys1-abc:8096"); err != nil || l != ":8096" || tg != "sys1-abc:8096" {
		t.Fatalf("ParseForward: %s %s %v", l, tg, err)
	}
}
