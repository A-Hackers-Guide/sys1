package storage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A snapshot is a folder turned into chunks. Each file's contents become
// sealed chunks; a manifest (paths, modes, times, chunk lists) is gzipped,
// split and sealed too; and one small sealed root chunk points at the
// manifest. The root's ID names the snapshot. Without the data key, a
// snapshot reveals only how many chunks it has and their sizes.

// Entry is one path in a snapshot.
type Entry struct {
	Path   string   `json:"p"`           // slash-separated, relative to the snapshot root
	Type   string   `json:"t"`           // "f" file, "d" directory, "l" symlink
	Mode   uint32   `json:"m"`           // permission bits
	Size   int64    `json:"s,omitempty"` // files
	MTime  int64    `json:"mt"`          // unix nanoseconds
	Chunks []string `json:"c,omitempty"` // files
	Target string   `json:"l,omitempty"` // symlinks
	Owner  []int    `json:"o,omitempty"` // uid, gid (Unix)
	Hash   string   `json:"h,omitempty"` // files: SHA-256 of the plain contents
}

// Index remembers the entries of the last snapshot so unchanged files
// reuse their chunks instead of being read and uploaded again.
type Index map[string]Entry

type manifest struct {
	Version int     `json:"v"`
	Entries []Entry `json:"e"`
}

type rootDoc struct {
	Version int      `json:"v"`
	Parts   []string `json:"parts"`
	Size    int64    `json:"size"`
	Files   int      `json:"files"`
	Created int64    `json:"created"`
}

// Result describes a snapshot just taken.
type Result struct {
	Root    string   // the snapshot's ID
	Chunks  []string // every chunk it needs, including manifest and root
	Size    int64    // total file bytes
	Files   int
	Index   Index
	Changed bool // anything differs from the previous index
	// ContentHash fingerprints what the folder holds: paths, types,
	// permissions, file contents and link targets, but not times or
	// owners. Two machines that produce the same output get the same hash,
	// even though their encrypted chunks differ.
	ContentHash string
}

// Putter stores one sealed chunk somewhere durable.
type Putter func(id string, sealed []byte) error

// Getter fetches one sealed chunk.
type Getter func(id string) ([]byte, error)

