package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/storage"
)

// Pooled storage, node side.
//
// Machines hold sealed chunks; the node never needs to read them. It keeps
// the bookkeeping: which snapshots exist, which chunks each one needs, how
// many copies each chunk should have, and which machines hold them. A
// maintenance pass then moves the pool toward that: chunks pass through
// the node's staging folder on their way from one machine to another,
// missing copies are re-made from surviving ones, and chunks nothing needs
// any more are deleted. See docs/STORAGE.md.

const (
	// A holder that has been offline this long no longer counts as a copy.
	holderGrace = 10 * time.Minute
	// Storage work handed to a machine is retried after this long.
	inflightTimeout = 3 * time.Minute
	// A chunk someone is waiting to read stays staged this long.
	demandWindow = 2 * time.Minute
	// Most chunk commands per kind in one heartbeat.
	batchSize = 64

	keepTaskSnapshots = 3
)

// Repairs wait this long after the node starts, so machines can report
// what they hold before anything is judged missing. Tests shorten it.
var repairWarmup = time.Minute

type Snapshot struct {
	Root    string    `json:"root"`
	Owner   string    `json:"owner"` // "task:<id>" or "backup:<id>"
	Chunks  []string  `json:"chunks"`
	Size    int64     `json:"size"`
	Files   int       `json:"files"`
	Created time.Time `json:"created"`
	Machine string    `json:"machine"`
}

type Backup struct {
	ID              string    `json:"id"`
	AgentID         string    `json:"agentId"`
	Path            string    `json:"path"`
	Copies          int       `json:"copies"`
	IntervalMinutes int       `json:"intervalMinutes"`
	Keep            int       `json:"keep"`
	RunNow          int       `json:"runNow"`
	Created         time.Time `json:"created"`
}

