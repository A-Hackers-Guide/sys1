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
	"net"
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
	Runtimes       []string `json:"runtimes,omitempty"`     // "docker", "podman", "exec"
	Addr           string   `json:"addr,omitempty"`         // address other machines can reach this one on
	OpenInternet   bool     `json:"openInternet,omitempty"` // the lender lets apps use the internet freely
	// Problems says, in plain words, what keeps this machine from running
	// some kinds of apps (Docker installed but not running, no permission…).
	Problems []string `json:"problems,omitempty"`
	// Updates is "automatic", or why this install doesn't update itself.
	Updates string `json:"updates,omitempty"`
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
	KindVM        Kind = "vm"        // a whole operating system under QEMU; owner machines only
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
	ShmMB           int      `json:"shmMB,omitempty"`     // shared memory for containers (desktops and browsers need it)
	GPU             bool     `json:"gpu,omitempty"`       // prefer and use a GPU when available
	MinVRAMMB       int      `json:"minVramMB,omitempty"` // only run where a usable GPU has this much free memory
	Service         bool     `json:"service,omitempty"`   // runs forever and is restarted on exit
	OwnerOnly       bool     `json:"ownerOnly,omitempty"` // only on machines whose owner said "mine" (e.g. unauthenticated network services)
	// Egress lists the internet hosts the app may reach over HTTP(S) when
	// it runs on a machine lent by someone else ("*.example.com" allowed).
	// "*" means it needs unrestricted internet, which lent machines only
	// give when their owner allows it.
	Egress []string `json:"egress,omitempty"`
	OS     []string `json:"os,omitempty"`     // exec only: OSes the program runs on
	Shards int      `json:"shards,omitempty"` // a batch job of this many pieces (see docs/BATCH.md)
	Verify int      `json:"verify,omitempty"` // batch: run each piece on this many machines and accept matching output
	// Group makes this spec the head of a group of workers on other
	// machines (see docs/BATCH.md).
	Group *GroupSpec `json:"group,omitempty"`
	// VM describes the virtual machine for kind "vm" (see docs/VMS.md).
	VM *VMSpec `json:"vm,omitempty"`
	// Build makes a container's image from a Git repository's Dockerfile
	// on the machine that runs it (see docs/GITHUB.md).
	Build *BuildSpec `json:"build,omitempty"`
}

// BuildSpec builds a container image from a public Git repository.
type BuildSpec struct {
	Repo       string `json:"repo"`                 // "owner/name" on GitHub
	Branch     string `json:"branch,omitempty"`     // followed for new commits (default: the repository's default branch)
	Commit     string `json:"commit,omitempty"`     // the commit to build, set by the node
	Dir        string `json:"dir,omitempty"`        // folder holding the build context, default the top
	Dockerfile string `json:"dockerfile,omitempty"` // path inside Dir, default "Dockerfile"
}

// idRe is every ID the node hands out (random hex) or tests use. IDs name
// folders on the client, so nothing that could leave one is allowed.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidID reports whether id is safe to use as a task, backup or restore ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	relRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

func (b *BuildSpec) validate() error {
	if !repoRe.MatchString(b.Repo) || strings.Contains(b.Repo, "..") {
		return fmt.Errorf("repository %q: use owner/name", b.Repo)
	}
	if b.Branch != "" && (!branchRe.MatchString(b.Branch) || strings.Contains(b.Branch, "..") || strings.HasPrefix(b.Branch, "-")) {
		return fmt.Errorf("invalid branch %q", b.Branch)
	}
	if b.Commit != "" && !commitRe.MatchString(b.Commit) {
		return errors.New("commit must be a full 40-character hash")
	}
	for _, p := range []string{b.Dir, b.Dockerfile} {
		if p != "" && (!relRe.MatchString(p) || strings.Contains(p, "..")) {
			return fmt.Errorf("invalid path %q in the repository", p)
		}
	}
	return nil
}

