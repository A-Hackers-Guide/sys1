package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"
)

var fast = Config{Backoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, Healthy: time.Hour}

func TestGivesUpAfterFiveCrashes(t *testing.T) {
	calls := 0
	out, err := Run(context.Background(), fast, func(context.Context, int) error {
		calls++
		return errors.New("boom")
	}, nil)
	if out != GaveUp || err == nil || calls != 5 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}

func TestRecoversAfterCrashes(t *testing.T) {
	calls := 0
	out, _ := Run(context.Background(), fast, func(context.Context, int) error {
		calls++
		if calls < 4 {
			return errors.New("flaky")
		}
		return nil
	}, nil)
	if out != Completed || calls != 4 {
		t.Fatalf("out=%v calls=%d", out, calls)
	}
}

func TestHealthyRunResetsCrashCount(t *testing.T) {
	cfg := fast
	cfg.Healthy = 5 * time.Millisecond
	calls := 0
	out, _ := Run(context.Background(), cfg, func(context.Context, int) error {
		calls++
		if calls == 12 {
			return nil
		}
		if calls%3 == 0 {
			time.Sleep(6 * time.Millisecond) // a long, healthy run
		}
		return errors.New("crash")
	}, nil)
	if out != Completed || calls != 12 {
		t.Fatalf("out=%v calls=%d", out, calls)
	}
}

func TestServiceCleanExitIsRestarted(t *testing.T) {
	cfg := fast
	cfg.Service = true
	calls := 0
	out, err := Run(context.Background(), cfg, func(context.Context, int) error { calls++; return nil }, nil)
	if out != GaveUp || !errors.Is(err, ErrExitedCleanly) || calls != 5 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}

func TestStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out, _ := Run(ctx, fast, func(ctx context.Context, _ int) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	if out != Stopped {
		t.Fatalf("out=%v", out)
	}
}
