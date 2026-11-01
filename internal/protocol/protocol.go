// Package protocol defines the messages sys0 clients and the sys1 node
// exchange. Everything travels as JSON over TLS 1.3, with the node's
// certificate pinned by the fingerprint in the join code. See
// docs/PROTOCOL.md for the full description.
package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const APIVersion = 1

// HeartbeatInterval is how often a client reports in. A client silent for
// OfflineAfter is considered gone and its work is moved elsewhere.
const (
	HeartbeatInterval = 5 * time.Second
	OfflineAfter      = 3 * HeartbeatInterval
)

// MaxCheckpoint caps the fast-save state a task can hand back to the node.
const MaxCheckpoint = 8 << 20

// Trust is what a machine's owner allows on it.
type Trust string

const (
	// TrustOwner: "this is my machine". Native programs and less
	// restricted containers may run here.
	TrustOwner Trust = "owner"
	// TrustCommunity: a machine shared by someone else. Only hardened,
	// sandboxed containers run here.
	TrustCommunity Trust = "community"
)

func (t Trust) Valid() bool { return t == TrustOwner || t == TrustCommunity }

// Resources is a machine's spare capacity as last measured.
type Resources struct {
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	Hostname       string   `json:"hostname"`
	CPUCores       int      `json:"cpuCores"`
	CPUIdlePercent float64  `json:"cpuIdlePercent"`
	MemTotal       uint64   `json:"memTotal"`
	MemFree        uint64   `json:"memFree"`
	DiskTotal      uint64   `json:"diskTotal"`
	DiskFree       uint64   `json:"diskFree"`
	GPUs           []string `json:"gpus,omitempty"` // display names
	GPUInfo        []GPU    `json:"gpuInfo,omitempty"`
	OnBattery      bool     `json:"onBattery"`
	Runtimes       []string `json:"runtimes,omitempty"` // "docker", "podman", "exec"
}

// GPU is one graphics or AI accelerator.
type GPU struct {
	Vendor string `json:"vendor"` // nvidia, amd, intel, apple, other
	Name   string `json:"name"`
	VRAMMB int    `json:"vramMB"`           // total memory (Apple: the share usable by the GPU)
	FreeMB int    `json:"freeMB,omitempty"` // 0 when the driver doesn't say
}

// Avail is the GPU memory a task can count on.
func (g GPU) Avail() int {
	if g.FreeMB > 0 {
		return g.FreeMB
	}
	return g.VRAMMB
}

// UsableGPUs lists GPUs a task of this kind can actually use here.
// Containers reach NVIDIA GPUs through the NVIDIA container toolkit, and
// AMD and Intel GPUs through /dev on Linux; Docker on macOS and Windows
// cannot pass Apple, AMD or Intel GPUs through. Native programs use any.
func (r Resources) UsableGPUs(kind Kind) []GPU {
	var out []GPU
	for _, g := range r.GPUInfo {
		if kind == KindExec || g.Vendor == "nvidia" || ((g.Vendor == "amd" || g.Vendor == "intel") && r.OS == "linux") {
			out = append(out, g)
		}
	}
	return out
}

// BestGPU is the usable GPU with the most memory available.
func (r Resources) BestGPU(kind Kind) (GPU, bool) {
	var best GPU
	ok := false
	for _, g := range r.UsableGPUs(kind) {
		if !ok || g.Avail() > best.Avail() {
			best, ok = g, true
		}
	}
	return best, ok
}

func (r Resources) HasRuntime(name string) bool {
	for _, x := range r.Runtimes {
		if x == name {
			return true
		}
	}
	return false
}

func (r Resources) HasContainers() bool {
	return r.HasRuntime("docker") || r.HasRuntime("podman")
}

// Kind is how a task runs.
type Kind string

const (
	KindContainer Kind = "container" // an OCI image via docker or podman
	KindExec      Kind = "exec"      // a native program; owner machines only
)

