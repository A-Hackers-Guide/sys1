// Package node is sys1: the hub machines join to pool their resources.
//
// It serves two things. The client API (TLS, reachable from the network)
// is where sys0 clients register, report in and hand back results. The
// GUI (plain HTTP, bound to localhost only, token-protected) is the
// dashboard and the input/output for the whole pool.
//
// All state lives in a crash-safe journal, so the node can lose power at
// any moment and come back exactly where it was.
package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/journal"
	"github.com/A-Hackers-Guide/sys1/internal/marketplace"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/tlsutil"
)

type Config struct {
	DataDir     string
	ListenAddr  string   // client API, e.g. ":7443"
	PublicAddr  string   // host:port clients use; guessed when empty
	GUIAddr     string   // dashboard, e.g. "127.0.0.1:7700"
	TrustedKeys []string // marketplace signing keys the owner trusts
	// Pooled storage: copies kept of app data (default 3), and how much
	// disk the node may use to pass chunks between machines (default 4 GiB).
	Copies       int
	StagingBytes int64
}

type Node struct {
	cfg    Config
	log    *slog.Logger
	j      *journal.Journal
	cert   tls.Certificate
	fp     string
	guiKey string

	mu        sync.Mutex
	agents    map[string]*Agent
	tasks     map[string]*Task
	jobs      map[string]*Job
	joinToken string
	// localToken lets the node's own built-in client join as an owner
	// machine. It lives only in memory and never leaves this process.
	localToken string
	catalog    *marketplace.Catalog
	started    time.Time
	pool       *pool
}

func New(cfg Config, log *slog.Logger) (*Node, error) {
	n := &Node{cfg: cfg, log: log, agents: map[string]*Agent{}, tasks: map[string]*Task{}, jobs: map[string]*Job{}, started: time.Now()}
	var err error
	if n.cert, err = tlsutil.LoadOrCreate(cfg.DataDir); err != nil {
		return nil, err
	}
	if err := n.initPool(cfg); err != nil {
		return nil, err
	}
	n.fp = tlsutil.Fingerprint(n.cert.Certificate[0])
	if n.j, err = journal.Open(filepath.Join(cfg.DataDir, "node.journal"), n.replay); err != nil {
		return nil, err
	}
	if n.catalog, err = marketplace.Load(filepath.Join(cfg.DataDir, "packages"), cfg.TrustedKeys); err != nil {
		return nil, err
	}
	if n.joinToken == "" {
		n.joinToken = randomToken()
		n.persistLocked(event{JoinToken: n.joinToken})
	}
	// Machines get a grace period after a node restart before their work
	// is moved, so a quick node reboot does not reshuffle the whole pool.
	for _, a := range n.agents {
		a.LastSeen = n.started
	}
	n.guiKey = loadGUIKey(cfg.DataDir)
	n.localToken = randomToken()
	return n, nil
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// JoinCode is what a new machine pastes into its installer.
func (n *Node) JoinCode() protocol.JoinCode {
	n.mu.Lock()
	defer n.mu.Unlock()
	return protocol.JoinCode{Addr: n.publicAddr(), Fingerprint: n.fp, Token: n.joinToken}
}

// LocalJoinCode is the join code for the node's own built-in client.
func (n *Node) LocalJoinCode() protocol.JoinCode {
	n.mu.Lock()
	defer n.mu.Unlock()
	return protocol.JoinCode{Addr: n.listenAddrFor("127.0.0.1"), Fingerprint: n.fp, Token: n.localToken}
}

func (n *Node) publicAddr() string {
	if n.cfg.PublicAddr != "" {
		return n.cfg.PublicAddr
	}
	return n.listenAddrFor(lanIP())
}

// listenAddrFor returns the client API address, substituting fallback for
// the host when the node listens on every interface.
func (n *Node) listenAddrFor(fallback string) string {
	host, port, _ := net.SplitHostPort(n.cfg.ListenAddr)
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = fallback
	}
	return net.JoinHostPort(host, port)
}

// lanIP finds the address other machines on the network can reach. The
// UDP "dial" sends nothing; it only asks the OS which interface it would use.
func lanIP() string {
	c, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// GUIURL is the dashboard address, including the access key.
func (n *Node) GUIURL(addr string) string {
	return "http://" + addr + "/?key=" + n.guiKey
}

// Serve runs the client API and the GUI until ctx is cancelled. ready, if
// set, receives the actual addresses once both are listening.
func (n *Node) Serve(ctx context.Context, ready func(apiAddr, guiAddr string)) error {
	apiLn, err := tls.Listen("tcp", n.cfg.ListenAddr, tlsutil.ServerConfig(n.cert))
	if err != nil {
		return err
	}
	guiLn, err := net.Listen("tcp", n.cfg.GUIAddr)
	if err != nil {
		apiLn.Close()
		return err
	}
	if host, _, _ := net.SplitHostPort(guiLn.Addr().String()); !net.ParseIP(host).IsLoopback() {
		n.log.Warn("the dashboard is listening beyond this machine; anyone with the URL key can control the pool", "addr", guiLn.Addr())
	}
	n.mu.Lock()
	n.cfg.ListenAddr = apiLn.Addr().String()
	n.mu.Unlock()
	n.saveJoinCode()

	api := &http.Server{Handler: n.apiHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	gui := &http.Server{Handler: n.guiHandler(guiLn.Addr().String()), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- api.Serve(apiLn) }()
	go func() { errc <- gui.Serve(guiLn) }()
	go n.loop(ctx)
	if ready != nil {
		ready(apiLn.Addr().String(), guiLn.Addr().String())
	}
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	api.Shutdown(sctx)
	gui.Shutdown(sctx)
	n.mu.Lock()
	n.compactLocked()
	n.j.Close()
	n.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (n *Node) loop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			n.mu.Lock()
			n.scheduleLocked(now)
			n.maintainPoolLocked(now)
			n.mu.Unlock()
		}
	}
}

