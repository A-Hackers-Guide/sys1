// Package agent is sys0: the background client that lends a machine's
// spare capacity to a sys1 node.
//
// It reports in every few seconds, runs what the node assigns under a
// watchdog, and keeps finished results in a durable outbox until the node
// confirms it has them, so a crash or power cut never loses a result.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/journal"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

type Agent struct {
	// Sample measures this machine. It defaults to the real measurement;
	// tests replace it so they do not depend on the host's load or battery.
	Sample func(allowExec bool) protocol.Resources
	// FixedAddrs keeps the addresses from the config instead of following
	// the node's list (the node's own built-in client uses loopback).
	FixedAddrs bool

	cfg     Config
	dataDir string
	log     *slog.Logger
	c       *client
	outbox  *journal.Journal
	st      *agentStorage

	mu       sync.Mutex
	running  map[string]*taskRun
	pending  []protocol.Result
	last     protocol.Resources
	tasksDir string
}

type taskRun struct {
	attempt      int
	cancel       context.CancelFunc
	done         chan struct{}
	cancelByNode bool
}

func New(dataDir string, cfg Config, log *slog.Logger) (*Agent, error) {
	a := &Agent{
		cfg:      cfg,
		dataDir:  dataDir,
		log:      log,
		c:        newClient(cfg.Addrs(), cfg.Fingerprint),
		running:  map[string]*taskRun{},
		tasksDir: tasksDir(dataDir),
	}
	a.c.auth = cfg.AgentID + "." + cfg.Secret
	a.Sample = (&resources.Sampler{DataDir: dataDir}).Sample
	var err error
	a.outbox, err = journal.Open(filepath.Join(dataDir, "outbox.log"), func(b []byte) error {
		var r protocol.Result
		if json.Unmarshal(b, &r) == nil {
			a.pending = append(a.pending, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if a.st, err = newAgentStorage(a); err != nil {
		a.outbox.Close()
		return nil, err
	}
	return a, nil
}

// tasksDir is where task state lives. Docker Desktop on macOS can only
// bind-mount from shared folders, and /Library is not one of them.
func tasksDir(dataDir string) string {
	if runtime.GOOS == "darwin" && os.Geteuid() == 0 {
		return "/private/var/sys0/tasks"
	}
	return filepath.Join(dataDir, "tasks")
}

// Run reports to the node until ctx is cancelled. Network trouble never
// stops it; it backs off and keeps trying, and running tasks carry on.
func (a *Agent) Run(ctx context.Context) error {
	defer a.shutdown()
	a.log.Info("sys0 running", "node", a.cfg.NodeAddr, "name", a.cfg.Name, "trust", a.cfg.Trust, "version", version.Version)
	backoff := time.Second
	for {
		a.flushResults(ctx)
		resp, err := a.heartbeat(ctx)
		var wait time.Duration
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			var se *StatusError
			if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
				a.log.Error("the node no longer recognizes this machine; run `sys0 setup` to join again")
				wait = time.Minute
			} else {
				a.log.Warn("cannot reach node, will retry", "err", err, "in", backoff)
				wait = backoff
				backoff = min(backoff*2, 30*time.Second)
			}
		default:
			backoff = time.Second
			a.followNode(resp.NodeAddrs)
			a.apply(ctx, resp)
			a.st.apply(ctx, resp)
			// Jitter spreads thousands of clients out so they do not all
			// hit the node at the same instant.
			wait = protocol.HeartbeatInterval + time.Duration(rand.Int64N(int64(time.Second))) - 500*time.Millisecond
			if resp.Hurry {
				wait = time.Second
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) (protocol.HeartbeatResponse, error) {
	res := a.Sample(a.cfg.Trust == protocol.TrustOwner)
	res.OpenInternet = a.cfg.OpenInternet
	a.mu.Lock()
	a.last = res
	req := protocol.HeartbeatRequest{Resources: res, Version: version.Version, Storage: a.st.report()}
	for id, r := range a.running {
		req.Running = append(req.Running, protocol.RunningTask{ID: id, Attempt: r.attempt})
	}
	a.mu.Unlock()
	var resp protocol.HeartbeatResponse
	err := a.c.post(ctx, "/api/v1/heartbeat", req, &resp)
	return resp, err
}

func (a *Agent) apply(ctx context.Context, resp protocol.HeartbeatResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range resp.Cancel {
		if r := a.running[id]; r != nil {
			r.cancelByNode = true
			r.cancel()
		}
	}
	for _, t := range resp.Assign {
		if r := a.running[t.ID]; r != nil {
			if r.attempt == t.Attempt {
				continue
			}
			r.cancel()
		}
		if reason := a.refuse(t); reason != "" {
			a.queueResultLocked(protocol.Result{TaskID: t.ID, Attempt: t.Attempt, Outcome: protocol.OutcomeRefused, Message: reason})
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		r := &taskRun{attempt: t.Attempt, cancel: cancel, done: make(chan struct{})}
		a.running[t.ID] = r
		a.log.Info("starting task", "task", t.ID, "attempt", t.Attempt, "image", t.Spec.Image)
		go a.runTask(tctx, t, r)
	}
}

// refuse is the client's own safety check. The node should never send
// work a machine has not allowed, but the client does not take its word.
func (a *Agent) refuse(t protocol.Task) string {
	if err := t.Spec.Validate(); err != nil {
		return "invalid task: " + err.Error()
	}
	if len(a.running) >= a.cfg.MaxTasks {
		return "machine is at its task limit"
	}
	if t.Spec.OwnerOnly && a.cfg.Trust != protocol.TrustOwner {
		return "this app runs only on its owner's own machines"
	}
	if t.Spec.Sandboxed(a.cfg.Trust, a.cfg.OpenInternet) {
		if p := t.Spec.SandboxProblem(); p != "" {
			return "this app " + p
		}
	}
	switch t.Spec.Kind {
	case protocol.KindExec:
		if a.cfg.Trust != protocol.TrustOwner {
			return "native programs are not allowed on this machine"
		}
	case protocol.KindContainer:
		if !a.last.HasContainers() {
			return "no container runtime available"
		}
	}
	return ""
}

func (a *Agent) runTask(ctx context.Context, t protocol.Task, r *taskRun) {
	defer close(r.done)
	res := a.execute(ctx, t)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running[t.ID] == r {
		delete(a.running, t.ID)
	}
	// A task stopped because sys0 itself is shutting down is not reported:
	// the node notices the machine is gone and moves the task elsewhere.
	if res.Outcome == protocol.OutcomeStopped && !r.cancelByNode {
		return
	}
	a.log.Info("task finished", "task", t.ID, "outcome", res.Outcome, "exit", res.ExitCode)
	a.queueResultLocked(res)
}

// queueResultLocked saves a result to the durable outbox before anything
// else, so it survives a crash before delivery.
func (a *Agent) queueResultLocked(r protocol.Result) {
	b, _ := json.Marshal(r)
	if err := a.outbox.Append(b); err != nil {
		a.log.Error("cannot save result to outbox", "err", err)
	}
	a.pending = append(a.pending, r)
}

func (a *Agent) flushResults(ctx context.Context) {
	a.mu.Lock()
	pending := append([]protocol.Result(nil), a.pending...)
	a.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	sent := 0
	for _, r := range pending {
		err := a.c.post(ctx, "/api/v1/result", r, nil)
		var se *StatusError
		if err != nil && !(errors.As(err, &se) && se.Code >= 400 && se.Code < 500 && se.Code != http.StatusUnauthorized) {
			break // node unreachable: keep it for next time
		}
		sent++ // delivered, or permanently rejected (stale attempt)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = a.pending[sent:]
	var keep [][]byte
	for _, r := range a.pending {
		b, _ := json.Marshal(r)
		keep = append(keep, b)
	}
	if err := a.outbox.Compact(keep); err != nil {
		a.log.Error("cannot compact outbox", "err", err)
	}
}

func (a *Agent) shutdown() {
	a.mu.Lock()
	runs := make([]*taskRun, 0, len(a.running))
	for _, r := range a.running {
		r.cancel()
		runs = append(runs, r)
	}
	a.mu.Unlock()
	deadline := time.After(25 * time.Second)
	for _, r := range runs {
		select {
		case <-r.done:
		case <-deadline:
		}
	}
	a.outbox.Close()
}

// followNode adopts the node's current addresses, so a node whose address
// changes (a new IP, a newly opened public port) stays reachable.
func (a *Agent) followNode(addrs []string) {
	if a.FixedAddrs || len(addrs) == 0 || slices.Equal(addrs, a.cfg.Addrs()) {
		return
	}
	a.cfg.NodeAddrs, a.cfg.NodeAddr = addrs, addrs[0]
	a.c.setAddrs(addrs)
	if err := SaveConfig(a.dataDir, a.cfg); err != nil {
		a.log.Warn("could not save the node's new addresses", "err", err)
	}
}
