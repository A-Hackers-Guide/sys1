package engines

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu     sync.Mutex
	up     bool
	starts int
	fail   bool
}

func (f *fake) engine() Engine {
	return Engine{Name: "Docker Desktop", Runtime: "docker", start: func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.starts++
		if f.fail {
			return errors.New("it broke")
		}
		f.up = true
		return nil
	}}
}

func (f *fake) answers(context.Context, string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.up }

func supervisor(f *fake) *Supervisor {
	s := &Supervisor{engines: func() []Engine { return []Engine{f.engine()} }, answers: f.answers, wait: 50 * time.Millisecond}
	s.defaults()
	return s
}

func TestStartsTheEngineAndRestartsItAfterACrash(t *testing.T) {
	f := &fake{}
	s := supervisor(f)
	changed := 0
	s.Changed = func() { changed++ }
	s.check(context.Background())
	if !f.up || f.starts != 1 || changed != 1 || s.Problem() != "" {
		t.Fatalf("not started: up=%v starts=%d changed=%d problem=%q", f.up, f.starts, changed, s.Problem())
	}
	s.check(context.Background()) // running: left alone
	if f.starts != 1 {
		t.Fatal("started a running engine again")
	}
	f.mu.Lock()
	f.up = false // it crashed
	f.mu.Unlock()
	s.check(context.Background())
	if !f.up || f.starts != 2 {
		t.Fatal("not restarted after a crash")
	}
}

func TestBacksOffAndSaysSoWhenItKeepsFailing(t *testing.T) {
	f := &fake{fail: true}
	s := supervisor(f)
	for i := 0; i < maxFailures; i++ {
		s.mu.Lock()
		s.next = time.Time{} // skip the wait between tries
		s.mu.Unlock()
		s.check(context.Background())
	}
	if f.starts != maxFailures {
		t.Fatalf("%d starts", f.starts)
	}
	if p := s.Problem(); !strings.Contains(p, "keeps failing to start") || !strings.Contains(p, "it broke") {
		t.Fatalf("problem: %q", p)
	}
	s.check(context.Background()) // within the slow-retry wait
	if f.starts != maxFailures {
		t.Fatal("didn't back off")
	}
	if time.Until(s.next) < slowRetry-time.Minute {
		t.Fatalf("next try in %s", time.Until(s.next))
	}
	// Someone starts it by hand: the problem clears.
	f.mu.Lock()
	f.up = true
	f.mu.Unlock()
	s.check(context.Background())
	if s.Problem() != "" {
		t.Fatal("problem kept after the engine came back")
	}
}

func TestNothingInstalledNothingDone(t *testing.T) {
	s := &Supervisor{engines: func() []Engine { return nil }}
	s.defaults()
	s.check(context.Background())
	if s.Problem() != "" {
		t.Fatal("problem without an engine")
	}
}
