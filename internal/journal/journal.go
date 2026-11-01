// Package journal is a crash-safe, append-only write-ahead log.
//
// It is the "fast save" behind sys1: every record is framed as
//
//	[length uint32][crc32c uint32][payload]
//
// and fsync'd before Append returns, so a record that Append acknowledged
// survives a crash or a power cut. A record torn by a power cut mid-write
// fails its checksum and is cut off on the next Open, leaving every record
// before it intact.
package journal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// MaxRecord is the largest payload a single record may carry.
const MaxRecord = 64 << 20

var (
	ErrTooLarge = errors.New("journal: record too large")
	ErrClosed   = errors.New("journal: closed")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Journal is safe for concurrent use.
type Journal struct {
	mu   sync.Mutex
	path string
	f    *os.File
	n    int
}

// Open opens or creates the journal at path and calls replay once for every
// intact record, in the order they were written. Anything after the first
// damaged record is truncated away. A replay error aborts Open.
func Open(path string, replay func([]byte) error) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	good, n, err := scan(f, replay)
	if err == nil {
		err = f.Truncate(good)
	}
	if err == nil {
		_, err = f.Seek(good, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Journal{path: path, f: f, n: n}, nil
}

// scan returns the byte offset just past the last intact record.
func scan(r io.Reader, replay func([]byte) error) (good int64, n int, err error) {
	br := bufio.NewReader(r)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return good, n, nil
		}
		size := binary.LittleEndian.Uint32(hdr[0:4])
		sum := binary.LittleEndian.Uint32(hdr[4:8])
		if size > MaxRecord {
			return good, n, nil
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(br, buf); err != nil {
			return good, n, nil
		}
		if crc32.Checksum(buf, castagnoli) != sum {
			return good, n, nil
		}
		if replay != nil {
			if err := replay(buf); err != nil {
				return 0, 0, fmt.Errorf("journal: replay record %d: %w", n, err)
			}
		}
		good += 8 + int64(size)
		n++
	}
}

func frame(p []byte) []byte {
	buf := make([]byte, 8+len(p))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(p)))
	binary.LittleEndian.PutUint32(buf[4:8], crc32.Checksum(p, castagnoli))
	copy(buf[8:], p)
	return buf
}

// Append durably writes one record. When it returns nil the record is on
// stable storage.
func (j *Journal) Append(p []byte) error {
	if len(p) > MaxRecord {
		return ErrTooLarge
	}
	buf := frame(p)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return ErrClosed
	}
	if _, err := j.f.Write(buf); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.n++
	return nil
}

// AppendMany durably writes several records with a single fsync. Either
// all of them survive a crash or, if it strikes mid-write, those before
// the torn one do.
func (j *Journal) AppendMany(ps [][]byte) error {
	var buf []byte
	for _, p := range ps {
		if len(p) > MaxRecord {
			return ErrTooLarge
		}
		buf = append(buf, frame(p)...)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return ErrClosed
	}
	if _, err := j.f.Write(buf); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.n += len(ps)
	return nil
}

// Len reports how many records the journal holds.
func (j *Journal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.n
}

// Compact atomically replaces the journal's contents with records,
// typically a snapshot of current state. A crash at any point leaves either
// the old journal or the new one, never a mix.
func (j *Journal) Compact(records [][]byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return ErrClosed
	}
	for _, r := range records {
		if len(r) > MaxRecord {
			return ErrTooLarge
		}
	}
	tmp := j.path + ".tmp"
	if err := writeFile(tmp, records); err != nil {
		os.Remove(tmp)
		return err
	}
	// Windows cannot rename over a file that is still open.
	j.f.Close()
	j.f = nil
	renameErr := os.Rename(tmp, j.path)
	f, err := os.OpenFile(j.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return err
	}
	j.f = f
	if renameErr != nil {
		os.Remove(tmp)
		return renameErr
	}
	syncDir(filepath.Dir(j.path))
	j.n = len(records)
	return nil
}

func writeFile(path string, records [][]byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range records {
		if _, err := w.Write(frame(r)); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir makes a rename durable. Not every OS supports syncing a
// directory (Windows does not), so errors are ignored.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Close releases the file. Further calls fail with ErrClosed.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}
