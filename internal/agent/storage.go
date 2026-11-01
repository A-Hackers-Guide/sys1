package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/storage"
	"github.com/shirou/gopsutil/v4/disk"
)

// Pooled storage, client side: holding sealed chunks for the pool (this
// machine never has the keys to read them), and producing and restoring
// snapshots of app volumes and backed-up folders.

// diskReserve is always left free, whatever the owner's share says.
const diskReserve = 5 << 30

type agentStorage struct {
	a     *Agent
	store *storage.Store
	spool *storage.Store // chunks waiting to be uploaded
	share int64          // what the owner agreed to lend

	mu       sync.Mutex
	stored   []string
	missing  []string
	running  map[string]bool // chunk work in flight, "fetch id" / "upload id"
	backups  map[string]*backupState
	restores map[string]bool
	sem      chan struct{}
}

type backupState struct {
	job      protocol.BackupJob
	lastRun  time.Time
	seenRun  int
	running  bool
	lastRoot string
}

func newAgentStorage(a *Agent) (*agentStorage, error) {
	s := &agentStorage{a: a, running: map[string]bool{}, backups: map[string]*backupState{}, restores: map[string]bool{}, sem: make(chan struct{}, 4)}
	var err error
	if s.store, err = storage.OpenStore(filepath.Join(a.dataDir, "chunks")); err != nil {
		return nil, err
	}
	if s.spool, err = storage.OpenStore(filepath.Join(a.dataDir, "spool")); err != nil {
		return nil, err
	}
	s.share = int64(a.cfg.StorageGB) << 30
	s.loadBackupTimes()
	return s, nil
}

// report is this machine's storage line for the heartbeat. The quota never
// lets pool data push the disk below diskReserve.
func (s *agentStorage) report() protocol.StorageReport {
	used, n := s.store.Usage()
	quota := s.share
	if d, err := disk.Usage(s.a.dataDir); err == nil {
		quota = min(quota, used+int64(d.Free)-diskReserve)
	}
	if quota < 0 {
		quota = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := protocol.StorageReport{QuotaBytes: quota, UsedBytes: used, Chunks: n, Stored: s.stored, Missing: s.missing}
	s.stored, s.missing = nil, nil
	return r
}

// apply starts the storage work the node handed out in a heartbeat.
func (s *agentStorage) apply(ctx context.Context, resp protocol.HeartbeatResponse) {
	for _, id := range resp.Drop {
		s.store.Delete(id)
	}
	for _, id := range resp.Fetch {
		s.spawn(ctx, "fetch "+id, func() { s.fetch(ctx, id) })
	}
	for _, id := range resp.Upload {
		s.spawn(ctx, "upload "+id, func() { s.upload(ctx, id) })
	}
	if resp.Inventory {
		go s.a.c.post(ctx, "/api/v1/inventory", protocol.Inventory{Chunks: s.store.List()}, nil)
	}
	s.syncBackups(ctx, resp.Backups)
	for _, job := range resp.Restores {
		s.mu.Lock()
		started := s.restores[job.ID]
		s.restores[job.ID] = true
		s.mu.Unlock()
		if !started {
			go s.restore(ctx, job)
		}
	}
}

func (s *agentStorage) spawn(ctx context.Context, key string, f func()) {
	s.mu.Lock()
	if s.running[key] {
		s.mu.Unlock()
		return
	}
	s.running[key] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, key)
			s.mu.Unlock()
		}()
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.sem }()
		f()
	}()
}

// fetch downloads a chunk from the node and keeps it.
func (s *agentStorage) fetch(ctx context.Context, id string) {
	if s.store.Has(id) {
		s.note(&s.stored, id)
		return
	}
	b, err := s.getRemote(ctx, id, 2*time.Minute)
	if err != nil {
		return // the node notices and asks again or elsewhere
	}
	if s.store.Put(id, b) == nil {
		s.note(&s.stored, id)
	}
}