// TaskSpec describes what to run. Marketplace packages carry one.
type TaskSpec struct {
	Kind    Kind              `json:"kind"`
	Image   string            `json:"image,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Ports   []string          `json:"ports,omitempty"`   // "host:container", e.g. "8096:8096"
	Volumes []string          `json:"volumes,omitempty"` // app data, snapshotted to pooled storage so it moves with the app
	// LocalVolumes stay on the machine the app runs on: caches, and big
	// media libraries you already keep somewhere. Containers only.
	LocalVolumes    []string `json:"localVolumes,omitempty"`
	SnapshotSeconds int      `json:"snapshotSeconds,omitempty"` // how often volumes are snapshotted (default 60)
	Network         bool     `json:"network"`
	CPU             float64  `json:"cpu,omitempty"`       // cores, 0 = no limit
	MemoryMB        int      `json:"memoryMB,omitempty"`  // 0 = no limit
	GPU             bool     `json:"gpu,omitempty"`       // prefer and use a GPU when available
	MinVRAMMB       int      `json:"minVramMB,omitempty"` // only run where a usable GPU has this much free memory
	Shards          int      `json:"shards,omitempty"`    // a batch job of this many pieces (see docs/BATCH.md)
	Service         bool     `json:"service,omitempty"`   // runs forever and is restarted on exit
	OS              []string `json:"os,omitempty"`        // exec only: OSes the program runs on
}

var (
	portRe  = regexp.MustCompile(`^[0-9]{1,5}:[0-9]{1,5}(/(tcp|udp))?$`)
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)
	envRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pathRe  = regexp.MustCompile(`^/[a-zA-Z0-9._/-]*$`)
)

// Validate rejects specs that are malformed or could smuggle options into
// the container runtime's command line.
func (s TaskSpec) Validate() error {
	switch s.Kind {
	case KindContainer:
		if !imageRe.MatchString(s.Image) {
			return fmt.Errorf("invalid image %q", s.Image)
		}
	case KindExec:
		if len(s.Command) == 0 {
			return errors.New("exec task needs a command")
		}
		if len(s.Ports) > 0 || len(s.LocalVolumes) > 0 {
			return errors.New("ports and local volumes apply to containers only")
		}
	default:
		return fmt.Errorf("unknown kind %q", s.Kind)
	}
	for _, p := range s.Ports {
		if !portRe.MatchString(p) {
			return fmt.Errorf("invalid port mapping %q", p)
		}
	}
	if len(s.Ports) > 0 && !s.Network {
		return errors.New("ports need network permission")
	}
	seen := map[string]bool{}
	for _, v := range append(append([]string(nil), s.Volumes...), s.LocalVolumes...) {
		if !pathRe.MatchString(v) || strings.Contains(v, "..") || strings.Contains(v, ":") {
			return fmt.Errorf("invalid volume path %q", v)
		}
		if seen[v] {
			return fmt.Errorf("volume %q listed twice", v)
		}
		seen[v] = true
	}
	if s.Shards < 0 {
		return errors.New("shards cannot be negative")
	}
	if s.MinVRAMMB < 0 {
		return errors.New("minVramMB cannot be negative")
	}
	if s.SnapshotSeconds < 0 {
		return errors.New("snapshotSeconds cannot be negative")
	}
	for k := range s.Env {
		if !envRe.MatchString(k) {
			return fmt.Errorf("invalid env name %q", k)
		}
	}
	if s.CPU < 0 || s.MemoryMB < 0 {
		return errors.New("limits cannot be negative")
	}
	return nil
}

// Task is one assignment from the node to a client.
type Task struct {
	ID         string   `json:"id"`
	Attempt    int      `json:"attempt"`
	Spec       TaskSpec `json:"spec"`
	Checkpoint []byte   `json:"checkpoint,omitempty"` // fast-saved state from a previous machine
	// For apps with volumes: the key for this app's data, and the latest
	// snapshot to restore before starting (empty for a fresh app).
	DataKey []byte `json:"dataKey,omitempty"`
	Restore string `json:"restore,omitempty"`
}

type RegisterRequest struct {
	APIVersion int       `json:"apiVersion"`
	JoinToken  string    `json:"joinToken"`
	Name       string    `json:"name"`
	Trust      Trust     `json:"trust"`
	Version    string    `json:"version"`
	Resources  Resources `json:"resources"`
}

type RegisterResponse struct {
	AgentID string `json:"agentId"`
	Secret  string `json:"secret"`
}

// RunningTask identifies a task attempt the client is currently running.
type RunningTask struct {
	ID      string `json:"id"`
	Attempt int    `json:"attempt"`
}

type HeartbeatRequest struct {
	Resources Resources     `json:"resources"`
	Running   []RunningTask `json:"running"`
	Version   string        `json:"version"`
	Storage   StorageReport `json:"storage"`
}

type HeartbeatResponse struct {
	Assign []Task   `json:"assign,omitempty"`
	Cancel []string `json:"cancel,omitempty"`

	// Pooled storage work for this machine.
	Fetch     []string     `json:"fetch,omitempty"`  // download these chunks from the node and keep them
	Upload    []string     `json:"upload,omitempty"` // send these chunks to the node
	Drop      []string     `json:"drop,omitempty"`   // delete these chunks
	Inventory bool         `json:"inventory,omitempty"`
	Backups   []BackupJob  `json:"backups,omitempty"`
	Restores  []RestoreJob `json:"restores,omitempty"`
	// Hurry asks for the next heartbeat in about a second, because
	// storage work is in flight.
	Hurry bool `json:"hurry,omitempty"`
}

// StorageReport is a machine's share of pooled storage, plus the outcome of
// storage work since its last heartbeat.
type StorageReport struct {
	QuotaBytes int64    `json:"quotaBytes"`
	UsedBytes  int64    `json:"usedBytes"`
	Chunks     int      `json:"chunks"`
	Stored     []string `json:"stored,omitempty"`  // fetched and kept
	Missing    []string `json:"missing,omitempty"` // asked to upload, but not here (or corrupt)
}

// Inventory lists every chunk a machine holds.
type Inventory struct {
	Chunks []string `json:"chunks"`
}

// SnapshotReport tells the node a snapshot was taken. Every chunk in it
// has been uploaded already, or was already in the pool.
type SnapshotReport struct {
	Owner   string   `json:"owner"` // "task:<id>" or "backup:<id>"
	Attempt int      `json:"attempt,omitempty"`
	Root    string   `json:"root"`
	Chunks  []string `json:"chunks"`
	Size    int64    `json:"size"`
	Files   int      `json:"files"`
}

// BackupJob asks an owner machine to keep a folder backed up to the pool.
type BackupJob struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Key             []byte `json:"key"`
	RunNow          int    `json:"runNow"` // increases when the user clicks "Back up now"
}

// RestoreJob asks an owner machine to restore a snapshot into a new folder.
type RestoreJob struct {
	ID   string `json:"id"`
	Root string `json:"root"`
	Dest string `json:"dest"`
	Key  []byte `json:"key"`
}

type RestoreResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Files   int    `json:"files,omitempty"`
}

// Outcome is how a task attempt ended on a client.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed" // job finished cleanly
	OutcomeStopped   Outcome = "stopped"   // cancelled by the node
	OutcomeFailed    Outcome = "failed"    // crashed too many times; move it
	OutcomeRefused   Outcome = "refused"   // this machine will not run it
)

type Result struct {
	TaskID     string  `json:"taskId"`
	Attempt    int     `json:"attempt"`
	Outcome    Outcome `json:"outcome"`
	ExitCode   int     `json:"exitCode"`
	Message    string  `json:"message,omitempty"`
	Output     string  `json:"output,omitempty"` // tail of the output
	Checkpoint []byte  `json:"checkpoint,omitempty"`
}

// JoinCode is everything a client needs to join a node, as one string.
type JoinCode struct {
	Addr        string `json:"a"` // host:port of the node's client API
	Fingerprint string `json:"f"` // SHA-256 of the node's TLS certificate, hex
	Token       string `json:"t"`
}

const joinPrefix = "sys1-join:"

func (j JoinCode) String() string {
	b, _ := json.Marshal(j)
	return joinPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func ParseJoinCode(s string) (JoinCode, error) {
	var j JoinCode
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, joinPrefix) {
		return j, errors.New("not a sys1 join code (it should start with sys1-join:)")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, joinPrefix))
	if err == nil {
		err = json.Unmarshal(b, &j)
	}
	if err != nil {
		return j, errors.New("join code is damaged; copy it again from the node")
	}
	if j.Addr == "" || len(j.Fingerprint) != 64 || j.Token == "" {
		return j, errors.New("join code is incomplete; copy it again from the node")
	}
	return j, nil
}
