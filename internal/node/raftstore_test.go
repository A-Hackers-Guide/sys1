package node

import (
	"path/filepath"
	"testing"

	"github.com/hashicorp/raft"
)

func TestRaftStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raft.journal")
	s, err := openRaftStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var logs []*raft.Log
	for i := uint64(1); i <= 10; i++ {
		logs = append(logs, &raft.Log{Index: i, Term: 1, Data: []byte{byte(i)}})
	}
	s.StoreLogs(logs)
	s.DeleteRange(1, 3)  // compaction after a snapshot
	s.DeleteRange(9, 10) // a conflicting suffix
	s.SetUint64([]byte("CurrentTerm"), 7)
	s.Set([]byte("LastVoteCand"), []byte("b"))
	s.Close()

	s, err = openRaftStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if f, _ := s.FirstIndex(); f != 4 {
		t.Fatalf("first = %d", f)
	}
	if l, _ := s.LastIndex(); l != 8 {
		t.Fatalf("last = %d", l)
	}
	var l raft.Log
	if err := s.GetLog(2, &l); err != raft.ErrLogNotFound {
		t.Fatalf("deleted log still there: %v", err)
	}
	if err := s.GetLog(5, &l); err != nil || l.Data[0] != 5 {
		t.Fatalf("log 5: %+v %v", l, err)
	}
	if v, _ := s.GetUint64([]byte("CurrentTerm")); v != 7 {
		t.Fatalf("term = %d", v)
	}
	if _, err := s.Get([]byte("missing")); err == nil {
		t.Fatal("missing key found")
	}
}
