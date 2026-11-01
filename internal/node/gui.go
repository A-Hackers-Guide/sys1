package node

import (
	"crypto/subtle"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/marketplace"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

//go:embed web
var webFS embed.FS

// oldKeyCookie is how releases before v0.8 kept the key. A cookie for
// 127.0.0.1 is sent to every port on it, apps on the pool included, so the
// key now lives in the dashboard page's own storage and travels in a header.
const oldKeyCookie = "sys1key"

// loadGUIKey keeps the dashboard key across restarts so a bookmarked
// dashboard link keeps working.
func loadGUIKey(dataDir string) string {
	path := filepath.Join(dataDir, "dashboard.key")
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	k := randomToken()
	os.WriteFile(path, []byte(k+"\n"), 0o600)
	return k
}

func (n *Node) dashKey() string { return n.guiKey.Load().(string) }

// RotateGUIKey replaces the dashboard key: old links and browsers stop
// working. It returns the new key.
func (n *Node) RotateGUIKey() (string, error) {
	k := randomToken()
	path := filepath.Join(n.cfg.DataDir, "dashboard.key")
	if err := os.WriteFile(path+".tmp", []byte(k+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return "", err
	}
	n.guiKey.Store(k)
	return k, nil
}

// GUIKey reads the dashboard key from a data directory (for `sys1 open`).
func GUIKey(dataDir string) string { return loadGUIKey(dataDir) }

// guiHandler serves the dashboard. It defends against other websites and
// other local users: requests must name a loopback host (stopping DNS
// rebinding) and carry the key in an Authorization header, which no other
// website can set (there is no CORS) and no other port on this machine is
// ever sent, unlike a cookie.
func (n *Node) guiHandler(addr string) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	allowedHosts := map[string]bool{addr: true, "localhost:" + port: true, "127.0.0.1:" + port: true, "[::1]:" + port: true}
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(oldKeyCookie); err == nil {
			http.SetCookie(w, &http.Cookie{Name: oldKeyCookie, Value: "", Path: "/", MaxAge: -1})
		}
		// The page takes ?key= into its own storage and drops it from the
		// address bar; the files themselves are no secret.
		w.Header().Set("Referrer-Policy", "no-referrer")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/state", n.guiAuth(n.handleState))
	mux.HandleFunc("GET /api/catalog", n.guiAuth(n.handleCatalog))
	mux.HandleFunc("POST /api/deploy", n.guiAuth(n.handleDeploy))
	mux.HandleFunc("POST /api/tasks/{id}/stop", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		replyErr(w, n.StopTask(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/agents/{id}/remove", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		replyErr(w, n.RemoveAgent(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/jobs/{id}/stop", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		replyErr(w, n.StopJob(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/jobs/{id}/collect", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentID string `json:"agentId"`
			Dest    string `json:"dest"`
		}
		if !readJSON(w, r, 1<<16, &req) {
			return
		}
		count, err := n.CollectJob(r.PathValue("id"), req.AgentID, req.Dest)
		if err != nil {
			replyErr(w, err)
			return
		}
		writeJSON(w, map[string]int{"restores": count})
	}))
	mux.HandleFunc("GET /api/cluster/code", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		code, err := n.ClusterCode()
		if err != nil {
			replyErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"code": code})
	}))
	mux.HandleFunc("POST /api/cluster/enable", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		if err := n.EnableCluster(); err != nil {
			replyErr(w, err)
			return
		}
		writeJSON(w, struct{}{})
	}))
	mux.HandleFunc("POST /api/cluster/auto-standby", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		if !readJSON(w, r, 4096, &req) {
			return
		}
		n.SetAutoStandby(req.On)
		writeJSON(w, struct{}{})
	}))
	mux.HandleFunc("POST /api/cluster/prefer", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Node string `json:"node"`
		}
		if !readJSON(w, r, 4096, &req) {
			return
		}
		if err := n.Prefer(req.Node); err != nil {
			replyErr(w, err)
			return
		}
		writeJSON(w, struct{}{})
	}))
	mux.HandleFunc("GET /api/storage", n.guiAuth(n.handleStorage))
	mux.HandleFunc("GET /api/crashes", n.guiAuth(n.handleCrashes))
	mux.HandleFunc("GET /metrics", n.guiAuth(n.handleMetrics))
	mux.HandleFunc("POST /api/update-all", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		n.UpdateAll()
		writeJSON(w, struct{}{})
	}))
	mux.HandleFunc("POST /api/dashboard-key/rotate", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		k, err := n.RotateGUIKey()
		if err != nil {
			replyErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"key": k})
	}))
	mux.HandleFunc("POST /api/crashes/sending", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ On bool }
		if readJSON(w, r, 1<<10, &req) {
			n.crashes.SetSending(req.On)
			writeJSON(w, struct{}{})
		}
	}))
	mux.HandleFunc("POST /api/crashes/{sig}/forget", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		n.crashes.Forget(r.PathValue("sig"))
		writeJSON(w, struct{}{})
	}))
	mux.HandleFunc("GET /api/storage/recovery-key", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"key": n.RecoveryKey()})
	}))
	mux.HandleFunc("POST /api/backups", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentID         string `json:"agentId"`
			Path            string `json:"path"`
			Copies          int    `json:"copies"`
			IntervalMinutes int    `json:"intervalMinutes"`
			Keep            int    `json:"keep"`
		}
		if !readJSON(w, r, 1<<16, &req) {
			return
		}
		_, err := n.AddBackup(req.AgentID, req.Path, req.Copies, req.IntervalMinutes, req.Keep)
		replyErr(w, err)
	}))
	mux.HandleFunc("POST /api/backups/{id}/run", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		replyErr(w, n.RunBackup(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/backups/{id}/remove", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		replyErr(w, n.RemoveBackup(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/restores", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root    string `json:"root"`
			AgentID string `json:"agentId"`
			Dest    string `json:"dest"`
		}
		if !readJSON(w, r, 1<<16, &req) {
			return
		}
		_, err := n.StartRestore(req.Root, req.AgentID, req.Dest)
		replyErr(w, err)
	}))
	mux.HandleFunc("POST /api/internet", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		if !readJSON(w, r, 1<<10, &req) {
			return
		}
		n.SetInternet(req.On)
		replyErr(w, nil)
	}))
	mux.HandleFunc("POST /api/joincode/rotate", n.guiAuth(func(w http.ResponseWriter, r *http.Request) {
		n.RotateJoinCode()
		replyErr(w, nil)
	}))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		csp := "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
		if r.URL.Path == "/screen.html" {
			// The screen page connects to the VM's machine over WebSocket.
			csp += "; connect-src 'self' ws: wss:"
		}
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (n *Node) guiAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(key), []byte(n.dashKey())) != 1 {
			http.Error(w, "locked", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Sys1") != "1" {
			http.Error(w, "missing X-Sys1 header", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && !n.leading() {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, map[string]string{"error": "This node is a standby. Make changes on the dashboard of the node that is leading."})
			return
		}
		h(w, r)
	}
}