// GitContext is what `docker build` fetches: the repository at the commit,
// in the build folder.
func (b *BuildSpec) GitContext() string {
	ctx := "https://github.com/" + b.Repo + ".git#" + b.Commit
	if b.Dir != "" {
		ctx += ":" + b.Dir
	}
	return ctx
}

// VMSpec is a virtual machine run with QEMU. Its disk is a copy-on-write
// layer over a downloaded base image; with a pooled volume it moves to
// another machine together with the VM.
type VMSpec struct {
	Image     string `json:"image"`               // https URL of a disk image (qcow2 or raw)
	SHA256    string `json:"sha256"`              // of the image, checked after download
	Arch      string `json:"arch,omitempty"`      // x86_64 (default) or aarch64
	CPUs      int    `json:"cpus,omitempty"`      // default 2
	DiskGB    int    `json:"diskGB,omitempty"`    // size the guest sees, default 20
	CloudInit string `json:"cloudInit,omitempty"` // cloud-init user-data for the first boot
	Screen    bool   `json:"screen,omitempty"`    // a display, viewable from the dashboard
}

// GroupSpec describes a group's workers. Worker i runs on its own machine
// and listens on PeerPort+i; the head starts once all workers run and gets
// their addresses in SYS1_PEERS.
type GroupSpec struct {
	Workers  int      `json:"workers"`
	PeerPort int      `json:"peerPort"`
	Worker   TaskSpec `json:"worker"`
}

var (
	portRe  = regexp.MustCompile(`^[0-9]{1,5}:[0-9]{1,5}(/(tcp|udp))?$`)
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)
	envRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pathRe  = regexp.MustCompile(`^/[a-zA-Z0-9._/-]*$`)
	shaRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate rejects specs that are malformed or could smuggle options into
