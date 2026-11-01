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
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/crash"
	"github.com/A-Hackers-Guide/sys1/internal/engines"
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
	// Updates is shown on the node: "automatic", or why this install
	// doesn't update itself.
	Updates string
	// OnUpdateNow is called when the node asks for an update check now.
	OnUpdateNow func()

	cfg     Config
	dataDir string
	log     *slog.Logger

	crashesSent time.Time
	hostTried   string // the cluster code last tried, so an order runs once
	hostProblem string
	// Host runs the sys1 commands that make this machine a cluster member;
	// nil when it can't (no sys1 beside sys0, or no right to install a
	// service). Tests replace it.
	Host      func(ctx context.Context, args ...string) error
	sampler   *resources.Sampler
	engines   *engines.Supervisor
	updateNow int
	c         *client
	outbox    *journal.Journal
	st        *agentStorage

	mu       sync.Mutex
	running  map[string]*taskRun
	vms      map[string]*vmProc
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
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs // container volume paths must be absolute
	}
	a := &Agent{
		cfg:      cfg,
		dataDir:  dataDir,
		Host:     defaultHost(),
		log:      log,
		c:        newClient(cfg.Addrs(), cfg.Fingerprint),
		running:  map[string]*taskRun{},
		vms:      map[string]*vmProc{},
		tasksDir: tasksDir(dataDir),
	}
	a.c.auth = cfg.AgentID + "." + cfg.Secret
	a.sampler = &resources.Sampler{DataDir: dataDir}
	a.Sample = a.sampler.Sample
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
// sendCrashes hands crash reports from earlier runs to the node.
func (a *Agent) sendCrashes(ctx context.Context) {
	reports, done := crash.Queued(a.dataDir)
	for i, r := range reports {
		err := a.c.post(ctx, "/api/v1/crash", r, nil)
		var se *StatusError
		if err == nil || (errors.As(err, &se) && se.Code == http.StatusBadRequest) {
			done[i]()
		}
	}
}

func (a *Agent) Run(ctx context.Context) error {
	defer a.shutdown()
	if a.cfg.KeepsEngines() {
		a.engines = &engines.Supervisor{Log: a.log.With("component", "engines"), Changed: func() {
			if a.sampler != nil {
				a.sampler.Refresh()
			}
		}}
		go a.engines.Run(ctx)
	}
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
			if !a.cfg.NoCrashReports && time.Since(a.crashesSent) > 10*time.Minute {
				a.crashesSent = time.Now()
				go a.sendCrashes(ctx)
			}
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
	res.Updates = a.Updates
	if a.engines != nil {
		if p := a.engines.Problem(); p != "" {
			res.Problems = append([]string{p}, res.Problems...)
		}
	}
	if p := missingExe(); p != "" {
		res.Problems = append([]string{p}, res.Problems...)
	}
	a.mu.Lock()
	res.Hosting = a.cfg.Hosting
	res.CanHost = res.Hosting == "" && !a.cfg.NoHosting && a.cfg.Trust == protocol.TrustOwner && a.Host != nil
	if a.hostProblem != "" {
		res.Problems = append([]string{a.hostProblem}, res.Problems...)
	}
	a.last = res
	req := protocol.HeartbeatRequest{Resources: res, Version: version.Version, Storage: a.st.report()}
	for id, r := range a.running {
		rt := protocol.RunningTask{ID: id, Attempt: r.attempt}
		if p := a.vms[id]; p != nil {
			rt.Screen = p.screen
		}
		req.Running = append(req.Running, rt)
	}
	a.mu.Unlock()
	var resp protocol.HeartbeatResponse
	err := a.c.post(ctx, "/api/v1/heartbeat", req, &resp)
	return resp, err
}

func (a *Agent) apply(ctx context.Context, resp protocol.HeartbeatResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if o := resp.Host; o != nil && a.cfg.Hosting == "" && !a.cfg.NoHosting && a.Host != nil && o.Code != a.hostTried {
		a.hostTried = o.Code
		go a.host(ctx, *o)
	}
	if resp.UpdateNow != a.updateNow {
		if a.updateNow != 0 && a.OnUpdateNow != nil {
			go a.OnUpdateNow()
		}
		a.updateNow = resp.UpdateNow
	}
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
	if !protocol.ValidID(t.ID) {
		return "invalid task ID"
	}
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
		if _, err := exec.LookPath(t.Spec.Command[0]); err != nil {
			return t.Spec.Command[0] + " isn't installed on this machine"
		}
	case protocol.KindContainer:
		if !a.last.HasContainers() {
			return "no container runtime available"
		}
	case protocol.KindVM:
		if a.cfg.Trust != protocol.TrustOwner {
			return "virtual machines run only on their owner's own machines"
		}
		if !a.last.HasRuntime("qemu-" + vmArch(t.Spec.VM)) {
			return "QEMU for this VM isn't installed here"
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

func (a *Agent) setVM(id string, p *vmProc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p == nil {
		delete(a.vms, id)
	} else {
		a.vms[id] = p
	}
}

func (a *Agent) vmFor(id string) *vmProc {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.vms[id]
}

// missingExe notices this program's file was removed while it runs, which
// is what antivirus quarantine looks like: it keeps working until the next
// restart, then can't start at all.
func missingExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(exe); !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	why := "Something removed " + filepath.Base(exe) + " while it was running, so it won't start again after a restart."
	if runtime.GOOS == "windows" {
		why += " If Windows Security quarantined it, open Windows Security → Virus & threat protection → Protection history, choose Allow on device, then reinstall sys1."
	}
	return why
}