// Take snapshots the folder at dir. Files whose size, time and mode match
// prev keep their old chunks. Sockets, devices and pipes are skipped.
func Take(dir string, key []byte, prev Index, put Putter) (Result, error) {
	res := Result{Index: Index{}}
	seen := map[string]bool{}
	var chunks []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{Path: rel, Mode: uint32(info.Mode().Perm()), MTime: info.ModTime().UnixNano(), Owner: fileOwner(info)}
		switch {
		case d.IsDir():
			e.Type = "d"
		case info.Mode()&fs.ModeSymlink != 0:
			e.Type = "l"
			if e.Target, err = os.Readlink(path); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			e.Type, e.Size = "f", info.Size()
			if old, ok := prev[rel]; ok && old.Type == "f" && old.Size == e.Size && old.MTime == e.MTime && old.Mode == e.Mode && old.Hash != "" && (e.Size == 0 || len(old.Chunks) > 0) {
				e.Chunks, e.Hash = old.Chunks, old.Hash
			} else {
				res.Changed = true
				if e.Chunks, e.Hash, err = sealFile(path, key, put); err != nil {
					return fmt.Errorf("%s: %w", rel, err)
				}
			}
			res.Size += e.Size
			res.Files++
		default:
			return nil
		}
		if old, ok := prev[rel]; !ok || old.Type != e.Type || old.Mode != e.Mode || old.Target != e.Target {
			res.Changed = true
		}
		seen[rel] = true
		chunks = append(chunks, e.Chunks...)
		res.Index[rel] = e
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	for p := range prev {
		if !seen[p] {
			res.Changed = true
		}
	}

	m := manifest{Version: 1}
	for _, e := range res.Index {
		m.Entries = append(m.Entries, e)
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	ch := sha256.New()
	for _, e := range m.Entries {
		fmt.Fprintf(ch, "%s\x00%s\x00%o\x00%s\x00%s\n", e.Path, e.Type, e.Mode, e.Hash, e.Target)
	}
	res.ContentHash = hex.EncodeToString(ch.Sum(nil))
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	json.NewEncoder(zw).Encode(m)
	zw.Close()
	doc := rootDoc{Version: 1, Size: res.Size, Files: res.Files, Created: time.Now().UnixNano()}
	for b := buf.Bytes(); len(b) > 0; {
		n := min(len(b), ChunkSize)
		id, err := sealPut(key, b[:n], put)
		if err != nil {
			return Result{}, err
		}
		doc.Parts = append(doc.Parts, id)
		b = b[n:]
	}
	rb, _ := json.Marshal(doc)
	if res.Root, err = sealPut(key, rb, put); err != nil {
		return Result{}, err
	}
	res.Chunks = dedupe(append(append(chunks, doc.Parts...), res.Root))
	return res, nil
}

func sealPut(key, plain []byte, put Putter) (string, error) {
	sealed, id, err := Seal(key, plain)
	if err != nil {
		return "", err
	}
	return id, put(id, sealed)
}

func sealFile(path string, key []byte, put Putter) ([]string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var ids []string
	h := sha256.New()
	buf := make([]byte, ChunkSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			h.Write(buf[:n])
			id, perr := sealPut(key, buf[:n], put)
			if perr != nil {
				return nil, "", perr
			}
			ids = append(ids, id)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return ids, hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return nil, "", err
		}
	}
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Chunks lists every chunk a snapshot needs, by reading its root and
// manifest. It is how a machine that only has the root can check a
// snapshot is complete.
func Chunks(root string, key []byte, get Getter) ([]string, error) {
	m, parts, err := readManifest(root, key, get)
	if err != nil {
		return nil, err
	}
	ids := append(parts, root)
	for _, e := range m.Entries {
		ids = append(ids, e.Chunks...)
	}
	return dedupe(ids), nil
}

func readManifest(root string, key []byte, get Getter) (manifest, []string, error) {
	var m manifest
	rb, err := fetchOpen(root, key, get)
	if err != nil {
		return m, nil, err
	}
	var doc rootDoc
	if err := json.Unmarshal(rb, &doc); err != nil || doc.Version != 1 {
		return m, nil, errors.New("storage: unreadable snapshot root")
	}
	var buf bytes.Buffer
	for _, id := range doc.Parts {
		b, err := fetchOpen(id, key, get)
		if err != nil {
			return m, nil, err
		}
		buf.Write(b)
	}
	zr, err := gzip.NewReader(&buf)
	if err == nil {
		err = json.NewDecoder(zr).Decode(&m)
	}
	if err != nil || m.Version != 1 {
		return m, nil, errors.New("storage: unreadable snapshot manifest")
	}
	return m, doc.Parts, nil
}

func fetchOpen(id string, key []byte, get Getter) ([]byte, error) {
	sealed, err := get(id)
	if err != nil {
		return nil, err
	}
	return Open(key, sealed, id)
}

// Restore writes snapshot root into dir and returns the index for the next
// snapshot. With clean set, everything already in dir is removed first (an
// app's volume coming back on a new machine); otherwise files are written
// over whatever is there. Paths that would escape dir are refused, and
// existing symlinks are never followed.
func Restore(dir, root string, key []byte, get Getter, clean bool) (Index, error) {
	m, _, err := readManifest(root, key, get)
	if err != nil {
		return nil, err
	}
	if clean {
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	idx := Index{}
	var links []Entry
	var dirs []Entry
	for _, e := range m.Entries {
		target, err := safeJoin(dir, e.Path)
		if err != nil {
			return nil, err
		}
		switch e.Type {
		case "d":
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
			dirs = append(dirs, e)
		case "f":
			if err := restoreFile(target, e, key, get); err != nil {
				return nil, fmt.Errorf("%s: %w", e.Path, err)
			}
		case "l":
			links = append(links, e)
			continue
		}
		idx[e.Path] = e
	}
	// Symlinks last, so none can redirect a file written above.
	for _, e := range links {
		target, _ := safeJoin(dir, e.Path)
		os.Remove(target)
		// Windows needs a privilege to make symlinks; a missing link is
		// better than a failed restore.
		if os.Symlink(e.Target, target) == nil {
			restoreOwner(target, e.Owner)
			idx[e.Path] = e
		}
	}
	// Directory times last, since writing files inside changes them.
	for i := len(dirs) - 1; i >= 0; i-- {
		target, _ := safeJoin(dir, dirs[i].Path)
		restoreOwner(target, dirs[i].Owner)
		os.Chmod(target, fs.FileMode(dirs[i].Mode))
		t := time.Unix(0, dirs[i].MTime)
		os.Chtimes(target, t, t)
	}
	return idx, nil
}

func restoreFile(target string, e Entry, key []byte, get Getter) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".sys1-restore"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var n int64
	for _, id := range e.Chunks {
		b, err := fetchOpen(id, key, get)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		f.Write(b)
		n += int64(len(b))
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if n != e.Size {
		os.Remove(tmp)
		return errors.New("size does not match the snapshot")
	}
	os.Remove(target)
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	restoreOwner(target, e.Owner)
	os.Chmod(target, fs.FileMode(e.Mode))
	t := time.Unix(0, e.MTime)
	return os.Chtimes(target, t, t)
}

// safeJoin joins a snapshot path onto dir, refusing absolute paths, ".."
// and paths that pass through an existing symlink.
func safeJoin(dir, rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("storage: unsafe path %q in snapshot", rel)
	}
	parts := strings.Split(rel, "/")
	cur := dir
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("storage: unsafe path %q in snapshot", rel)
		}
		cur = filepath.Join(cur, p)
		if i < len(parts)-1 {
			if fi, err := os.Lstat(cur); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				return "", fmt.Errorf("storage: path %q passes through a symlink", rel)
			}
		}
	}
	return cur, nil
}

// SaveIndex and LoadIndex keep an index next to the data it describes.
func SaveIndex(path string, idx Index) error {
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadIndex(path string) Index {
	var idx Index
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &idx)
	}
	if idx == nil {
		idx = Index{}
	}
	return idx
}
