package node

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/A-Hackers-Guide/sys1/internal/journal"
	"github.com/hashicorp/raft"
)

// raftStore keeps the Raft log and Raft's own settings in a crash-safe
// journal (see internal/journal), with the live log in memory. It builds
// on every platform sys1 supports, unlike the bolt-based stores. Raft's
// snapshots keep the log short, and the journal is rewritten when most of
// it is history.
type raftStore struct {
	mu    sync.RWMutex
	j     *journal.Journal
	logs  map[uint64]*raft.Log
	first uint64
	last  uint64
	conf  map[string][]byte
	dead  int // journal records no longer live
}

type raftRecord struct {
	Log      *raft.Log `json:"log,omitempty"`
	DelFrom  uint64    `json:"delFrom,omitempty"`
	DelTo    uint64    `json:"delTo,omitempty"`
	Key      string    `json:"key,omitempty"`
	Val      []byte    `json:"val,omitempty"`
	Snapshot bool      `json:"snapshot,omitempty"` // marks a rewritten journal
}

var errNotKey = errors.New("not found")

func openRaftStore(path string) (*raftStore, error) {
	s := &raftStore{logs: map[uint64]*raft.Log{}, conf: map[string][]byte{}}
	j, err := journal.Open(path, func(b []byte) error {
		var r raftRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		s.apply(r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.j = j
	return s, nil
}

func (s *raftStore) apply(r raftRecord) {
	switch {
	case r.Log != nil:
		s.logs[r.Log.Index] = r.Log
		if s.first == 0 || r.Log.Index < s.first {
			s.first = r.Log.Index
		}
		if r.Log.Index > s.last {
			s.last = r.Log.Index
		}
	case r.DelTo != 0:
		for i := r.DelFrom; i <= r.DelTo; i++ {
			if _, ok := s.logs[i]; ok {
				delete(s.logs, i)
				s.dead++
			}
		}
		s.first, s.last = 0, 0
		for i := range s.logs {
			if s.first == 0 || i < s.first {
				s.first = i
			}
			if i > s.last {
				s.last = i
			}
		}
	case r.Key != "":
		if _, ok := s.conf[r.Key]; ok {
			s.dead++
		}
		s.conf[r.Key] = r.Val
	}
}

func (s *raftStore) write(rs ...raftRecord) error {
	var bs [][]byte
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		bs = append(bs, b)
	}
	if err := s.j.AppendMany(bs); err != nil {
		return err
	}
	for _, r := range rs {
		s.apply(r)
	}
	if s.dead > 4096 && s.dead > 2*len(s.logs) {
		s.rewrite()
	}
	return nil
}

// rewrite replaces the journal with just the live records.
func (s *raftStore) rewrite() {
	var bs [][]byte
	for k, v := range s.conf {
		b, _ := json.Marshal(raftRecord{Key: k, Val: v})
		bs = append(bs, b)
	}
	for i := s.first; i <= s.last && i != 0; i++ {
		if l := s.logs[i]; l != nil {
			b, _ := json.Marshal(raftRecord{Log: l})
			bs = append(bs, b)
		}
	}
	if s.j.Compact(bs) == nil {
		s.dead = 0
	}
}

func (s *raftStore) Close() error { return s.j.Close() }

func (s *raftStore) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.first, nil
}

func (s *raftStore) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last, nil
}

func (s *raftStore) GetLog(index uint64, log *raft.Log) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.logs[index]
	if l == nil {
		return raft.ErrLogNotFound
	}
	*log = *l
	return nil
}

func (s *raftStore) StoreLog(log *raft.Log) error { return s.StoreLogs([]*raft.Log{log}) }

func (s *raftStore) StoreLogs(logs []*raft.Log) error {
	rs := make([]raftRecord, len(logs))
	for i, l := range logs {
		c := *l
		rs[i] = raftRecord{Log: &c}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(rs...)
}

func (s *raftStore) DeleteRange(min, max uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(raftRecord{DelFrom: min, DelTo: max})
}

func (s *raftStore) Set(key, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(raftRecord{Key: string(key), Val: append([]byte(nil), val...)})
}

// Get returns an error when key is missing, as raft.StableStore requires.
func (s *raftStore) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.conf[string(key)]
	if !ok {
		return nil, errNotKey
	}
	return append([]byte(nil), v...), nil
}

func (s *raftStore) SetUint64(key []byte, val uint64) error {
	b, _ := json.Marshal(val)
	return s.Set(key, b)
}

func (s *raftStore) GetUint64(key []byte) (uint64, error) {
	v, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	var n uint64
	return n, json.Unmarshal(v, &n)
}
