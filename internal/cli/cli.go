// Package cli holds the pieces sys0 and sys1 share: logging, signal
// handling and background-service management for every OS.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
)

// Logger writes to stderr and to <dataDir>/<name>.log, starting the file
// afresh once it passes 10 MB.
func Logger(dataDir, name string, verbose bool) *slog.Logger {
	l, _ := OpenLogger(dataDir, name, verbose)
	return l
}

// OpenLogger is Logger for callers that outlive it and must close the log
// file (embedded uses such as the mobile apps).
func OpenLogger(dataDir, name string, verbose bool) (*slog.Logger, io.Closer) {
	var w io.Writer = os.Stderr
	var c io.Closer = io.NopCloser(nil)
	if err := os.MkdirAll(dataDir, 0o700); err == nil {
		path := filepath.Join(dataDir, name+".log")
		if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
			os.Rename(path, path+".old")
		}
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			w, c = io.MultiWriter(os.Stderr, f), f
		}
	}
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), c
}

// SignalContext is cancelled on Ctrl+C or a termination signal.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Service describes a background service for kardianos/service, which
// speaks launchd, systemd, OpenRC, Upstart, SysV, FreeBSD rc.d and the
// Windows service manager.
type Service struct {
	Name, DisplayName, Description string
	Args                           []string
	// Run does the work until ctx is cancelled.
	Run func(ctx context.Context) error
}

type program struct {
	s      *Service
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.s.Run(ctx); err != nil {
			slog.Error("service stopped with an error", "err", err)
			os.Exit(1) // let the service manager restart us
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
		}
	}
	return nil
}

// UserService reports whether to install as a per-user service, which
// needs no admin rights (launchd agent, systemd --user). Windows and
// FreeBSD only offer system services.
func UserService() bool {
	return os.Geteuid() > 0 && (runtime.GOOS == "darwin" || runtime.GOOS == "linux")
}

func (s *Service) build() (service.Service, error) {
	cfg := &service.Config{
		Name:        s.Name,
		DisplayName: s.DisplayName,
		Description: s.Description,
		Arguments:   s.Args,
		Option: service.KeyValue{
			"UserService":            UserService(),
			"KeepAlive":              true,
			"RunAtLoad":              true,
			"Restart":                "always",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   60,
			"DelayedAutoStart":       false,
		},
	}
	return service.New(&program{s: s}, cfg)
}

// RunManaged runs s, either under the OS service manager or, when started
// from a terminal, in the foreground.
func (s *Service) RunManaged() error {
	svc, err := s.build()
	if err != nil {
		return err
	}
	return svc.Run()
}

// Install registers the service (replacing an old registration) and
// starts it.
func (s *Service) Install() error {
	svc, err := s.build()
	if err != nil {
		return err
	}
	if st, err := svc.Status(); err == nil && st != service.StatusUnknown {
		svc.Stop()
		svc.Uninstall()
	}
	if err := svc.Install(); err != nil {
		return friendly(err)
	}
	if err := svc.Start(); err != nil {
		return friendly(err)
	}
	return nil
}

func (s *Service) Uninstall() error {
	svc, err := s.build()
	if err != nil {
		return err
	}
	svc.Stop()
	return friendly(svc.Uninstall())
}

func (s *Service) Control(action string) error {
	svc, err := s.build()
	if err != nil {
		return err
	}
	return friendly(service.Control(svc, action))
}

func (s *Service) Status() string {
	svc, err := s.build()
	if err != nil {
		return "unknown"
	}
	st, err := svc.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		return "not installed"
	case err != nil:
		return "unknown (" + err.Error() + ")"
	case st == service.StatusRunning:
		return "running"
	case st == service.StatusStopped:
		return "stopped"
	}
	return "unknown"
}

func friendly(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrPermission) || isAccessDenied(err) {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("%w (run the installer or terminal as Administrator)", err)
		}
		return fmt.Errorf("%w (try again with sudo)", err)
	}
	return err
}

func isAccessDenied(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"access is denied", "permission denied", "operation not permitted"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
