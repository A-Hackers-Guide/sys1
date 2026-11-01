package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func readAll(t *testing.T, path string) []string {
	t.Helper()
	var got []string
	j, err := Open(path, func(p []byte) error { got = append(got, string(p)); return nil })
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	return got
}

func TestAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	j, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := j.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	got := readAll(t, path)
	if len(got) != 100 || got[0] != "rec-0" || got[99] != "rec-99" {
		t.Fatalf("replay mismatch: %d records, first=%q", len(got), got[0])
	}
}

func TestTornTailIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	j, _ := Open(path, nil)
	j.Append([]byte("one"))
	j.Append([]byte("two"))
	j.Close()

	// Simulate a power cut halfway through writing a third record.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write(frame([]byte("three-is-long"))[:10])
	f.Close()

	if got := readAll(t, path); len(got) != 2 || got[1] != "two" {
		t.Fatalf("want [one two], got %v", got)
	}
	// The torn bytes are gone, so new appends land cleanly.
	j, _ = Open(path, nil)
	j.Append([]byte("three"))
	j.Close()
	if got := readAll(t, path); len(got) != 3 || got[2] != "three" {
		t.Fatalf("want 3 records ending in three, got %v", got)
	}
}

func TestCorruptRecordStopsReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	j, _ := Open(path, nil)
	j.Append([]byte("good"))
	j.Append([]byte("flipped"))
	j.Close()
	b, _ := os.ReadFile(path)
	b[len(b)-1] ^= 0xff
	os.WriteFile(path, b, 0o600)
	if got := readAll(t, path); len(got) != 1 || got[0] != "good" {
		t.Fatalf("want [good], got %v", got)
	}
}

func TestAppendMany(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	j, _ := Open(path, nil)
	j.Append([]byte("one"))
	if err := j.AppendMany([][]byte{[]byte("two"), []byte("three")}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	if got := readAll(t, path); len(got) != 3 || got[2] != "three" {
		t.Fatalf("got %v", got)
	}
}

func TestCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	j, _ := Open(path, nil)
	for i := 0; i < 10; i++ {
		j.Append([]byte("old"))
	}
	if err := j.Compact([][]byte{[]byte("snap")}); err != nil {
		t.Fatal(err)
	}
	if j.Len() != 1 {
		t.Fatalf("len after compact = %d", j.Len())
	}
	j.Append([]byte("after"))
	j.Close()
	if got := readAll(t, path); len(got) != 2 || got[0] != "snap" || got[1] != "after" {
		t.Fatalf("got %v", got)
	}
}
