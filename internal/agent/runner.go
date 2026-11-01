package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/storage"
	"github.com/A-Hackers-Guide/sys1/internal/watchdog"
)

const maxLogBytes = 10 << 20

// execute runs one task attempt under the watchdog.
func (a *Agent) execute(ctx context.Context, t protocol.Task) protocol.Result {
	dir := filepath.Join(a.tasksDir, t.ID)
	state := filepath.Join(dir, "state")
	checkpoint := filepath.Join(state, "checkpoint")
	res := protocol.Result{TaskID: t.ID, Attempt: t.Attempt}
	if err := os.MkdirAll(state, 0o700); err != nil {
		res.Outcome, res.Message = protocol.OutcomeFailed, err.Error()
		return res
	}
	if len(t.Checkpoint) > 0 {
		os.WriteFile(checkpoint, t.Checkpoint, 0o600)
	}
	vols := &volumes{a: a, t: t, dir: filepath.Join(dir, "volumes"), state: state}
	if len(t.Spec.Volumes) > 0 {
		if err := vols.prepare(ctx); err != nil {
			res.Outcome, res.Message = protocol.OutcomeFailed, "could not restore this app's data: "+err.Error()
			return res
		}
		stop := vols.every(ctx)
		defer func() {
			stop()
			// One last snapshot, even when sys0 is shutting down, so the
			// next machine starts from the newest data possible.
			fctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			vols.take(fctx)
		}()
	}
	out := &tail{max: 4096}
	exitCode := 0
	outcome, err := watchdog.Run(ctx, watchdog.Config{Service: t.Spec.Service},
		func(ctx context.Context, attempt int) error {
			code, err := a.runOnce(ctx, t, dir, out)
			exitCode = code
			return err
		},
		func(n int, err error) { a.log.Warn("task crashed, restarting", "task", t.ID, "crash", n, "err", err) },
	)
	res.ExitCode, res.Output = exitCode, out.String()
	switch outcome {
	case watchdog.Completed:
		res.Outcome = protocol.OutcomeCompleted
	case watchdog.Stopped:
		res.Outcome = protocol.OutcomeStopped
	case watchdog.GaveUp:
		res.Outcome = protocol.OutcomeFailed
		res.Message = fmt.Sprintf("crashed 5 times in a row (last: %v)", err)
		if b, err := readCapped(checkpoint, protocol.MaxCheckpoint); err == nil {
			res.Checkpoint = b
		}
	}
	if !t.Spec.Service && outcome != watchdog.Stopped && len(t.Spec.Volumes) == 0 {
		os.RemoveAll(dir)
	}
	return res
}

// volumes manages an app's pooled volumes on this machine.
type volumes struct {
	a     *Agent
	t     protocol.Task
	dir   string // holds vol0, vol1, ...
	state string

	mu   sync.Mutex
	last string // newest snapshot root this machine's data matches
}

func (v *volumes) marker() string { return filepath.Join(v.state, "snapshot") }
func (v *volumes) index() string  { return filepath.Join(v.state, "index.json") }

// prepare brings the app's data here. If this machine's copy already
// matches the snapshot the node knows about (it ran here last), it is kept
// as it is; otherwise the node's snapshot replaces it.
func (v *volumes) prepare(ctx context.Context) error {
	b, _ := os.ReadFile(v.marker())
	v.last = strings.TrimSpace(string(b))
	if v.t.Restore != "" && v.t.Restore != v.last {
		v.a.log.Info("restoring app data from the pool", "task", v.t.ID, "snapshot", v.t.Restore[:12])
		idx, err := storage.Restore(v.dir, v.t.Restore, v.t.DataKey, v.a.st.getter(ctx), true)
		if err != nil {
			return err
		}
		storage.SaveIndex(v.index(), idx)
		v.last = v.t.Restore
		os.WriteFile(v.marker(), []byte(v.last), 0o600)
	}
	for i := range v.t.Spec.Volumes {
		p := v.path(i)
		if err := os.MkdirAll(p, 0o777); err != nil {
			return err
		}
		// Apps in containers often run as their own user; let them write.
		os.Chmod(p, 0o777)
	}
	return nil
}

func (v *volumes) path(i int) string { return filepath.Join(v.dir, "vol"+strconv.Itoa(i)) }

// every snapshots the volumes on the app's interval until stopped.
func (v *volumes) every(ctx context.Context) (stop func()) {
	interval := time.Duration(v.t.Spec.SnapshotSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tk.C:
				v.take(ctx)
			}
		}
	}()
	return func() { close(done); <-finished }
}

func (v *volumes) take(ctx context.Context) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var freeze func() func()
	if v.t.Spec.Kind == protocol.KindContainer {
		freeze = func() func() {
			// Pausing freezes the app for the moment files are read, so
			// the snapshot is consistent, like a sudden power cut.
			rt, name := v.a.containerRuntime(), "sys1-"+v.t.ID
			if exec.Command(rt, "pause", name).Run() != nil {
				return func() {}
			}
			return func() { exec.Command(rt, "unpause", name).Run() }
		}
	}
	root, err := v.a.st.snapshot(ctx, v.dir, "task:"+v.t.ID, v.t.Attempt, v.t.DataKey, v.index(), v.last, freeze)
	if err != nil {
		v.a.log.Warn("app data snapshot failed; will retry", "task", v.t.ID, "err", err)
		return
	}
	if root != v.last {
		v.last = root
		os.WriteFile(v.marker(), []byte(root), 0o600)
		v.a.log.Info("app data saved to the pool", "task", v.t.ID, "snapshot", root[:12])
	}
}

