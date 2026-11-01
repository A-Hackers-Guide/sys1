package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

func port(t *testing.T, srv *httptest.Server) int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	n, _ := strconv.Atoi(p)
	return n
}

func TestHealthCheckCatchesAHungApp(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	h := &protocol.HealthCheck{HTTP: "/health", Port: port(t, srv), Every: 1, Timeout: 1, Failures: 2, Grace: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var why string
	sick := watchHealth(ctx, h, &why)
	select {
	case <-sick:
		t.Fatal("healthy app flagged")
	case <-time.After(2500 * time.Millisecond):
	}
	healthy.Store(false)
	select {
	case <-sick:
		if !strings.Contains(why, "503") {
			t.Fatalf("reason: %q", why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hung app not caught")
	}
}

func TestTCPHealthCheck(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens: every check misses
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var why string
	select {
	case <-watchHealth(ctx, &protocol.HealthCheck{Port: p, Every: 1, Failures: 1, Grace: 1}, &why):
	case <-time.After(4 * time.Second):
		t.Fatal("closed port not caught")
	}
}

func TestHealthCheckValidation(t *testing.T) {
	base := protocol.TaskSpec{Kind: protocol.KindContainer, Image: "nginx:alpine", Network: true, Ports: []string{"8080:80"}, Service: true}
	ok := base
	ok.Health = &protocol.HealthCheck{HTTP: "/", Port: 8080}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]*protocol.HealthCheck{
		"unpublished port": {Port: 9999},
		"bad path":         {HTTP: "health", Port: 8080},
	} {
		s := base
		s.Health = h
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