// ---- client API ----

func (n *Node) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/register", n.handleRegister)
	mux.HandleFunc("POST /api/v1/heartbeat", n.withAgent(n.handleHeartbeat))
	mux.HandleFunc("POST /api/v1/result", n.withAgent(n.handleResult))
	mux.HandleFunc("PUT /api/v1/chunks/{id}", n.withAgent(n.handleChunkPut))
	mux.HandleFunc("GET /api/v1/chunks/{id}", n.withAgent(n.handleChunkGet))
	mux.HandleFunc("POST /api/v1/inventory", n.withAgent(n.handleInventory))
	mux.HandleFunc("POST /api/v1/snapshot", n.withAgent(n.handleSnapshot))
	mux.HandleFunc("POST /api/v1/restore-result", n.withAgent(n.handleRestoreResult))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func readJSON(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (n *Node) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req protocol.RegisterRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	local := subtle.ConstantTimeCompare([]byte(req.JoinToken), []byte(n.localToken)) == 1
	if !local && subtle.ConstantTimeCompare([]byte(req.JoinToken), []byte(n.joinToken)) != 1 {
		http.Error(w, "this join code is wrong or has been replaced; copy a fresh one from the node", http.StatusForbidden)
		return
	}
	if local {
		req.Trust = protocol.TrustOwner
	}
	if !req.Trust.Valid() {
		req.Trust = protocol.TrustCommunity
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Resources.Hostname
	}
	if len(name) > 64 {
		name = name[:64]
	}
	secret := randomToken()
	a := &Agent{
		ID: randomToken()[:16], Name: name, SecretHash: hashSecret(secret), Trust: req.Trust,
		Version: req.Version, Joined: time.Now(), Resources: req.Resources, LastSeen: time.Now(), Local: local,
	}
	n.agents[a.ID] = a
	n.persistLocked(event{Agent: a})
	n.log.Info("machine joined", "name", a.Name, "os", a.Resources.OS, "trust", a.Trust)
	writeJSON(w, protocol.RegisterResponse{AgentID: a.ID, Secret: secret})
}

// withAgent authenticates "Authorization: Bearer <id>.<secret>".
func (n *Node) withAgent(h func(http.ResponseWriter, *http.Request, *Agent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, secret, _ := strings.Cut(cred, ".")
		n.mu.Lock()
		a := n.agents[id]
		n.mu.Unlock()
		if a == nil || subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(a.SecretHash)) != 1 {
			http.Error(w, "unknown machine", http.StatusUnauthorized)
			return
		}
		h(w, r, a)
	}
}

func (n *Node) handleHeartbeat(w http.ResponseWriter, r *http.Request, a *Agent) {
	var req protocol.HeartbeatRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	a.Resources, a.LastSeen, a.Version = req.Resources, now, req.Version
	reported := map[string]int{}
	for _, rt := range req.Running {
		reported[rt.ID] = rt.Attempt
	}
	var resp protocol.HeartbeatResponse
	for id, attempt := range reported {
		t := n.tasks[id]
		if t == nil || t.AgentID != a.ID || t.Attempt != attempt || terminal(t.State) || t.State == StateStopping {
			resp.Cancel = append(resp.Cancel, id)
		}
	}
	for _, t := range n.tasks {
		if t.AgentID != a.ID {
			continue
		}
		attempt, has := reported[t.ID]
		has = has && attempt == t.Attempt
		switch t.State {
		case StateAssigned:
			if has {
				t.State, t.Message = StateRunning, "Running on "+a.Name
				n.saveTaskLocked(t)
			} else {
				pt := protocol.Task{ID: t.ID, Attempt: t.Attempt, Spec: t.Spec, Checkpoint: t.Checkpoint}
				if len(t.Spec.Volumes) > 0 {
					pt.DataKey, pt.Restore = n.dataKey("task:"+t.ID), t.Snapshot
				}
				resp.Assign = append(resp.Assign, pt)
			}
		case StateRunning:
			// The machine forgot it (it restarted) without reporting a
			// result: send the task elsewhere.
			if !has {
				n.requeueLocked(t, "machine restarted; moving it", false)
			}
		case StateStopping:
			if !has {
				t.State, t.Message = StateStopped, "Stopped"
				n.saveTaskLocked(t)
			}
		}
	}
	n.storageHeartbeatLocked(a, req.Storage, &resp, now)
	writeJSON(w, resp)
}

