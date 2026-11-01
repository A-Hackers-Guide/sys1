package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/tlsutil"
)

// client talks to the node's client API over pinned TLS. It knows every
// address the node has, sticks with whichever answers, and moves to the
// next when one stops answering.
type client struct {
	auth string
	http *http.Client

	mu    sync.Mutex
	addrs []string
	cur   int
}

func newClient(addrs []string, fingerprint string) *client {
	return &client{
		addrs: addrs,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsutil.PinnedConfig(fingerprint), MaxIdleConns: 2, IdleConnTimeout: time.Minute,
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
		},
	}
}

// setAddrs replaces the address list, keeping the current one first if
// it is still listed.
func (c *client) setAddrs(addrs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.addrs[c.cur]
	c.addrs, c.cur = addrs, 0
	for i, a := range addrs {
		if a == cur {
			c.cur = i
		}
	}
}

// do sends a request built by mk, trying each address in turn when the
// network fails. HTTP errors from a node that answered are not retried.
func (c *client) do(mk func(base string) (*http.Request, error)) (*http.Response, error) {
	c.mu.Lock()
	addrs, start := c.addrs, c.cur
	c.mu.Unlock()
	var lastErr error
	var errs []addrErr
	for k := 0; k < len(addrs); k++ {
		i := (start + k) % len(addrs)
		req, err := mk("https://" + addrs[i])
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err == nil && resp.StatusCode == http.StatusMisdirectedRequest {
			// A standby node in a cluster: the leader is at another address.
			resp.Body.Close()
			lastErr = &StatusError{Code: resp.StatusCode, Msg: "standby node"}
			continue
		}
		if err == nil {
			if k > 0 {
				c.mu.Lock()
				if len(c.addrs) == len(addrs) {
					c.cur = i
				}
				c.mu.Unlock()
			}
			return resp, nil
		}
		if req.Context().Err() != nil {
			return nil, err
		}
		lastErr = err
		errs = append(errs, addrErr{addrs[i], err})
	}
	if se, ok := lastErr.(*StatusError); ok {
		// Every node answered "standby": an election is under way.
		return nil, se
	}
	if len(errs) > 1 {
		return nil, &unreachable{errs}
	}
	return nil, lastErr
}

type addrErr struct {
	addr string
	err  error
}

// unreachable says what went wrong at every address, not only the last,
// with a hint when the operating system is in the way.
type unreachable struct{ errs []addrErr }

func (u *unreachable) Error() string {
	var parts []string
	for _, e := range u.errs {
		parts = append(parts, e.addr+": "+shortNetErr(e.err))
	}
	msg := strings.Join(parts, "; ")
	if h := netHint(u.errs); h != "" {
		msg += " (" + h + ")"
	}
	return msg
}

func (u *unreachable) Unwrap() []error {
	out := make([]error, len(u.errs))
	for i, e := range u.errs {
		out[i] = e.err
	}
	return out
}

func shortNetErr(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// netHint explains a local-network address that can't be reached while
// the machine is otherwise online: on macOS that's Local Network privacy.
func netHint(errs []addrErr) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, e := range errs {
		host, _, _ := net.SplitHostPort(e.addr)
		ip := net.ParseIP(host)
		if ip != nil && ip.IsPrivate() && (errors.Is(e.err, syscall.EHOSTUNREACH) || errors.Is(e.err, syscall.ENETUNREACH)) {
			return MacLocalNetworkHint
		}
	}
	return ""
}

// MacLocalNetworkHint is shown when macOS keeps sys0 off the local network.
const MacLocalNetworkHint = "macOS is keeping sys0 off your local network: allow sys0 under System Settings → Privacy & Security → Local Network, or run it as a system service, which macOS doesn't restrict: sudo sys0 install"

func (c *client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(func(base string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.auth != "" {
			req.Header.Set("Authorization", "Bearer "+c.auth)
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Msg: string(bytes.TrimSpace(msg))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// raw sends a request with an opaque body and returns the response body.
func (c *client) raw(ctx context.Context, method, path string, body []byte, max int64) ([]byte, error) {
	resp, err := c.do(func(base string) (*http.Request, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Authorization", "Bearer "+c.auth)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Code: resp.StatusCode, Msg: string(bytes.TrimSpace(msg))}
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return fmt.Sprintf("node said %d: %s", e.Code, e.Msg) }

// Register joins a node using a join code and returns the saved-ready
// config. It is how both `sys0 setup` and `sys0 join` connect.
func Register(ctx context.Context, code protocol.JoinCode, name string, trust protocol.Trust, res protocol.Resources, version string) (Config, error) {
	c := newClient(code.Addrs(), code.Fingerprint)
	var out protocol.RegisterResponse
	err := c.post(ctx, "/api/v1/register", protocol.RegisterRequest{
		APIVersion: protocol.APIVersion, JoinToken: code.Token, Name: name, Trust: trust, Version: version, Resources: res,
	}, &out)
	if err != nil {
		return Config{}, err
	}
	c.mu.Lock()
	reached := c.addrs[c.cur]
	c.mu.Unlock()
	addrs := []string{reached}
	for _, x := range code.Addrs() {
		if x != reached {
			addrs = append(addrs, x)
		}
	}
	if out.Trust.Valid() {
		trust = out.Trust // the node decides from the join code
	}
	return Config{NodeAddr: reached, NodeAddrs: addrs, Fingerprint: code.Fingerprint, AgentID: out.AgentID, Secret: out.Secret, Name: name, Trust: trust, MaxTasks: 4, StorageGB: DefaultStorageGB}, nil
}
