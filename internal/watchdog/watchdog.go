// Package watchdog keeps a workload alive. It restarts the workload when it
// crashes, backing off between attempts, and gives up after too many crashes
// in a row so a broken workload can be moved to a healthier machine instead
// of looping forever.
package watchdog

import (
	"context"
	"errors"
	"time"
)

// Config tunes the restart policy. Zero values pick the defaults.
type Config struct {
	MaxCrashes int           // consecutive crashes before giving up (default 5)
	Backoff    time.Duration // delay before the first restart, doubled per crash (default 500ms)
	MaxBackoff time.Duration // cap on the restart delay (default 30s)
	Healthy    time.Duration // a run at least this long resets the crash count (default 60s)
	// Service workloads are expected to run forever, so a clean exit is
	// treated as a crash and restarted. Jobs that exit cleanly are done.
	Service bool
}

func (c *Config) defaults() {
	if c.MaxCrashes <= 0 {
		c.MaxCrashes = 5
	}
	if c.Backoff <= 0 {
		c.Backoff = 500 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.Healthy <= 0 {
		c.Healthy = 60 * time.Second
	}
}

// Outcome says why Run returned.
type Outcome int

const (
	Completed Outcome = iota // a job exited cleanly
	Stopped                  // the context was cancelled
	GaveUp                   // MaxCrashes crashes in a row
)

func (o Outcome) String() string {
	switch o {
	case Completed:
		return "completed"
	case Stopped:
		return "stopped"
	default:
		return "gave-up"
	}
}

// ErrExitedCleanly is reported to OnCrash when a service exits with no error.
var ErrExitedCleanly = errors.New("service exited unexpectedly")

// Run calls start repeatedly until the workload completes, ctx is cancelled,
// or it crashes MaxCrashes times in a row. start runs one attempt and blocks
// until it ends, returning nil for a clean exit. onCrash, if set, is told
// about every crash. On GaveUp, Run also returns the last crash's error.
func Run(ctx context.Context, cfg Config, start func(ctx context.Context, attempt int) error, onCrash func(attempt int, err error)) (Outcome, error) {
	cfg.defaults()
	crashes := 0
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return Stopped, nil
		}
		began := time.Now()
		err := start(ctx, attempt)
		if ctx.Err() != nil {
			return Stopped, nil
		}
		if err == nil && !cfg.Service {
			return Completed, nil
		}
		if err == nil {
			err = ErrExitedCleanly
		}
		if time.Since(began) >= cfg.Healthy {
			crashes = 0
		}
		crashes++
		if onCrash != nil {
			onCrash(attempt, err)
		}
		if crashes >= cfg.MaxCrashes {
			return GaveUp, err
		}
		delay := cfg.Backoff << (crashes - 1)
		if delay > cfg.MaxBackoff || delay <= 0 {
			delay = cfg.MaxBackoff
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return Stopped, nil
		case <-t.C:
		}
	}
}