func (n *Node) handleResult(w http.ResponseWriter, r *http.Request, a *Agent) {
	var res protocol.Result
	if !readJSON(w, r, 2*protocol.MaxCheckpoint+(1<<20), &res) {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.tasks[res.TaskID]
	if t == nil || t.AgentID != a.ID || t.Attempt != res.Attempt {
		http.Error(w, "stale result", http.StatusConflict)
		return
	}
	t.Output, t.ExitCode = res.Output, res.ExitCode
	if len(res.Checkpoint) > 0 && len(res.Checkpoint) <= protocol.MaxCheckpoint {
		t.Checkpoint = res.Checkpoint
	}
	switch res.Outcome {
	case protocol.OutcomeCompleted:
		t.State, t.Message = StateDone, "Finished on "+a.Name
		n.saveTaskLocked(t)
	case protocol.OutcomeStopped:
		t.State, t.Message = StateStopped, "Stopped"
		n.saveTaskLocked(t)
	case protocol.OutcomeRefused:
		n.requeueLocked(t, a.Name+" refused it: "+res.Message, true)
	default:
		n.requeueLocked(t, a.Name+": "+res.Message, true)
	}
	n.log.Info("task result", "task", t.ID, "machine", a.Name, "outcome", res.Outcome, "state", t.State)
	w.WriteHeader(http.StatusOK)
}

// Deploy queues a marketplace package to run on the pool.
func (n *Node) Deploy(pkgName string, inputs map[string]string) (*Task, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.catalog.Packages[pkgName]
	if p == nil {
		return nil, errors.New("no such package")
	}
	if !p.Verified {
		return nil, errors.New("package is not verified: " + p.Problem)
	}
	spec := p.Apply(inputs)
	if spec.Group != nil {
		if w, err := strconv.Atoi(inputs["WORKERS"]); err == nil && w > 0 {
			g := *spec.Group
			g.Workers = w
			spec.Group = &g
		}
		j, err := n.queueGroupLocked(p.Name, p.Title, spec)
		if err != nil {
			return nil, err
		}
		return n.tasks[j.Head], nil
	}
	if spec.Shards > 0 {
		if s, err := strconv.Atoi(inputs["SHARDS"]); err == nil && s > 0 {
			spec.Shards = s
		}
		if v, err := strconv.Atoi(inputs["VERIFY"]); err == nil {
			spec.Verify = v
			if v <= 1 {
				spec.Verify = 0
			}
		}
		j, err := n.queueJobLocked(p.Name, p.Title, spec)
		if err != nil {
			return nil, err
		}
		return n.tasks[j.Children[0]], nil
	}
	return n.queueLocked(p.Name, p.Title, spec)
}

func (n *Node) queueLocked(pkg, title string, spec protocol.TaskSpec) (*Task, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	t := &Task{ID: randomToken()[:12], Package: pkg, Title: title, Spec: spec, State: StateQueued, Attempt: 1, Created: time.Now(), Message: "Finding the best machine"}
	n.tasks[t.ID] = t
	n.saveTaskLocked(t)
	n.scheduleLocked(time.Now())
	return t, nil
}

func (n *Node) StopTask(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.tasks[id]
	if t == nil {
		return errors.New("no such task")
	}
	switch {
	case terminal(t.State):
		delete(n.tasks, id)
		n.persistLocked(event{DelTask: id})
		n.deleteSnapshotsLocked("task:"+id, 0)
	case t.AgentID == "":
		t.State, t.Message = StateStopped, "Stopped"
		n.saveTaskLocked(t)
	default:
		t.State, t.Message = StateStopping, "Stopping"
		n.saveTaskLocked(t)
	}
	return nil
}

func (n *Node) RemoveAgent(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.agents[id]
	if a == nil {
		return errors.New("no such machine")
	}
	if a.Local {
		return errors.New("the node's own machine cannot be removed")
	}
	delete(n.agents, id)
	n.persistLocked(event{DelAgent: id})
	delete(n.pool.held, id)
	delete(n.pool.queue, id)
	for _, t := range n.tasks {
		if t.AgentID == id && !terminal(t.State) {
			n.requeueLocked(t, "machine removed; moving it", false)
		}
	}
	return nil
}

// RotateJoinCode invalidates the old join code. Machines that already
// joined keep working; they authenticate with their own secret.
func (n *Node) RotateJoinCode() {
	n.mu.Lock()
	n.joinToken = randomToken()
	n.persistLocked(event{JoinToken: n.joinToken})
	n.mu.Unlock()
	n.saveJoinCode()
}

// JoinCodeFile holds the current join code so `sys1 join-code` can print
// it without disturbing a running node.
const JoinCodeFile = "join-code.txt"

func (n *Node) saveJoinCode() {
	os.WriteFile(filepath.Join(n.cfg.DataDir, JoinCodeFile), []byte(n.JoinCode().String()+"\n"), 0o600)
}
