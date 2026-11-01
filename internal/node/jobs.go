package node

import (
	"errors"
	"fmt"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Batch jobs: one piece of work split into shards. Each shard is an
// ordinary task (placed, watched, retried and moved like any app) that
// also gets SYS1_SHARD_INDEX and SYS1_SHARD_COUNT. Shards that declare
// volumes leave their output in pooled storage, from where "collect"
// restores every shard's output into one folder.

// MaxShards bounds one job.
const MaxShards = 10000

type Job struct {
	ID       string             `json:"id"`
	Kind     string             `json:"kind,omitempty"` // "" (batch) or "group"
	Head     string             `json:"head,omitempty"` // group: the head task
	HeadSpec *protocol.TaskSpec `json:"headSpec,omitempty"`
	Package  string             `json:"package"`
	Title    string             `json:"title"`
	Shards   int                `json:"shards"`
	Children []string           `json:"children"`
	Created  time.Time          `json:"created"`
	// Verified jobs: replicas per piece, and the accepted output per piece.
	Verify   int            `json:"verify,omitempty"`
	Accepted map[int]string `json:"accepted,omitempty"` // shard -> snapshot root
	Rejected map[int]string `json:"rejected,omitempty"` // shard -> why no agreement
}

// JobProgress counts a job's shards by state.
type JobProgress struct {
	Total, Done, Running, Waiting, Failed, Stopped int
	Saved                                          int // shards whose output is in the pool
}

func (n *Node) progressLocked(j *Job) JobProgress {
	if j.Verify > 0 {
		return n.verifiedProgressLocked(j)
	}
	p := JobProgress{Total: len(j.Children)}
	for _, id := range j.Children {
		t := n.tasks[id]
		if t == nil {
			continue
		}
		switch t.State {
		case StateDone:
			p.Done++
		case StateRunning, StateAssigned, StateStopping:
			p.Running++
		case StateFailed:
			p.Failed++
		case StateStopped:
			p.Stopped++
		default:
			p.Waiting++
		}
		if t.Snapshot != "" {
			p.Saved++
		}
	}
	return p
}

// queueJobLocked splits spec into spec.Shards tasks.
func (n *Node) queueJobLocked(pkg, title string, spec protocol.TaskSpec) (*Job, error) {
	if spec.Shards < 1 || spec.Shards > MaxShards {
		return nil, fmt.Errorf("a job has 1 to %d shards", MaxShards)
	}
	if spec.Service {
		return nil, errors.New("a batch job's shards must finish; it can't be a service")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	j := &Job{ID: randomToken()[:12], Package: pkg, Title: title, Shards: spec.Shards, Created: time.Now(), Verify: spec.Verify}
	replicas := max(spec.Verify, 1)
	for k := 0; k < spec.Shards*replicas; k++ {
		i, r := k/replicas, k%replicas
		s := spec
		s.Shards, s.Verify = 0, 0
		s.Env = map[string]string{}
		for k, v := range spec.Env {
			s.Env[k] = v
		}
		s.Env["SYS1_SHARD_INDEX"] = strconv.Itoa(i)
		s.Env["SYS1_SHARD_COUNT"] = strconv.Itoa(spec.Shards)
		t := &Task{ID: randomToken()[:12], Package: pkg, Title: fmt.Sprintf("%s · shard %d/%d", title, i+1, spec.Shards),
			Spec: s, State: StateQueued, Attempt: 1, Created: time.Now(), Message: "Finding the best machine", Parent: j.ID, Shard: i, Replica: r}
		if spec.Verify > 0 {
			t.SpreadKey = fmt.Sprintf("%s:%d", j.ID, i)
		}
		n.tasks[t.ID] = t
		n.saveTaskLocked(t)
		j.Children = append(j.Children, t.ID)
	}
	n.jobs[j.ID] = j
	n.persistLocked(event{Job: j})
	n.scheduleLocked(time.Now())
	return j, nil
}

// StopJob stops every unfinished shard. Once all shards have finished,
// it clears the job and its shards, including their saved output.
func (n *Node) StopJob(id string) error {
	n.mu.Lock()
	j := n.jobs[id]
	n.mu.Unlock()
	if j == nil {
		return errors.New("no such job")
	}
	n.mu.Lock()
	p := n.progressLocked(j)
	finished := p.Running+p.Waiting == 0
	n.mu.Unlock()
	for _, c := range j.Children {
		n.StopTask(c) // stops running shards, or clears finished ones
	}
	if finished {
		n.mu.Lock()
		delete(n.jobs, id)
		n.persistLocked(event{DelJob: id})
		n.mu.Unlock()
	}
	return nil
}

// CollectJob restores every shard's saved output into dest/shard-NNNN on
// one of your machines.
func (n *Node) CollectJob(id, agentID, dest string) (int, error) {
	n.mu.Lock()
	j := n.jobs[id]
	var roots []string
	var shards []int
	if j != nil && j.Verify > 0 {
		for s := 0; s < j.Shards; s++ {
			if root := j.Accepted[s]; root != "" {
				roots, shards = append(roots, root), append(shards, s)
			}
		}
	} else if j != nil {
		for _, c := range j.Children {
			if t := n.tasks[c]; t != nil && t.Snapshot != "" {
				roots, shards = append(roots, t.Snapshot), append(shards, t.Shard)
			}
		}
	}
	n.mu.Unlock()
	if j == nil {
		return 0, errors.New("no such job")
	}
	if len(roots) == 0 {
		return 0, errors.New("no shard has saved output yet (shards save output from their volumes)")
	}
	for i, r := range roots {
		if _, err := n.StartRestore(r, agentID, joinDest(dest, fmt.Sprintf("shard-%04d", shards[i]))); err != nil {
			return i, err
		}
	}
	return len(roots), nil
}

// joinDest joins with the separator the destination already uses, since
// the node may run on a different OS than the machine restoring.
func joinDest(dest, name string) string {
	for i := len(dest) - 1; i >= 0; i-- {
		if dest[i] == '\\' {
			return dest + `\` + name
		}
		if dest[i] == '/' {
			break
		}
	}
	return path.Join(dest, name)
}

type jobView struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Verify   int         `json:"verify"`
	Package  string      `json:"package"`
	Title    string      `json:"title"`
	Shards   int         `json:"shards"`
	Created  time.Time   `json:"created"`
	Progress JobProgress `json:"progress"`
	Machines int         `json:"machines"` // distinct machines running shards right now
}

func (n *Node) jobViewsLocked() []jobView {
	out := []jobView{}
	for _, j := range n.jobs {
		m := map[string]bool{}
		for _, c := range j.Children {
			if t := n.tasks[c]; t != nil && t.AgentID != "" && (t.State == StateRunning || t.State == StateAssigned) {
				m[t.AgentID] = true
			}
		}
		out = append(out, jobView{j.ID, j.Kind, j.Verify, j.Package, j.Title, j.Shards, j.Created, n.progressLocked(j), len(m)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out
}

// ---- groups ----

// queueGroupLocked starts a group: the workers are queued now, the head
// waits until they are all running.
func (n *Node) queueGroupLocked(pkg, title string, spec protocol.TaskSpec) (*Job, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	g := spec.Group
	head := spec
	head.Group = nil
	j := &Job{ID: randomToken()[:12], Kind: "group", Package: pkg, Title: title, Shards: g.Workers, HeadSpec: &head, Created: time.Now()}
	for i := 0; i < g.Workers; i++ {
		port := strconv.Itoa(g.PeerPort + i)
		w := g.Worker
		w.Network, w.Service = true, true
		w.Env = map[string]string{}
		for k, v := range g.Worker.Env {
			w.Env[k] = v
		}
		w.Env["SYS1_PEER_PORT"] = port
		w.Env["SYS1_SHARD_INDEX"] = strconv.Itoa(i)
		w.Env["SYS1_SHARD_COUNT"] = strconv.Itoa(g.Workers)
		w.Command = substitute(g.Worker.Command, "${SYS1_PEER_PORT}", port)
		if w.Kind == protocol.KindContainer {
			w.Ports = append(append([]string(nil), g.Worker.Ports...), port+":"+port)
		}
		t := &Task{ID: randomToken()[:12], Package: pkg, Title: fmt.Sprintf("%s · worker %d/%d", title, i+1, g.Workers),
			Spec: w, State: StateQueued, Attempt: 1, Created: time.Now(), Message: "Finding a machine", Parent: j.ID, Shard: i, SpreadKey: j.ID}
		n.tasks[t.ID] = t
		n.saveTaskLocked(t)
		j.Children = append(j.Children, t.ID)
	}
	h := &Task{ID: randomToken()[:12], Package: pkg, Title: title + " · head", Spec: head, State: StateWaiting, Attempt: 1,
		Created: time.Now(), Message: fmt.Sprintf("Waiting for %d worker(s) to start", g.Workers), Parent: j.ID, Shard: -1, SpreadKey: j.ID}
	n.tasks[h.ID] = h
	n.saveTaskLocked(h)
	j.Head = h.ID
	j.Children = append(j.Children, h.ID)
	n.jobs[j.ID] = j
	n.persistLocked(event{Job: j})
	n.scheduleLocked(time.Now())
	return j, nil
}

func substitute(args []string, from, to string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, from, to)
	}
	return out
}

// groupsLocked starts each group's head once every worker runs, and
// restarts it whenever the set of workers changes.
func (n *Node) groupsLocked(now time.Time) {
	for _, j := range n.jobs {
		if j.Kind != "group" {
			continue
		}
		head := n.tasks[j.Head]
		if head == nil {
			continue
		}
		if terminal(head.State) {
			// The head finished or was stopped: so is the group.
			for _, c := range j.Children {
				if t := n.tasks[c]; t != nil && c != j.Head && !terminal(t.State) && t.State != StateStopping {
					if t.AgentID == "" {
						t.State, t.Message = StateStopped, "Group finished"
					} else {
						t.State, t.Message = StateStopping, "Group finished"
					}
					n.saveTaskLocked(t)
				}
			}
			continue
		}
		var peers []string
		ready := true
		for _, c := range j.Children {
			if c == j.Head {
				continue
			}
			w := n.tasks[c]
			a := n.agents[w.AgentID]
			if w.State != StateRunning || a == nil || a.Resources.Addr == "" {
				ready = false
				break
			}
			peers = append(peers, net.JoinHostPort(a.Resources.Addr, w.Spec.Env["SYS1_PEER_PORT"]))
		}
		key := strings.Join(peers, ",")
		switch {
		case head.State == StateWaiting && ready:
			s := *j.HeadSpec
			s.Env = map[string]string{"SYS1_PEERS": key}
			for k, v := range j.HeadSpec.Env {
				s.Env[k] = v
			}
			s.Command = substitute(j.HeadSpec.Command, "${SYS1_PEERS}", key)
			head.Spec, head.State, head.Message = s, StateQueued, "Workers ready; starting"
			n.saveTaskLocked(head)
		case head.State != StateWaiting && head.State != StateStopping && (!ready || head.Spec.Env["SYS1_PEERS"] != key):
			head.AgentID = ""
			head.Attempt++
			head.State, head.Message = StateWaiting, "A worker moved; restarting with the new set"
			n.saveTaskLocked(head)
		}
	}
}

// spreadBusyLocked lists, per spread key, the machines already running a
// task with that key, so those tasks land on different machines.
func (n *Node) spreadBusyLocked() map[string]map[string]bool {
	busy := map[string]map[string]bool{}
	for _, t := range n.tasks {
		if t.SpreadKey == "" || t.AgentID == "" || terminal(t.State) {
			continue
		}
		if busy[t.SpreadKey] == nil {
			busy[t.SpreadKey] = map[string]bool{}
		}
		busy[t.SpreadKey][t.AgentID] = true
	}
	return busy
}

// ---- verified jobs ----

// Machines that disagree this often are kept off verified work.
const disagreeLimit = 3

// verifyLocked accepts a piece once enough of its replicas produced the
// same output, marks machines that produced something else, and adds a
// replica on another machine when the finished ones don't agree.
func (n *Node) verifyLocked(now time.Time) {
	for _, j := range n.jobs {
		if j.Verify == 0 {
			continue
		}
		byShard := map[int][]*Task{}
		for _, c := range j.Children {
			if t := n.tasks[c]; t != nil {
				byShard[t.Shard] = append(byShard[t.Shard], t)
			}
		}
		changed := false
		for s := 0; s < j.Shards; s++ {
			if j.Accepted[s] != "" || j.Rejected[s] != "" {
				continue
			}
			reps := byShard[s]
			votes := map[string][]*Task{}
			active := 0
			for _, t := range reps {
				if !terminal(t.State) {
					active++
				}
				if t.State == StateDone {
					if snap := n.pool.snapshots[t.Snapshot]; snap != nil && snap.Content != "" {
						votes[snap.Content] = append(votes[snap.Content], t)
					}
				}
			}
			winner := ""
			for h, ts := range votes {
				if len(ts) >= j.Verify {
					winner = h
				}
			}
			if winner != "" {
				if j.Accepted == nil {
					j.Accepted = map[int]string{}
				}
				j.Accepted[s] = votes[winner][0].Snapshot
				changed = true
				for h, ts := range votes {
					if h == winner {
						continue
					}
					for _, t := range ts {
						if a := n.agents[t.AgentID]; a != nil {
							a.Disagreed++
							n.persistLocked(event{Agent: a})
							n.log.Warn("machine's output disagreed with other machines", "machine", a.Name, "job", j.Title, "piece", s)
						}
					}
				}
				for _, t := range reps {
					if !terminal(t.State) && t.State != StateStopping {
						if t.AgentID == "" {
							t.State = StateStopped
						} else {
							t.State = StateStopping
						}
						t.Message = "Not needed: piece already verified"
						n.saveTaskLocked(t)
					}
				}
				continue
			}
			if active > 0 {
				continue
			}
			if len(reps) >= j.Verify+2 {
				if j.Rejected == nil {
					j.Rejected = map[int]string{}
				}
				j.Rejected[s] = fmt.Sprintf("no %d machines agreed after %d tries", j.Verify, len(reps))
				changed = true
				continue
			}
			base := reps[0]
			t := &Task{ID: randomToken()[:12], Package: base.Package, Title: base.Title, Spec: base.Spec, State: StateQueued,
				Attempt: 1, Created: now, Message: "Checking with another machine", Parent: j.ID, Shard: s, Replica: len(reps), SpreadKey: base.SpreadKey}
			for _, r := range reps {
				if r.AgentID != "" {
					t.Excluded = append(t.Excluded, r.AgentID)
				}
			}
			n.tasks[t.ID] = t
			n.saveTaskLocked(t)
			j.Children = append(j.Children, t.ID)
			changed = true
		}
		if changed {
			n.persistLocked(event{Job: j})
		}
	}
}

func (n *Node) verifiedProgressLocked(j *Job) JobProgress {
	p := JobProgress{Total: j.Shards}
	state := map[int]string{}
	for _, c := range j.Children {
		t := n.tasks[c]
		if t == nil {
			continue
		}
		switch {
		case t.State == StateRunning || t.State == StateAssigned:
			state[t.Shard] = "running"
		case !terminal(t.State) && state[t.Shard] != "running":
			state[t.Shard] = "waiting"
		}
	}
	for s := 0; s < j.Shards; s++ {
		switch {
		case j.Accepted[s] != "":
			p.Done++
			p.Saved++
		case j.Rejected[s] != "":
			p.Failed++
		case state[s] == "running":
			p.Running++
		default:
			p.Waiting++
		}
	}
	return p
}