func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func (a *Agent) containerRuntime() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last.HasRuntime("docker") {
		return "docker"
	}
	return "podman"
}

// runOnce runs a single attempt and returns its exit code. A nil error
// means a clean exit.
func (a *Agent) runOnce(ctx context.Context, t protocol.Task, dir string, out *tail) (int, error) {
	state := filepath.Join(dir, "state")
	var cmd *exec.Cmd
	var stop func()
	switch t.Spec.Kind {
	case protocol.KindContainer:
		rt := a.containerRuntime()
		name := "sys1-" + t.ID
		exec.Command(rt, "rm", "-f", name).Run() // leftover from a crash
		a.mu.Lock()
		gpu := t.Spec.GPU && rt == "docker" && resources.NvidiaGPU(a.last)
		a.mu.Unlock()
		cmd = exec.Command(rt, containerArgs(t, name, dir, a.cfg.Trust, gpu)...)
		stop = func() {
			sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			exec.CommandContext(sctx, rt, "stop", "-t", "10", name).Run()
		}
	case protocol.KindExec:
		cmd = exec.Command(t.Spec.Command[0], t.Spec.Command[1:]...)
		cmd.Dir = state
		cmd.Env = append(os.Environ(), "SYS1_TASK_ID="+t.ID, "SYS1_CHECKPOINT="+filepath.Join(state, "checkpoint"))
		for i := range t.Spec.Volumes {
			cmd.Env = append(cmd.Env, "SYS1_VOLUME_"+strconv.Itoa(i)+"="+filepath.Join(dir, "volumes", "vol"+strconv.Itoa(i)))
		}
		for _, k := range sortedKeys(t.Spec.Env) {
			cmd.Env = append(cmd.Env, k+"="+t.Spec.Env[k])
		}
		stop = func() {
			if runtime.GOOS == "windows" {
				cmd.Process.Kill()
				return
			}
			cmd.Process.Signal(os.Interrupt)
		}
	}
	logf, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return -1, err
	}
	defer logf.Close()
	w := io.MultiWriter(out, &capped{w: logf, left: maxLogBytes})
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err), err
	case <-ctx.Done():
		stop()
		select {
		case err := <-done:
			return exitCode(err), ctx.Err()
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			return exitCode(<-done), ctx.Err()
		}
	}
}

// containerArgs builds a hardened `docker run`. Every machine gets the
// lowest CPU weight, a process limit and no privilege escalation; machines
// shared by other people also drop every capability except a small,
// common set.
func containerArgs(t protocol.Task, name, dir string, trust protocol.Trust, gpu bool) []string {
	s := t.Spec
	state := filepath.Join(dir, "state")
	args := []string{"run", "--rm", "--name", name, "--label", "sys1.task=" + t.ID, "--init",
		"--cpu-shares", "2", "--pids-limit", "1024", "--security-opt", "no-new-privileges"}
	if trust != protocol.TrustOwner {
		args = append(args, "--cap-drop", "ALL")
		for _, c := range []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "NET_BIND_SERVICE", "KILL"} {
			args = append(args, "--cap-add", c)
		}
	}
	if s.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.CPU, 'f', 2, 64))
	}
	if s.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(s.MemoryMB)+"m")
	}
	if !s.Network {
		args = append(args, "--network", "none")
	}
	for _, p := range s.Ports {
		args = append(args, "-p", p)
	}
	// Pooled volumes are folders sys0 can snapshot; local ones are named
	// volumes, which work the same on every OS and survive restarts.
	for i, v := range s.Volumes {
		args = append(args, "-v", filepath.Join(dir, "volumes", "vol"+strconv.Itoa(i))+":"+v)
	}
	for i, v := range s.LocalVolumes {
		args = append(args, "-v", fmt.Sprintf("sys1-%s-local%d:%s", t.ID, i, v))
	}
	args = append(args, "-v", state+":/sys1", "-e", "SYS1_TASK_ID="+t.ID, "-e", "SYS1_CHECKPOINT=/sys1/checkpoint")
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "-e", k+"="+s.Env[k])
	}
	if gpu {
		args = append(args, "--gpus", "all")
	}
	args = append(args, s.Image)
	return append(args, s.Command...)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// capped stops writing after left bytes but always reports success, so a
// chatty workload never fills the disk or blocks on its own output.
type capped struct {
	w    io.Writer
	left int64
}

func (c *capped) Write(p []byte) (int, error) {
	if c.left > 0 {
		n := min(int64(len(p)), c.left)
		c.w.Write(p[:n])
		c.left -= n
	}
	return len(p), nil
}
