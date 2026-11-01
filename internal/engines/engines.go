// Package engines keeps the container engine a machine relies on running,
// the way the watchdog keeps apps running: started when sys0 or sys1
// starts (so at boot), started again when it stops or crashes, with
// backoff, and reported plainly on the dashboard when it keeps failing.
//
// It knows Docker Desktop, OrbStack, Rancher Desktop, Colima, Podman
// machines and the Docker system service, and only starts what is already
// installed.
package engines

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Engine is one installed way to get a container runtime.
type Engine struct {
	Name    string // shown to people, e.g. "Docker Desktop"
	Runtime string // the command that talks to it: "docker" or "podman"
	start   func(ctx context.Context) error
}

// Installed lists the engines installed on this machine, preferred first.
func Installed() []Engine { return installed() }

// Answers reports whether runtime rt has an engine that answers.
func Answers(ctx context.Context, rt string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, rt, "info", "--format", "{{.ID}}").Output()
	id := strings.TrimSpace(string(out))
	return err == nil && id != "" && id != "<no value>"
}

// Policy, the same shape as the app watchdog's.
const (
	checkEvery  = 30 * time.Second
	startWait   = 3 * time.Minute // Docker Desktop's VM can take a while on a cold boot
	maxFailures = 5               // in a row, before slowing down
	slowRetry   = 30 * time.Minute
)

// Supervisor keeps one engine running.
type Supervisor struct {
	Log *slog.Logger
	// Changed is called after an engine starts, so the machine's
	// capabilities are measured again right away.
	Changed func()
	// for tests
	engines func() []Engine
	answers func(ctx context.Context, rt string) bool
	every   time.Duration
	wait    time.Duration

	mu       sync.Mutex
	problem  string
	failures int
	next     time.Time
	wasUp    bool
}

// Problem is what the dashboard should say about the engine, if anything.
func (s *Supervisor) Problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

func (s *Supervisor) defaults() {
	if s.engines == nil {
		s.engines = Installed
	}
	if s.answers == nil {
		s.answers = Answers
	}
	if s.every == 0 {
		s.every = checkEvery
	}
	if s.wait == 0 {
		s.wait = startWait
	}
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
}

// Run supervises until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	s.defaults()
	for {
		s.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.every):
		}
	}
}

// Once checks the engine one time and starts it if it isn't running
// (`sys0 doctor --fix`). It returns the problem left, if any.
func (s *Supervisor) Once(ctx context.Context) string {
	s.defaults()
	s.check(ctx)
	return s.Problem()
}

func (s *Supervisor) check(ctx context.Context) {
	engines := s.engines()
	if len(engines) == 0 {
		return
	}
	for _, rt := range runtimes(engines) {
		if s.answers(ctx, rt) {
			s.mu.Lock()
			s.failures, s.problem, s.wasUp = 0, "", true
			s.mu.Unlock()
			return
		}
	}
	s.mu.Lock()
	if time.Now().Before(s.next) {
		s.mu.Unlock()
		return
	}
	if s.wasUp {
		s.Log.Warn("the container engine stopped; starting it again", "engine", engines[0].Name)
		s.wasUp = false
	}
	s.mu.Unlock()

	e := engines[0]
	s.Log.Info("starting the container engine", "engine", e.Name)
	err := e.start(ctx)
	up := false
	if err == nil {
		deadline := time.Now().Add(s.wait)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if s.answers(ctx, e.Runtime) {
				up = true
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
	s.mu.Lock()
	if up {
		s.failures, s.problem, s.wasUp, s.next = 0, "", true, time.Time{}
		s.mu.Unlock()
		s.Log.Info("the container engine is running", "engine", e.Name)
		if s.Changed != nil {
			s.Changed()
		}
		return
	}
	s.failures++
	why := "it didn't answer within " + s.wait.String()
	if err != nil {
		why = err.Error()
	}
	delay := min(s.every<<min(s.failures, 6), slowRetry)
	if s.failures >= maxFailures {
		delay = slowRetry
		s.problem = fmt.Sprintf("%s keeps failing to start (%d tries: %s). sys0 tries again every %d minutes.", e.Name, s.failures, why, int(slowRetry.Minutes()))
	} else {
		s.problem = fmt.Sprintf("%s isn't running; sys0 is starting it (try %d of %d).", e.Name, s.failures, maxFailures)
	}
	s.next = time.Now().Add(delay)
	s.mu.Unlock()
	s.Log.Warn("the container engine didn't start", "engine", e.Name, "try", s.failures, "err", why)
}

func runtimes(engines []Engine) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range engines {
		if !seen[e.Runtime] {
			seen[e.Runtime] = true
			out = append(out, e.Runtime)
		}
	}
	return out
}

// command makes a start function from a command line.
func command(name string, args ...string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, startWait)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if i := strings.IndexByte(msg, '\n'); i > 0 {
				msg = msg[:i]
			}
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("%s: %s", name, msg)
		}
		return nil
	}
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// podmanMachine reports whether a Podman machine (the VM Podman uses on
// macOS and Windows) has been set up.
func podmanMachine() bool {
	if !onPath("podman") {
		return false
	}
	out, err := exec.Command("podman", "machine", "list", "--format", "{{.Name}}").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}
