// Package sys0mobile runs the sys0 client inside a mobile app, for
// platforms where an app can't start other programs (iOS, iPadOS). The
// app calls these functions; gomobile turns them into an iOS framework.
//
// There, sys0 lends storage to the pool: iOS apps can't run other
// programs or containers, and are suspended in the background, so the app
// runs this only while it is open and the device is charging.
package sys0mobile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/cli"
	"github.com/A-Hackers-Guide/sys1/internal/paths"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

var (
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	lastErr string
)

func init() { paths.FixDNS() }

// Version is sys0's version.
func Version() string { return version.Version }

// Joined reports whether this device has joined a pool.
func Joined(dataDir string) bool {
	_, err := agent.LoadConfig(dataDir)
	return err == nil
}

// Join joins the pool in joinCode. mine means "this is my device".
func Join(dataDir, joinCode, name string, mine bool, storageGB int) error {
	code, err := protocol.ParseJoinCode(joinCode)
	if err != nil {
		return err
	}
	trust := protocol.TrustCommunity
	if mine {
		trust = protocol.TrustOwner
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := &resources.Sampler{DataDir: dataDir}
	cfg, err := agent.Register(ctx, code, name, trust, sample(s), version.Version)
	if err != nil {
		return err
	}
	if storageGB >= 0 {
		cfg.StorageGB = storageGB
	}
	return agent.SaveConfig(dataDir, cfg)
}

// sample measures the device, without the "exec" runtime: apps here can't
// start other programs.
func sample(s *resources.Sampler) protocol.Resources {
	r := s.Sample(false)
	r.Runtimes = slices.DeleteFunc(r.Runtimes, func(x string) bool { return x == "exec" })
	return r
}

// Start runs sys0 in the background of the app until Stop. It does
// nothing if it is already running.
func Start(dataDir string) error {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		return nil
	}
	cfg, err := agent.LoadConfig(dataDir)
	if err != nil {
		return err
	}
	a, err := agent.New(dataDir, cfg, cli.Logger(dataDir, "sys0", false))
	if err != nil {
		return err
	}
	s := &resources.Sampler{DataDir: dataDir}
	a.Sample = func(bool) protocol.Resources { return sample(s) }
	ctx, c := context.WithCancel(context.Background())
	cancel, done, lastErr = c, make(chan struct{}), ""
	go func(d chan struct{}) {
		defer close(d)
		if err := a.Run(ctx); err != nil {
			mu.Lock()
			lastErr = err.Error()
			mu.Unlock()
		}
	}(done)
	return nil
}

// Stop stops sys0 and waits for it to finish saving what it must.
func Stop() {
	mu.Lock()
	c, d := cancel, done
	cancel, done = nil, nil
	mu.Unlock()
	if c != nil {
		c()
		select {
		case <-d:
		case <-time.After(20 * time.Second):
		}
	}
}

// Running reports whether sys0 is running.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return cancel != nil
}

// Status describes what sys0 is doing, in a line or two.
func Status(dataDir string) string {
	cfg, err := agent.LoadConfig(dataDir)
	if err != nil {
		return "Not joined to a pool"
	}
	state := "Paused"
	if Running() {
		state = "Lending storage"
	}
	mu.Lock()
	e := lastErr
	mu.Unlock()
	if e != "" {
		state += " (last problem: " + e + ")"
	}
	return state + " · node " + cfg.NodeAddr + " · " + strconv.Itoa(cfg.StorageGB) + " GB lent"
}

// Leave stops sys0 and forgets the pool. Data it held for the pool stays
// encrypted on the device until the app is removed.
func Leave(dataDir string) error {
	Stop()
	err := os.Remove(filepath.Join(dataDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