func replyErr(w http.ResponseWriter, err error) {
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type agentView struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Trust     protocol.Trust     `json:"trust"`
	Online    bool               `json:"online"`
	Local     bool               `json:"local"`
	Version   string             `json:"version"`
	LastSeen  time.Time          `json:"lastSeen"`
	Joined    time.Time          `json:"joined"`
	Tasks     int                `json:"tasks"`
	Resources protocol.Resources `json:"resources"`
	Disagreed int                `json:"disagreed"`
}

type taskView struct {
	ID        string      `json:"id"`
	Package   string      `json:"package"`
	Title     string      `json:"title"`
	State     string      `json:"state"`
	Agent     string      `json:"agent,omitempty"`
	Attempt   int         `json:"attempt"`
	Message   string      `json:"message"`
	Output    string      `json:"output,omitempty"`
	ExitCode  int         `json:"exitCode"`
	Ports     []string    `json:"ports,omitempty"`
	Kind      string      `json:"kind"`
	Service   bool        `json:"service"`
	Created   time.Time   `json:"created"`
	Updated   time.Time   `json:"updated"`
	HasSaved  bool        `json:"hasCheckpoint"`
	Pooled    bool        `json:"pooledData"`
	Screen    *screenView `json:"screen,omitempty"`
	SavedAt   time.Time   `json:"dataSavedAt"`
	AgentHost string      `json:"agentHost,omitempty"`
}

type totals struct {
	Machines   int    `json:"machines"`
	Online     int    `json:"online"`
	Cores      int    `json:"cores"`
	SpareCores int    `json:"spareCores"`
	MemFree    uint64 `json:"memFree"`
	MemTotal   uint64 `json:"memTotal"`
	DiskFree   uint64 `json:"diskFree"`
	DiskTotal  uint64 `json:"diskTotal"`
	GPUs       int    `json:"gpus"`
	Running    int    `json:"running"`
}

