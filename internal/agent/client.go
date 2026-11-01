package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
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
	}
	if se, ok := lastErr.(*StatusError); ok {
		// Every node answered "standby": an election is under way.
		return nil, se
	}
	return nil, lastErr
}

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
