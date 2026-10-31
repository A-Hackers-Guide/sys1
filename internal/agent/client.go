package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/tlsutil"
)

// client talks to the node's client API over pinned TLS.
type client struct {
	base string
	auth string
	http *http.Client
}

func newClient(addr, fingerprint string) *client {
	return &client{
		base: "https://" + addr,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsutil.PinnedConfig(fingerprint), MaxIdleConns: 2, IdleConnTimeout: time.Minute},
		},
	}
}

func (c *client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.auth != "" {
		req.Header.Set("Authorization", "Bearer "+c.auth)
	}
	resp, err := c.http.Do(req)
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

type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return fmt.Sprintf("node said %d: %s", e.Code, e.Msg) }

// Register joins a node using a join code and returns the saved-ready
// config. It is how both `sys0 setup` and `sys0 join` connect.
func Register(ctx context.Context, code protocol.JoinCode, name string, trust protocol.Trust, res protocol.Resources, version string) (Config, error) {
	c := newClient(code.Addr, code.Fingerprint)
	var out protocol.RegisterResponse
	err := c.post(ctx, "/api/v1/register", protocol.RegisterRequest{
		APIVersion: protocol.APIVersion, JoinToken: code.Token, Name: name, Trust: trust, Version: version, Resources: res,
	}, &out)
	if err != nil {
		return Config{}, err
	}
	return Config{NodeAddr: code.Addr, Fingerprint: code.Fingerprint, AgentID: out.AgentID, Secret: out.Secret, Name: name, Trust: trust, MaxTasks: 4}, nil
}