func (n *Node) handleState(w http.ResponseWriter, r *http.Request) {
	code, ownerCode := n.JoinCode(), n.OwnerJoinCode()
	internet := n.InternetStatus()
	clusterStatus := n.ClusterStatus()
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	var out struct {
		Version     string         `json:"version"`
		JoinCode    string         `json:"joinCode"`
		OwnerCode   string         `json:"ownerJoinCode"`
		Updates     string         `json:"updates"`
		PublicAddr  string         `json:"publicAddr"`
		Fingerprint string         `json:"fingerprint"`
		Uptime      float64        `json:"uptime"`
		Totals      totals         `json:"totals"`
		Agents      []agentView    `json:"agents"`
		Tasks       []taskView     `json:"tasks"`
		Jobs        []jobView      `json:"jobs"`
		Addrs       []string       `json:"addrs"`
		Internet    InternetStatus `json:"internet"`
		Cluster     ClusterStatus  `json:"cluster"`
	}
	out.Version, out.JoinCode, out.PublicAddr, out.Fingerprint = version.Version, code.String(), code.Addr, n.fp
	out.OwnerCode = ownerCode.String()
	out.Updates = n.cfg.Updates
	out.Uptime = now.Sub(n.started).Seconds()
	out.Addrs, out.Internet, out.Cluster = code.Addrs(), internet, clusterStatus
	load := map[string]int{}
	for _, t := range n.tasks {
		if !terminal(t.State) && t.AgentID != "" {
			load[t.AgentID]++
		}
		tv := taskView{
			ID: t.ID, Package: t.Package, Title: t.Title, State: t.State, Attempt: t.Attempt, Message: t.Message,
			Output: t.Output, ExitCode: t.ExitCode, Ports: t.Spec.Ports, Kind: string(t.Spec.Kind), Service: t.Spec.Service,
			Created: t.Created, Updated: t.Updated, HasSaved: len(t.Checkpoint) > 0,
			Pooled: len(t.Spec.Volumes) > 0, SavedAt: t.SnapshotAt,
		}
		if a := n.agents[t.AgentID]; a != nil {
			tv.Agent, tv.AgentHost = a.Name, a.Resources.Hostname
			if t.Screen != nil && t.State == StateRunning && a.Resources.Addr != "" {
				tv.Screen = &screenView{Host: a.Resources.Addr, Port: t.Screen.Port, Password: t.Screen.Password}
			}
		}
		if t.State == StateRunning {
			out.Totals.Running++
		}
		if t.Parent != "" {
			continue // shown as part of its job
		}
		out.Tasks = append(out.Tasks, tv)
	}
	for _, a := range n.agents {
		av := agentView{ID: a.ID, Name: a.Name, Trust: a.Trust, Online: n.online(a, now), Local: a.Local, Version: a.Version,
			LastSeen: a.LastSeen, Joined: a.Joined, Tasks: load[a.ID], Resources: a.Resources, Disagreed: a.Disagreed}
		out.Agents = append(out.Agents, av)
		out.Totals.Machines++
		if !av.Online {
			continue
		}
		res := a.Resources
		out.Totals.Online++
		out.Totals.Cores += res.CPUCores
		out.Totals.SpareCores += int(float64(res.CPUCores)*res.CPUIdlePercent/100 + 0.5)
		out.Totals.MemFree += res.MemFree
		out.Totals.MemTotal += res.MemTotal
		out.Totals.DiskFree += res.DiskFree
		out.Totals.DiskTotal += res.DiskTotal
		out.Totals.GPUs += len(res.GPUs)
	}
	sort.Slice(out.Agents, func(i, j int) bool {
		if out.Agents[i].Local != out.Agents[j].Local {
			return out.Agents[i].Local
		}
		return out.Agents[i].Name < out.Agents[j].Name
	})
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Created.After(out.Tasks[j].Created) })
	out.Jobs = n.jobViewsLocked()
	writeJSON(w, out)
}

type packageView struct {
	marketplace.Manifest
	Source      string   `json:"source"`
	Verified    bool     `json:"verified"`
	Problem     string   `json:"problem,omitempty"`
	Compatible  bool     `json:"compatible"`
	Reason      string   `json:"reason,omitempty"`
	Permissions []string `json:"permissions"`
}