type Restore struct {
	ID      string    `json:"id"`
	Root    string    `json:"root"`
	AgentID string    `json:"agentId"`
	Dest    string    `json:"dest"`
	State   string    `json:"state"` // pending, done, failed
	Message string    `json:"message,omitempty"`
	Files   int       `json:"files,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type stagedChunk struct {
	size int64
	at   time.Time
}

type agentWork struct {
	fetch, upload, drop map[string]bool
}

type pool struct {
	key         []byte
	copies      int
	stagingDir  string
	stagingCap  int64
	staged      map[string]stagedChunk
	stagedBytes int64

	snapshots map[string]*Snapshot
	backups   map[string]*Backup
	restores  map[string]*Restore

	held        map[string]map[string]bool // agent ID -> chunk IDs it holds
	inventoried map[string]bool
	asked       map[string]time.Time // inventory requests
	queue       map[string]*agentWork
	inflight    map[string]time.Time // "agent kind id"
	demand      map[string]time.Time // chunk -> last time someone wanted to read it
	health      PoolHealth
}

// PoolHealth summarizes durability for the dashboard.
type PoolHealth struct {
	Chunks      int   `json:"chunks"`
	Healthy     int   `json:"healthy"`     // at least the wanted number of copies
	Degraded    int   `json:"degraded"`    // fewer copies than wanted, but some
	Unavailable int   `json:"unavailable"` // no copy reachable right now
	StoredBytes int64 `json:"storedBytes"` // across all machines, every copy
	Staged      int64 `json:"stagedBytes"`
}

func (n *Node) initPool(cfg Config) error {
	p := &pool{
		copies: cfg.Copies, stagingDir: filepath.Join(cfg.DataDir, "staging"), stagingCap: cfg.StagingBytes,
		staged: map[string]stagedChunk{}, snapshots: map[string]*Snapshot{}, backups: map[string]*Backup{},
		restores: map[string]*Restore{}, held: map[string]map[string]bool{}, inventoried: map[string]bool{},
		asked: map[string]time.Time{}, queue: map[string]*agentWork{}, inflight: map[string]time.Time{}, demand: map[string]time.Time{},
	}
	if p.copies <= 0 {
		p.copies = 3
	}
	if p.stagingCap <= 0 {
		p.stagingCap = 4 << 30
	}
	keyPath := filepath.Join(cfg.DataDir, "pool.key")
	if b, err := os.ReadFile(keyPath); err == nil && len(b) == 32 {
		p.key = b
	} else {
		p.key = storage.NewPoolKey()
		if err := os.WriteFile(keyPath, p.key, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(p.stagingDir, 0o700); err != nil {
		return err
	}
	entries, _ := os.ReadDir(p.stagingDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(p.stagingDir, e.Name()))
			continue
		}
		if fi, err := e.Info(); err == nil && storage.ValidID(e.Name()) {
			p.staged[e.Name()] = stagedChunk{size: fi.Size(), at: time.Now()}
			p.stagedBytes += fi.Size()
		}
	}
	n.pool = p
	return nil
}

func (n *Node) dataKey(owner string) []byte { return storage.DeriveKey(n.pool.key, owner) }

func (n *Node) work(agentID string) *agentWork {
	w := n.pool.queue[agentID]
	if w == nil {
		w = &agentWork{fetch: map[string]bool{}, upload: map[string]bool{}, drop: map[string]bool{}}
		n.pool.queue[agentID] = w
	}
	return w
}

func inflightKey(agent, kind, id string) string { return agent + " " + kind + " " + id }

func (n *Node) busy(agent, kind, id string, now time.Time) bool {
	t, ok := n.pool.inflight[inflightKey(agent, kind, id)]
	return ok && now.Sub(t) < inflightTimeout
}

func (n *Node) copiesFor(owner string) int {
	if id, ok := strings.CutPrefix(owner, "backup:"); ok {
		if b := n.pool.backups[id]; b != nil && b.Copies > 0 {
			return b.Copies
		}
	}
	return n.pool.copies
}

func (n *Node) holderOK(agentID string, now time.Time) bool {
	a := n.agents[agentID]
	return a != nil && now.Sub(a.LastSeen) < holderGrace
}

// maintainPoolLocked moves the pool toward the wanted number of copies of
// every chunk, and cleans up what is no longer needed.
func (n *Node) maintainPoolLocked(now time.Time) {
	p := n.pool
	want := map[string]int{}
	for _, s := range p.snapshots {
		c := n.copiesFor(s.Owner)
		for _, id := range s.Chunks {
			want[id] = max(want[id], c)
		}
	}
	for k, t := range p.inflight {
		if now.Sub(t) >= inflightTimeout {
			delete(p.inflight, k)
		}
	}
	holders := map[string][]string{}
	for agent, set := range p.held {
		if !n.holderOK(agent, now) {
			continue
		}
		for id := range set {
			holders[id] = append(holders[id], agent)
		}
	}
	warm := now.Sub(n.started) >= repairWarmup

	var h PoolHealth
	for id, w := range want {
		hs := holders[id]
		h.Chunks++
		_, staged := p.staged[id]
		switch {
		case len(hs) >= w:
			h.Healthy++
		case len(hs) > 0:
			h.Degraded++
		case !staged:
			h.Unavailable++
		default:
			h.Degraded++
		}
		if len(hs) >= w {
			if staged && now.Sub(p.demand[id]) > demandWindow && now.Sub(p.staged[id].at) > 30*time.Second {
				n.unstageLocked(id)
			}
			if len(hs) > w+1 {
				n.trimLocked(id, hs, now)
			}
			continue
		}
		if !warm {
			continue
		}
		if staged {
			n.placeLocked(id, w-len(hs), hs, now)
		} else {
			n.requestUploadLocked(id, now)
		}
	}
	for id, at := range p.demand {
		if now.Sub(at) > demandWindow {
			delete(p.demand, id)
			continue
		}
		if _, staged := p.staged[id]; !staged {
			n.requestUploadLocked(id, now)
		}
	}
	// Chunks nothing needs any more: deleted everywhere.
	for agent, set := range p.held {
		for id := range set {
			if want[id] == 0 && !n.busy(agent, "drop", id, now) {
				n.work(agent).drop[id] = true
			}
		}
	}
	for id, st := range p.staged {
		if want[id] == 0 && now.Sub(st.at) > 15*time.Minute && now.Sub(p.demand[id]) > demandWindow {
			n.unstageLocked(id)
		}
	}
	for _, a := range n.agents {
		h.StoredBytes += a.Storage.UsedBytes
	}
	h.Staged = p.stagedBytes
	p.health = h
}

// placeLocked asks machines with free space to fetch a staged chunk.
func (n *Node) placeLocked(id string, need int, have []string, now time.Time) {
	type cand struct {
		id   string
		free int64
	}
	skip := map[string]bool{}
	for _, h := range have {
		skip[h] = true
	}
	var cs []cand
	for _, a := range n.agents {
		if skip[a.ID] || !n.online(a, now) || a.Storage.QuotaBytes <= 0 {
			continue
		}
		if n.busy(a.ID, "fetch", id, now) {
			need-- // already on its way there
			continue
		}
		pending := int64(len(n.work(a.ID).fetch)) * storage.MaxSealed
		free := a.Storage.QuotaBytes - a.Storage.UsedBytes - pending
		if free > storage.MaxSealed {
			cs = append(cs, cand{a.ID, free})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].free > cs[j].free })
	for i := 0; i < need && i < len(cs); i++ {
		n.work(cs[i].id).fetch[id] = true
	}
}

// requestUploadLocked asks one online holder to send a chunk to staging.
func (n *Node) requestUploadLocked(id string, now time.Time) {
	var src string
	for agent, set := range n.pool.held {
		if !set[id] {
			continue
		}
		if n.busy(agent, "upload", id, now) || n.work(agent).upload[id] {
			return
		}
		if a := n.agents[agent]; a != nil && n.online(a, now) && src == "" {
			src = agent
		}
	}
	if src != "" {
		n.work(src).upload[id] = true
	}
}

// trimLocked removes one surplus copy, from the fullest machine.
func (n *Node) trimLocked(id string, hs []string, now time.Time) {
	var victim string
	var least int64 = 1 << 62
	for _, h := range hs {
		a := n.agents[h]
		if a == nil || !n.online(a, now) || n.busy(h, "drop", id, now) {
			continue
		}
		if free := a.Storage.QuotaBytes - a.Storage.UsedBytes; free < least {
			victim, least = h, free
		}
	}
	if victim != "" {
		n.work(victim).drop[id] = true
	}
}

func (n *Node) unstageLocked(id string) {
	if st, ok := n.pool.staged[id]; ok {
		os.Remove(filepath.Join(n.pool.stagingDir, id))
		n.pool.stagedBytes -= st.size
		delete(n.pool.staged, id)
	}
}

// storageHeartbeatLocked applies a machine's storage report and hands it
// its share of storage work.
func (n *Node) storageHeartbeatLocked(a *Agent, rep protocol.StorageReport, resp *protocol.HeartbeatResponse, now time.Time) {
	p := n.pool
	a.Storage = protocol.StorageReport{QuotaBytes: rep.QuotaBytes, UsedBytes: rep.UsedBytes, Chunks: rep.Chunks}
	set := p.held[a.ID]
	if set == nil {
		set = map[string]bool{}
		p.held[a.ID] = set
	}
	for _, id := range rep.Stored {
		set[id] = true
		delete(p.inflight, inflightKey(a.ID, "fetch", id))
	}
	for _, id := range rep.Missing {
		delete(set, id)
		delete(p.inflight, inflightKey(a.ID, "upload", id))
	}
	if !p.inventoried[a.ID] && now.Sub(p.asked[a.ID]) > 30*time.Second {
		resp.Inventory = true
		p.asked[a.ID] = now
	}
	w := n.work(a.ID)
	take := func(m map[string]bool, kind string) []string {
		var out []string
		for id := range m {
			if len(out) == batchSize {
				break
			}
			out = append(out, id)
			delete(m, id)
			p.inflight[inflightKey(a.ID, kind, id)] = now
		}
		return out
	}
	resp.Fetch, resp.Upload, resp.Drop = take(w.fetch, "fetch"), take(w.upload, "upload"), take(w.drop, "drop")
	for _, id := range resp.Drop {
		delete(set, id)
	}
	for _, b := range p.backups {
		if b.AgentID == a.ID && a.Trust == protocol.TrustOwner {
			resp.Backups = append(resp.Backups, protocol.BackupJob{
				ID: b.ID, Path: b.Path, IntervalSeconds: b.IntervalMinutes * 60, Key: n.dataKey("backup:" + b.ID), RunNow: b.RunNow,
			})
		}
	}
	for _, r := range p.restores {
		if r.AgentID == a.ID && r.State == "pending" {
			if s := p.snapshots[r.Root]; s != nil {
				resp.Restores = append(resp.Restores, protocol.RestoreJob{ID: r.ID, Root: r.Root, Dest: r.Dest, Key: n.dataKey(s.Owner)})
			}
		}
	}
	resp.Hurry = len(resp.Fetch)+len(resp.Upload)+len(resp.Drop) > 0 ||
		len(w.fetch)+len(w.upload)+len(w.drop) > 0 || len(resp.Restores) > 0 || len(p.demand) > 0
}

// latestSnapshotLocked returns an owner's newest snapshot root, or "".
func (n *Node) latestSnapshotLocked(owner string) string {
	var best *Snapshot
	for _, s := range n.pool.snapshots {
		if s.Owner == owner && (best == nil || s.Created.After(best.Created)) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.Root
}

func (n *Node) deleteSnapshotsLocked(owner string, keep int) {
	var mine []*Snapshot
	for _, s := range n.pool.snapshots {
		if s.Owner == owner {
			mine = append(mine, s)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Created.After(mine[j].Created) })
	for i := keep; i < len(mine); i++ {
		delete(n.pool.snapshots, mine[i].Root)
		n.persistLocked(event{DelSnapshot: mine[i].Root})
	}
}

// ---- client API ----

func (n *Node) handleChunkPut(w http.ResponseWriter, r *http.Request, a *Agent) {
	id := r.PathValue("id")
	if !storage.ValidID(id) {
		http.Error(w, "bad chunk id", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, storage.MaxSealed))
	if err != nil {
		http.Error(w, "chunk too large", http.StatusRequestEntityTooLarge)
		return
	}
	if storage.ID(body) != id {
		http.Error(w, "chunk does not match its id", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	p := n.pool
	if _, ok := p.staged[id]; ok {
		delete(p.inflight, inflightKey(a.ID, "upload", id))
		n.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if p.stagedBytes+int64(len(body)) > p.stagingCap {
		n.mu.Unlock()
		w.Header().Set("Retry-After", "30")
		http.Error(w, "node staging is full; try again shortly", http.StatusInsufficientStorage)
		return
	}
	p.stagedBytes += int64(len(body)) // reserve before writing without the lock
	n.mu.Unlock()

	path := filepath.Join(p.stagingDir, id)
	err = writeAtomic(path, body)
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		p.stagedBytes -= int64(len(body))
		http.Error(w, "could not stage chunk", http.StatusInternalServerError)
		return
	}
	p.staged[id] = stagedChunk{size: int64(len(body)), at: time.Now()}
	delete(p.inflight, inflightKey(a.ID, "upload", id))
	w.WriteHeader(http.StatusOK)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// handleChunkGet serves a staged chunk. If it is not staged, the node asks
// a machine holding it to upload it and the caller is told to retry.
func (n *Node) handleChunkGet(w http.ResponseWriter, r *http.Request, a *Agent) {
	id := r.PathValue("id")
	if !storage.ValidID(id) {
		http.Error(w, "bad chunk id", http.StatusBadRequest)
		return
	}
	now := time.Now()
	n.mu.Lock()
	p := n.pool
	_, staged := p.staged[id]
	if staged {
		p.demand[id] = now
		n.mu.Unlock()
		http.ServeFile(w, r, filepath.Join(p.stagingDir, id))
		return
	}
	reachable := false
	for agent, set := range p.held {
		if set[id] {
			if h := n.agents[agent]; h != nil && n.online(h, now) {
				reachable = true
			}
		}
	}
	if !reachable {
		n.mu.Unlock()
		http.Error(w, "no machine holding this chunk is online", http.StatusNotFound)
		return
	}
	p.demand[id] = now
	n.requestUploadLocked(id, now)
	n.mu.Unlock()
	w.Header().Set("Retry-After", "1")
	http.Error(w, "on its way; retry", http.StatusServiceUnavailable)
}

func (n *Node) handleInventory(w http.ResponseWriter, r *http.Request, a *Agent) {
	var inv protocol.Inventory
	if !readJSON(w, r, 64<<20, &inv) {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	set := map[string]bool{}
	for _, id := range inv.Chunks {
		if storage.ValidID(id) {
			set[id] = true
		}
	}
	n.pool.held[a.ID] = set
	n.pool.inventoried[a.ID] = true
	w.WriteHeader(http.StatusOK)
}

func (n *Node) handleSnapshot(w http.ResponseWriter, r *http.Request, a *Agent) {
	var rep protocol.SnapshotReport
	if !readJSON(w, r, 64<<20, &rep) {
		return
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	kind, id, _ := strings.Cut(rep.Owner, ":")
	keep := keepTaskSnapshots
	var task *Task
	switch kind {
	case "task":
		task = n.tasks[id]
		if task == nil || task.AgentID != a.ID || task.Attempt != rep.Attempt || len(task.Spec.Volumes) == 0 {
			http.Error(w, "stale snapshot", http.StatusConflict)
			return
		}
	case "backup":
		b := n.pool.backups[id]
		if b == nil || b.AgentID != a.ID {
			http.Error(w, "unknown backup", http.StatusConflict)
			return
		}
		keep = max(b.Keep, 1)
	default:
		http.Error(w, "unknown owner", http.StatusBadRequest)
		return
	}
	hasRoot := false
	var missing []string
	for _, c := range rep.Chunks {
		if !storage.ValidID(c) {
			http.Error(w, "bad chunk id", http.StatusBadRequest)
			return
		}
		hasRoot = hasRoot || c == rep.Root
		if _, ok := n.pool.staged[c]; ok {
			continue
		}
		found := false
		for agent, set := range n.pool.held {
			if set[c] && n.holderOK(agent, now) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, c)
		}
	}
	if !hasRoot {
		http.Error(w, "snapshot root missing from chunk list", http.StatusBadRequest)
		return
	}
	if len(missing) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": "chunks missing from the pool", "missing": missing})
		return
	}
	s := &Snapshot{Root: rep.Root, Owner: rep.Owner, Chunks: rep.Chunks, Size: rep.Size, Files: rep.Files, Created: now, Machine: a.Name}
	n.pool.snapshots[s.Root] = s
	n.persistLocked(event{Snapshot: s})
	if task != nil {
		task.Snapshot, task.SnapshotAt = s.Root, now
		n.saveTaskLocked(task)
	}
	n.deleteSnapshotsLocked(rep.Owner, keep)
	w.WriteHeader(http.StatusOK)
}

func (n *Node) handleRestoreResult(w http.ResponseWriter, r *http.Request, a *Agent) {
	var res protocol.RestoreResult
	if !readJSON(w, r, 1<<20, &res) {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	rs := n.pool.restores[res.ID]
	if rs == nil || rs.AgentID != a.ID {
		http.Error(w, "unknown restore", http.StatusConflict)
		return
	}
	rs.State, rs.Message, rs.Files, rs.Updated = "done", res.Message, res.Files, time.Now()
	if !res.OK {
		rs.State = "failed"
	}
	n.persistLocked(event{Restore: rs})
	w.WriteHeader(http.StatusOK)
}

// ---- dashboard actions ----

func (n *Node) AddBackup(agentID, path string, copies, intervalMinutes, keep int) (*Backup, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.agents[agentID]
	if a == nil {
		return nil, errors.New("no such machine")
	}
	if a.Trust != protocol.TrustOwner {
		return nil, errors.New("backups are only for your own machines")
	}
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") && !(len(path) > 2 && path[1] == ':') {
		return nil, errors.New("use a full folder path, like /home/me/Photos or C:\\Users\\me\\Pictures")
	}
	if copies < 1 || copies > 10 {
		return nil, errors.New("copies must be between 1 and 10")
	}
	if intervalMinutes < 5 {
		intervalMinutes = 5
	}
	if keep < 1 {
		keep = 7
	}
	b := &Backup{ID: randomToken()[:12], AgentID: agentID, Path: path, Copies: copies, IntervalMinutes: intervalMinutes, Keep: keep, Created: time.Now()}
	n.pool.backups[b.ID] = b
	n.persistLocked(event{Backup: b})
	return b, nil
}

func (n *Node) RunBackup(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.pool.backups[id]
	if b == nil {
		return errors.New("no such backup")
	}
	b.RunNow++
	n.persistLocked(event{Backup: b})
	return nil
}

// RemoveBackup stops a backup and deletes its snapshots from the pool.
func (n *Node) RemoveBackup(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pool.backups[id] == nil {
		return errors.New("no such backup")
	}
	n.deleteSnapshotsLocked("backup:"+id, 0)
	delete(n.pool.backups, id)
	n.persistLocked(event{DelBackup: id})
	return nil
}

// StartRestore restores a snapshot into a new folder on one of your
// machines.
func (n *Node) StartRestore(root, agentID, dest string) (*Restore, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pool.snapshots[root] == nil {
		return nil, errors.New("no such snapshot")
	}
	a := n.agents[agentID]
	if a == nil || a.Trust != protocol.TrustOwner {
		return nil, errors.New("restores go to your own machines only")
	}
	if dest == "" || !(strings.HasPrefix(dest, "/") || (len(dest) > 2 && dest[1] == ':')) {
		return nil, errors.New("use a full path for the new folder")
	}
	rs := &Restore{ID: randomToken()[:12], Root: root, AgentID: agentID, Dest: dest, State: "pending", Created: time.Now(), Updated: time.Now()}
	n.pool.restores[rs.ID] = rs
	n.persistLocked(event{Restore: rs})
	return rs, nil
}

// RecoveryKey is the pool key in hex. Without it, nothing in the pool can
// be read, so it belongs somewhere safe outside the node.
func (n *Node) RecoveryKey() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return hex.EncodeToString(n.pool.key)
}
