package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// watchHealth checks a running service until ctx ends. It closes the
// returned channel, with the reason in *why, once the service has missed
// enough checks in a row that it should be restarted.
func watchHealth(ctx context.Context, h *protocol.HealthCheck, why *string) <-chan struct{} {
	sick := make(chan struct{})
	if h == nil {
		return sick
	}
	every := secs(h.Every, 30)
	timeout := secs(h.Timeout, 5)
	failures := h.Failures
	if failures == 0 {
		failures = 3
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(h.Port))
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(secs(h.Grace, 60)):
		}
		missed := 0
		for {
			err := probe(ctx, client, addr, h.HTTP, timeout)
			if err == nil {
				missed = 0
			} else if missed++; missed >= failures {
				*why = fmt.Sprintf("stopped answering its health check %d times in a row (%v); restarting it", missed, err)
				close(sick)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
	}()
	return sick
}

func probe(ctx context.Context, c *http.Client, addr, path string, timeout time.Duration) error {
	if path == "" {
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+path, nil)
	req.Header.Set("User-Agent", "sys0-health")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func secs(n, def int) time.Duration {
	if n <= 0 {
		n = def
	}
	return time.Duration(n) * time.Second
}
