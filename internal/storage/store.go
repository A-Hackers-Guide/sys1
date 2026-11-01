package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store keeps sealed chunks on local disk as <dir>/<first two hex>/<id>.
// Writes are atomic and fsync'd, and every read is checked against the ID,
// so a damaged chunk is reported instead of served.
type Store struct {
	dir string

	mu    sync.Mutex
	used  int64
	count int
}

// OpenStore opens (or creates) a store and counts what it already holds.
// Half-written temporary files left by a crash are removed.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".tmp") {
			os.Remove(path)
			return nil
		}
		if fi, err := d.Info(); err == nil && ValidID(d.Name()) {
			s.used += fi.Size()
			s.count++
		}
		return nil
	})
	return s, err
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id[:2], id) }

// Put stores a sealed chunk after checking it matches its ID.
func (s *Store) Put(id string, sealed []byte) error {
	if !ValidID(id) {
		return errors.New("storage: invalid chunk id")
	}
	if ID(sealed) != id {
		return ErrCorrupt
	}
	p := s.path(id)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(sealed); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.used += int64(len(sealed))
	s.count++
	s.mu.Unlock()
	return nil
}

// Get returns a sealed chunk. A chunk that fails its check is deleted and
// reported as ErrCorrupt, so the pool can repair it from another copy.
func (s *Store) Get(id string) ([]byte, error) {
	if !ValidID(id) {
		return nil, errors.New("storage: invalid chunk id")
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	if ID(b) != id {
		s.Delete(id)
		return nil, ErrCorrupt
	}
	return b, nil
}

func (s *Store) Has(id string) bool {
	if !ValidID(id) {
		return false
	}
	_, err := os.Stat(s.path(id))
	return err == nil
}

func (s *Store) Delete(id string) {
	if !ValidID(id) {
		return
	}
	p := s.path(id)
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	if os.Remove(p) == nil {
		s.mu.Lock()
		s.used -= fi.Size()
		s.count--
		s.mu.Unlock()
	}
}

// List returns every chunk ID held.
func (s *Store) List() []string {
	var ids []string
	filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && ValidID(d.Name()) {
			ids = append(ids, d.Name())
		}
		return nil
	})
	return ids
}

// Usage reports bytes and chunks held.
func (s *Store) Usage() (bytes int64, chunks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.count
}