// upload sends a held chunk to the node so it can be copied elsewhere.
func (s *agentStorage) upload(ctx context.Context, id string) {
	b, err := s.store.Get(id)
	if err != nil {
		s.note(&s.missing, id)
		return
	}
	s.putRemote(ctx, id, b)
}

func (s *agentStorage) note(list *[]string, id string) {
	s.mu.Lock()
	*list = append(*list, id)
	s.mu.Unlock()
}

// getRemote fetches a chunk through the node, waiting while it is brought
// in from a machine that holds it.
func (s *agentStorage) getRemote(ctx context.Context, id string, patience time.Duration) ([]byte, error) {
	deadline := time.Now().Add(patience)
	wait := 500 * time.Millisecond
	for {
		b, err := s.a.c.raw(ctx, http.MethodGet, "/api/v1/chunks/"+id, nil, storage.MaxSealed)
		if err == nil {
			if storage.ID(b) != id {
				return nil, storage.ErrCorrupt
			}
			return b, nil
		}
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("chunk %s is not available: %w", id[:12], err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("chunk %s: %w", id[:12], err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Second)
	}
}

// putRemote uploads a chunk to the node's staging, retrying while the
// node is busy or full.
func (s *agentStorage) putRemote(ctx context.Context, id string, b []byte) error {
	wait := time.Second
	for tries := 0; ; tries++ {
		_, err := s.a.c.raw(ctx, http.MethodPut, "/api/v1/chunks/"+id, b, 1<<10)
		if err == nil {
			return nil
		}
		var se *StatusError
		if errors.As(err, &se) && se.Code < 500 && se.Code != http.StatusTooManyRequests {
			return err
		}
		if tries == 8 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// getter reads chunks for a restore: from this machine first, then the pool.
func (s *agentStorage) getter(ctx context.Context) storage.Getter {
	return func(id string) ([]byte, error) {
		if b, err := s.store.Get(id); err == nil {
			return b, nil
		}
		return s.getRemote(ctx, id, 10*time.Minute)
	}
}

// snapshot takes a snapshot of dir and gets it into the pool: new chunks
// are spooled locally, uploaded, and then the snapshot is reported. It
// returns the new root, or prevRoot if nothing changed.
func (s *agentStorage) snapshot(ctx context.Context, dir, owner string, attempt int, key []byte, indexPath, prevRoot string, freeze func() func()) (string, error) {
	prev := storage.LoadIndex(indexPath)
	if prevRoot == "" {
		prev = storage.Index{} // nothing reported yet: upload everything
	}
	var spooled []string
	put := func(id string, sealed []byte) error {
		spooled = append(spooled, id)
		return s.spool.Put(id, sealed)
	}
	thaw := func() {}
	if freeze != nil {
		thaw = freeze()
	}
	res, err := storage.Take(dir, key, prev, put)
	thaw()
	defer func() {
		for _, id := range spooled {
			s.spool.Delete(id)
		}
	}()
	if err != nil {
		return prevRoot, err
	}
	if !res.Changed && prevRoot != "" {
		return prevRoot, nil
	}
	for _, id := range spooled {
		b, err := s.spool.Get(id)
		if err != nil {
			return prevRoot, err
		}
		if err := s.putRemote(ctx, id, b); err != nil {
			return prevRoot, fmt.Errorf("uploading snapshot: %w", err)
		}
	}
	rep := protocol.SnapshotReport{Owner: owner, Attempt: attempt, Root: res.Root, Chunks: res.Chunks, Size: res.Size, Files: res.Files, ContentHash: res.ContentHash}
	if err := s.a.c.post(ctx, "/api/v1/snapshot", rep, nil); err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusConflict {
			// Some reused chunk is gone from the pool: forget the index so
			// the next snapshot uploads everything again.
			os.Remove(indexPath)
		}
		return prevRoot, err
	}
	// The snapshot is safely in the pool now. Failing to save the index
	// only costs re-reading files next time, so it is not an error.
	os.MkdirAll(filepath.Dir(indexPath), 0o700)
	if err := storage.SaveIndex(indexPath, res.Index); err != nil {
		s.a.log.Warn("could not save snapshot index", "err", err)
	}
	return res.Root, nil
}

// ---- backups ----

func (s *agentStorage) backupDir(id string) string {
	return filepath.Join(s.a.dataDir, "backups", id)
}

func (s *agentStorage) loadBackupTimes() {
	entries, _ := os.ReadDir(filepath.Join(s.a.dataDir, "backups"))
	for _, e := range entries {
		var st struct {
			LastRun  time.Time `json:"lastRun"`
			SeenRun  int       `json:"seenRun"`
			LastRoot string    `json:"lastRoot"`
		}
		if b, err := os.ReadFile(filepath.Join(s.backupDir(e.Name()), "state.json")); err == nil && json.Unmarshal(b, &st) == nil {
			s.backups[e.Name()] = &backupState{lastRun: st.LastRun, seenRun: st.SeenRun, lastRoot: st.LastRoot}
		}
	}
}

func (s *agentStorage) saveBackupState(id string, b *backupState) {
	os.MkdirAll(s.backupDir(id), 0o700)
	data, _ := json.Marshal(map[string]any{"lastRun": b.lastRun, "seenRun": b.seenRun, "lastRoot": b.lastRoot})
	os.WriteFile(filepath.Join(s.backupDir(id), "state.json"), data, 0o600)
}

// syncBackups starts any backup that is due. Only owner machines back up
// their folders: on anyone else's machine the node could otherwise read
// whatever it liked.
func (s *agentStorage) syncBackups(ctx context.Context, jobs []protocol.BackupJob) {
	if s.a.cfg.Trust != protocol.TrustOwner {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range jobs {
		b := s.backups[job.ID]
		if b == nil {
			b = &backupState{seenRun: job.RunNow}
			s.backups[job.ID] = b
		}
		b.job = job
		due := b.lastRun.IsZero() || now.Sub(b.lastRun) >= time.Duration(job.IntervalSeconds)*time.Second || job.RunNow != b.seenRun
		if !due || b.running {
			continue
		}
		b.running = true
		b.seenRun = job.RunNow
		go s.runBackup(ctx, b)
	}
}

func (s *agentStorage) runBackup(ctx context.Context, b *backupState) {
	job := b.job
	os.MkdirAll(s.backupDir(job.ID), 0o700)
	root, err := s.snapshot(ctx, job.Path, "backup:"+job.ID, 0, job.Key, filepath.Join(s.backupDir(job.ID), "index.json"), b.lastRoot, nil)
	s.mu.Lock()
	b.running = false
	if err == nil {
		b.lastRun, b.lastRoot = time.Now(), root
	} else {
		s.a.log.Warn("backup failed; will retry", "path", job.Path, "err", err)
		b.lastRun = time.Now().Add(-time.Duration(job.IntervalSeconds)*time.Second + time.Minute) // retry in a minute
	}
	s.saveBackupState(job.ID, b)
	s.mu.Unlock()
	if err == nil {
		s.a.log.Info("backup done", "path", job.Path, "snapshot", root[:12])
	}
}

// restore writes a snapshot into a new, empty folder on this machine.
func (s *agentStorage) restore(ctx context.Context, job protocol.RestoreJob) {
	res := protocol.RestoreResult{ID: job.ID}
	switch {
	case s.a.cfg.Trust != protocol.TrustOwner:
		res.Message = "restores are only allowed on the owner's own machines"
	case !filepath.IsAbs(job.Dest):
		res.Message = "the destination must be a full path"
	default:
		if entries, err := os.ReadDir(job.Dest); err == nil && len(entries) > 0 {
			res.Message = "the destination folder already has files in it; pick a new folder"
			break
		}
		idx, err := storage.Restore(job.Dest, job.Root, job.Key, s.getter(ctx), false)
		if err != nil {
			res.Message = err.Error()
			break
		}
		res.OK, res.Files = true, len(idx)
	}
	for tries := 0; tries < 20; tries++ {
		if s.a.c.post(ctx, "/api/v1/restore-result", res, nil) == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