func (n *Node) handleCatalog(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	var machines []marketplace.Machine
	for _, a := range n.agents {
		if n.online(a, now) {
			machines = append(machines, marketplace.Machine{
				OS: a.Resources.OS, Containers: a.Resources.HasContainers(), Runtimes: a.Resources.Runtimes,
				Owner: a.Trust == protocol.TrustOwner && a.Resources.HasRuntime("exec"),
			})
		}
	}
	var out []packageView
	for _, p := range n.catalog.Sorted() {
		ok, why := p.Compatible(machines)
		m := p.Manifest
		m.Signature = ""
		out = append(out, packageView{Manifest: m, Source: p.Source, Verified: p.Verified, Problem: p.Problem, Compatible: ok, Reason: why, Permissions: p.Permissions()})
	}
	writeJSON(w, out)
}

func (n *Node) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Package string            `json:"package"`
		Inputs  map[string]string `json:"inputs"`
	}
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	t, err := n.Deploy(req.Package, req.Inputs)
	if err != nil {
		replyErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": t.ID})
}

type snapshotView struct {
	Root    string    `json:"root"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
	Files   int       `json:"files"`
	Machine string    `json:"machine"`
}

func (n *Node) handleStorage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	type machine struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
		Owner  bool   `json:"owner"`
		Quota  int64  `json:"quota"`
		Used   int64  `json:"used"`
		Chunks int    `json:"chunks"`
	}
	type backup struct {
		*Backup
		Machine   string         `json:"machine"`
		Snapshots []snapshotView `json:"snapshots"`
	}
	type restore struct {
		*Restore
		Machine string `json:"machine"`
	}
	var out struct {
		Health   PoolHealth `json:"health"`
		Copies   int        `json:"copies"`
		Machines []machine  `json:"machines"`
		Backups  []backup   `json:"backups"`
		Restores []restore  `json:"restores"`
		AppData  int64      `json:"appData"`
		Backed   int64      `json:"backedUp"`
	}
	out.Health, out.Copies = n.pool.health, n.pool.copies
	out.Machines, out.Backups, out.Restores = []machine{}, []backup{}, []restore{}
	for _, a := range n.agents {
		out.Machines = append(out.Machines, machine{a.ID, a.Name, n.online(a, now), a.Trust == protocol.TrustOwner, a.Storage.QuotaBytes, a.Storage.UsedBytes, a.Storage.Chunks})
	}
	sort.Slice(out.Machines, func(i, j int) bool { return out.Machines[i].Name < out.Machines[j].Name })
	for _, b := range n.pool.backups {
		bv := backup{Backup: b, Snapshots: []snapshotView{}}
		if a := n.agents[b.AgentID]; a != nil {
			bv.Machine = a.Name
		}
		for _, s := range n.pool.snapshots {
			if s.Owner == "backup:"+b.ID {
				bv.Snapshots = append(bv.Snapshots, snapshotView{s.Root, s.Created, s.Size, s.Files, s.Machine})
			}
		}
		sort.Slice(bv.Snapshots, func(i, j int) bool { return bv.Snapshots[i].Created.After(bv.Snapshots[j].Created) })
		if len(bv.Snapshots) > 0 {
			out.Backed += bv.Snapshots[0].Size
		}
		out.Backups = append(out.Backups, bv)
	}
	sort.Slice(out.Backups, func(i, j int) bool { return out.Backups[i].Created.Before(out.Backups[j].Created) })
	for _, t := range n.tasks {
		if s := n.pool.snapshots[t.Snapshot]; s != nil {
			out.AppData += s.Size
		}
	}
	for _, rs := range n.pool.restores {
		rv := restore{Restore: rs}
		if a := n.agents[rs.AgentID]; a != nil {
			rv.Machine = a.Name
		}
		out.Restores = append(out.Restores, rv)
	}
	sort.Slice(out.Restores, func(i, j int) bool { return out.Restores[i].Created.After(out.Restores[j].Created) })
	if len(out.Restores) > 10 {
		out.Restores = out.Restores[:10]
	}
	writeJSON(w, out)
}

// screenView tells the dashboard where to open a VM's screen.
type screenView struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

// HasOnlineMachine reports whether a machine with this name is online
// (used by tests outside this package).
func (n *Node) HasOnlineMachine(name string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		if a.Name == name && n.online(a, time.Now()) {
			return true
		}
	}
	return false
}