// the container runtime's command line.
func (s TaskSpec) Validate() error {
	if s.Build != nil && s.Kind != KindContainer {
		return errors.New("only containers can be built from a repository")
	}
	switch s.Kind {
	case KindContainer:
		if s.Build != nil {
			if err := s.Build.validate(); err != nil {
				return err
			}
			if !s.OwnerOnly {
				return errors.New("building from a repository runs its build steps, so it runs only on your own machines (ownerOnly)")
			}
		} else if !imageRe.MatchString(s.Image) {
			return fmt.Errorf("invalid image %q", s.Image)
		}
	case KindExec:
		if len(s.Command) == 0 {
			return errors.New("exec task needs a command")
		}
		if len(s.Ports) > 0 || len(s.LocalVolumes) > 0 {
			return errors.New("ports and local volumes apply to containers only")
		}
	case KindVM:
		v := s.VM
		if v == nil {
			return errors.New("a vm needs its vm settings")
		}
		if !strings.HasPrefix(v.Image, "https://") || strings.ContainsAny(v.Image, " ,\n") {
			return errors.New("vm image must be an https URL")
		}
		if !shaRe.MatchString(v.SHA256) {
			return errors.New("vm sha256 must be 64 hex characters")
		}
		if v.Arch != "" && v.Arch != "x86_64" && v.Arch != "aarch64" {
			return fmt.Errorf("vm arch %q: use x86_64 or aarch64", v.Arch)
		}
		if v.CPUs < 0 || v.CPUs > 64 || v.DiskGB < 0 || v.DiskGB > 4096 {
			return errors.New("vm cpus or diskGB out of range")
		}
		if s.MemoryMB < 128 {
			return errors.New("a vm needs memoryMB of at least 128")
		}
		if len(s.LocalVolumes) > 0 || len(s.Volumes) > 1 {
			return errors.New("a vm can have one pooled volume (its disk) and no local volumes")
		}
		for _, p := range s.Ports {
			if strings.HasSuffix(p, "/udp") {
				return errors.New("vm port forwards are tcp only")
			}
		}
	default:
		return fmt.Errorf("unknown kind %q", s.Kind)
	}
	if s.VM != nil && s.Kind != KindVM {
		return errors.New("vm settings are only for kind vm")
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
	if s.Verify != 0 && (s.Verify < 2 || s.Verify > 5 || s.Shards == 0 || len(s.Volumes) == 0) {
		return errors.New("verify is 2 to 5, for batch jobs whose pieces write output to a volume")
	}
	if g := s.Group; g != nil {
		if s.Shards > 0 {
			return errors.New("a package is either a batch job or a group, not both")
		}
		if g.Workers < 1 || g.Workers > 64 {
			return errors.New("a group has 1 to 64 workers")
		}
		if g.PeerPort < 1024 || g.PeerPort+g.Workers > 65535 {
			return errors.New("peerPort must leave room for one port per worker, between 1024 and 65535")
		}
		if g.Worker.Group != nil || g.Worker.Shards > 0 {
			return errors.New("workers can't be groups or batch jobs themselves")
		}
		if err := g.Worker.Validate(); err != nil {
			return fmt.Errorf("worker: %w", err)
		}
	}
	for _, h := range s.Egress {
		if h != "*" && !validEgressHost(h) {
			return fmt.Errorf("invalid egress host %q", h)
		}
	}
	if s.ShmMB < 0 || s.ShmMB > 16384 {
		return errors.New("shmMB out of range")
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
	// Trust is what the node granted, decided by the join code used.
	Trust Trust `json:"trust,omitempty"`
}

// RunningTask identifies a task attempt the client is currently running.
type RunningTask struct {
	ID      string `json:"id"`
	Attempt int    `json:"attempt"`
	// Screen is where a VM's display can be viewed, when it has one.
	Screen *Screen `json:"screen,omitempty"`
}

// Screen is a VM display: VNC over WebSocket, with a password.
type Screen struct {
	Port     int    `json:"port"` // WebSocket port on the machine's address
	Password string `json:"password"`
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
	// NodeAddrs is every address the node can be reached on right now, so
	// clients keep up when the node's addresses change.
	NodeAddrs []string `json:"nodeAddrs,omitempty"`
	// UpdateNow changes when the pool's owner asks every machine to check
	// for a new version right away.
	UpdateNow int `json:"updateNow,omitempty"`

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
	// ContentHash fingerprints the plain contents, so results from
	// different machines can be compared without decrypting anything.
	ContentHash string `json:"contentHash,omitempty"`
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
	Addr        string   `json:"a"`           // host:port of the node's client API
	More        []string `json:"x,omitempty"` // other addresses to try: VPN, public
	Fingerprint string   `json:"f"`           // SHA-256 of the node's TLS certificate, hex
	Token       string   `json:"t"`
}

// Addrs lists every address in the code, best first.
func (j JoinCode) Addrs() []string {
	return append([]string{j.Addr}, j.More...)
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

// Sandboxed reports whether an app runs behind the egress gateway on a
// machine: lent machines whose owner hasn't opened their internet, for
// apps that use the network.
func (s TaskSpec) Sandboxed(trust Trust, openInternet bool) bool {
	return s.Kind == KindContainer && s.Network && trust != TrustOwner && !openInternet
}

// SandboxProblem says why an app can't run sandboxed, or "" if it can.
func (s TaskSpec) SandboxProblem() string {
	for _, h := range s.Egress {
		if h == "*" {
			return "needs unrestricted internet, which this machine's owner doesn't allow"
		}
	}
	for _, p := range s.Ports {
		if strings.HasSuffix(p, "/udp") {
			return "uses UDP ports, which the sandbox gateway doesn't forward"
		}
	}
	return ""
}

// validEgressHost mirrors egress.ValidHost without importing it.
func validEgressHost(h string) bool {
	h = strings.TrimPrefix(h, "*.")
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") || net.ParseIP(h) != nil {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
