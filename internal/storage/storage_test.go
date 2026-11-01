package storage

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// mem is an in-memory chunk pool for tests.
type mem struct {
	mu   sync.Mutex
	m    map[string][]byte
	puts int
}

func newMem() *mem { return &mem{m: map[string][]byte{}} }
func (p *mem) put(id string, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ID(b) != id {
		return ErrCorrupt
	}
	p.m[id] = append([]byte(nil), b...)
	p.puts++
	return nil
}
func (p *mem) get(id string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.m[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

func TestSealOpen(t *testing.T) {
	key := DeriveKey(NewPoolKey(), "task:x")
	sealed, id, err := Seal(key, []byte("secret data"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("secret")) {
		t.Fatal("plaintext visible in sealed chunk")
	}
	if out, err := Open(key, sealed, id); err != nil || string(out) != "secret data" {
		t.Fatalf("open: %q %v", out, err)
	}
	sealed[20] ^= 1
	if _, err := Open(key, sealed, id); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered chunk: %v", err)
	}
	sealed[20] ^= 1
	if _, err := Open(DeriveKey(NewPoolKey(), "task:x"), sealed, id); !errors.Is(err, ErrKey) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestDerivedKeysDiffer(t *testing.T) {
	pk := NewPoolKey()
	if bytes.Equal(DeriveKey(pk, "task:a"), DeriveKey(pk, "task:b")) {
		t.Fatal("owners share a key")
	}
	if !bytes.Equal(DeriveKey(pk, "task:a"), DeriveKey(pk, "task:a")) {
		t.Fatal("derivation is not deterministic")
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	sealed, id, _ := Seal(DeriveKey(NewPoolKey(), "x"), []byte("hello"))
	if err := s.Put(id, sealed); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(strings.Repeat("0", 64), sealed); !errors.Is(err, ErrCorrupt) {
		t.Fatal("chunk stored under the wrong id")
	}
	if b, err := s.Get(id); err != nil || !bytes.Equal(b, sealed) {
		t.Fatal("get failed")
	}
	// Reopen counts what is on disk.
	s2, _ := OpenStore(dir)
	if used, n := s2.Usage(); n != 1 || used != int64(len(sealed)) {
		t.Fatalf("usage after reopen: %d bytes %d chunks", used, n)
	}
	// Bit rot is detected and the bad copy removed.
	// Flip the first byte (it's random, so setting it to a fixed value
	// would leave it unchanged one time in 256).
	os.WriteFile(s.path(id), append([]byte{sealed[0] ^ 0xff}, sealed[1:]...), 0o600)
	if _, err := s.Get(id); !errors.Is(err, ErrCorrupt) || s.Has(id) {
		t.Fatal("corrupt chunk served or kept")
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	write(t, filepath.Join(src, "dir/b.txt"), "bravo")
	write(t, filepath.Join(src, "empty"), "")
	big := make([]byte, ChunkSize*2+123)
	rand.Read(big)
	os.WriteFile(filepath.Join(src, "dir/big.bin"), big, 0o600)
	if runtime.GOOS != "windows" {
		os.Symlink("a.txt", filepath.Join(src, "link"))
	}

	pool := newMem()
	key := DeriveKey(NewPoolKey(), "task:t")
	res, err := Take(src, key, nil, pool.put)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Files != 4 || res.Size != int64(5+5+len(big)) {
		t.Fatalf("result: %+v", res)
	}
	for _, id := range res.Chunks {
		if _, err := pool.get(id); err != nil {
			t.Fatalf("chunk %s listed but not stored", id)
		}
	}
	if ids, err := Chunks(res.Root, key, pool.get); err != nil || len(ids) != len(res.Chunks) {
		t.Fatalf("Chunks(): %d ids, %v (want %d)", len(ids), err, len(res.Chunks))
	}

	// Nothing changed: no file is read or uploaded again.
	before := pool.puts
	res2, err := Take(src, key, res.Index, pool.put)
	if err != nil || res2.Changed {
		t.Fatalf("second snapshot changed=%v err=%v", res2.Changed, err)
	}
	if uploaded := pool.puts - before; uploaded > 2 {
		t.Fatalf("unchanged snapshot uploaded %d chunks (only manifest and root expected)", uploaded)
	}

	dst := filepath.Join(t.TempDir(), "restore")
	idx, err := Restore(dst, res.Root, key, pool.get, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.txt", "dir/b.txt", "empty"} {
		a, _ := os.ReadFile(filepath.Join(src, p))
		b, err := os.ReadFile(filepath.Join(dst, p))
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s differs after restore", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "dir/big.bin")); !bytes.Equal(b, big) {
		t.Fatal("big file differs after restore")
	}
	if runtime.GOOS != "windows" {
		if l, _ := os.Readlink(filepath.Join(dst, "link")); l != "a.txt" {
			t.Fatalf("symlink restored as %q", l)
		}
	}
	// The returned index matches, so a snapshot of the restored copy
	// reuses every chunk.
	res3, err := Take(dst, key, idx, pool.put)
	if err != nil || res3.Changed {
		t.Fatalf("snapshot after restore changed=%v err=%v", res3.Changed, err)
	}

	// Same contents, different times and keys: same content hash.
	other := t.TempDir()
	write(t, filepath.Join(other, "a.txt"), "alpha")
	write(t, filepath.Join(other, "dir/b.txt"), "bravo")
	write(t, filepath.Join(other, "empty"), "")
	os.WriteFile(filepath.Join(other, "dir/big.bin"), big, 0o600)
	if runtime.GOOS != "windows" {
		os.Symlink("a.txt", filepath.Join(other, "link"))
	}
	twin, err := Take(other, DeriveKey(NewPoolKey(), "task:u"), nil, newMem().put)
	if err != nil || twin.ContentHash != res.ContentHash {
		t.Fatalf("identical folders hash differently: %s vs %s (%v)", twin.ContentHash, res.ContentHash, err)
	}
	write(t, filepath.Join(other, "a.txt"), "ALPHA")
	if changed, _ := Take(other, key, nil, newMem().put); changed.ContentHash == res.ContentHash {
		t.Fatal("different contents hash the same")
	}

	// A wrong key cannot read the snapshot.
	if _, err := Restore(t.TempDir(), res.Root, DeriveKey(NewPoolKey(), "x"), pool.get, true); err == nil {
		t.Fatal("restored with the wrong key")
	}
}

func TestSafeJoinRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", "a//b", `a\b`, ""} {
		if _, err := safeJoin(dir, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if runtime.GOOS != "windows" {
		os.Symlink(t.TempDir(), filepath.Join(dir, "out"))
		if _, err := safeJoin(dir, "out/x"); err == nil {
			t.Error("followed a symlink out of the restore folder")
		}
	}
}
