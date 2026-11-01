package node

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
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
	ID       string    `json:"id"`
	Package  string    `json:"package"`
	Title    string    `json:"title"`
	Shards   int       `json:"shards"`
	Children []string  `json:"children"`
	Created  time.Time `json:"created"`
}

// JobProgress counts a job's shards by state.
type JobProgress struct {
	Total, Done, Running, Waiting, Failed, Stopped int
	Saved                                          int // shards whose output is in the pool
}

func (n *Node) progressLocked(j *Job) JobProgress {
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
	j := &Job{ID: randomToken()[:12], Package: pkg, Title: title, Shards: spec.Shards, Created: time.Now()}
	for i := 0; i < spec.Shards; i++ {
		s := spec
		s.Shards = 0
		s.Env = map[string]string{}
		for k, v := range spec.Env {
			s.Env[k] = v
		}
		s.Env["SYS1_SHARD_INDEX"] = strconv.Itoa(i)
		s.Env["SYS1_SHARD_COUNT"] = strconv.Itoa(spec.Shards)
		t := &Task{ID: randomToken()[:12], Package: pkg, Title: fmt.Sprintf("%s · shard %d/%d", title, i+1, spec.Shards),
			Spec: s, State: StateQueued, Attempt: 1, Created: time.Now(), Message: "Finding the best machine", Parent: j.ID, Shard: i}
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
	if j != nil {
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
		out = append(out, jobView{j.ID, j.Package, j.Title, j.Shards, j.Created, n.progressLocked(j), len(m)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out
}
