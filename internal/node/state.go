package node

import (
	"encoding/json"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Task states.
const (
	StateQueued   = "queued"   // waiting for a machine
	StateAssigned = "assigned" // sent to a machine, not yet confirmed running
	StateRunning  = "running"
	StateStopping = "stopping" // the user asked to stop it
	StateDone     = "done"     // a job that finished cleanly
	StateStopped  = "stopped"
	StateFailed   = "failed" // gave up after MaxAttempts machines
)

// MaxAttempts is how many times a task may be (re)placed before the node
// gives up on it.
const MaxAttempts = 10

func terminal(state string) bool {
	return state == StateDone || state == StateStopped || state == StateFailed
}

type Agent struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	SecretHash string                 `json:"secretHash"`
	Trust      protocol.Trust         `json:"trust"`
	Version    string                 `json:"version"`
	Joined     time.Time              `json:"joined"`
	Local      bool                   `json:"local,omitempty"` // the node's own built-in client
	Resources  protocol.Resources     `json:"resources"`
	LastSeen   time.Time              `json:"-"`
	Storage    protocol.StorageReport `json:"-"`
}

type Task struct {
	ID         string            `json:"id"`
	Package    string            `json:"package"`
	Title      string            `json:"title"`
	Spec       protocol.TaskSpec `json:"spec"`
	State      string            `json:"state"`
	AgentID    string            `json:"agentId,omitempty"`
	Attempt    int               `json:"attempt"`
	Excluded   []string          `json:"excluded,omitempty"` // machines it crashed on
	Message    string            `json:"message,omitempty"`
	Output     string            `json:"output,omitempty"`
	ExitCode   int               `json:"exitCode"`
	Checkpoint []byte            `json:"checkpoint,omitempty"`
	Snapshot   string            `json:"snapshot,omitempty"` // latest volume snapshot root
	SnapshotAt time.Time         `json:"snapshotAt,omitempty"`
	Created    time.Time         `json:"created"`
	Updated    time.Time         `json:"updated"`
}

func (t *Task) excludes(agentID string) bool {
	for _, x := range t.Excluded {
		if x == agentID {
			return true
		}
	}
	return false
}

// event is one record in the node's journal. Replaying every event in
// order rebuilds the node's state after a crash or power cut.
type event struct {
	Agent     *Agent `json:"agent,omitempty"`
	Task      *Task  `json:"task,omitempty"`
	DelAgent  string `json:"delAgent,omitempty"`
	DelTask   string `json:"delTask,omitempty"`
	JoinToken string `json:"joinToken,omitempty"`

	Snapshot    *Snapshot `json:"snapshot,omitempty"`
	DelSnapshot string    `json:"delSnapshot,omitempty"`
	Backup      *Backup   `json:"backup,omitempty"`
	DelBackup   string    `json:"delBackup,omitempty"`
	Restore     *Restore  `json:"restore,omitempty"`
}

func (n *Node) replay(b []byte) error {
	var e event
	if err := json.Unmarshal(b, &e); err != nil {
		return err
	}
	switch {
	case e.Agent != nil:
		n.agents[e.Agent.ID] = e.Agent
	case e.Task != nil:
		n.tasks[e.Task.ID] = e.Task
	case e.DelAgent != "":
		delete(n.agents, e.DelAgent)
	case e.DelTask != "":
		delete(n.tasks, e.DelTask)
	case e.JoinToken != "":
		n.joinToken = e.JoinToken
	case e.Snapshot != nil:
		n.pool.snapshots[e.Snapshot.Root] = e.Snapshot
	case e.DelSnapshot != "":
		delete(n.pool.snapshots, e.DelSnapshot)
	case e.Backup != nil:
		n.pool.backups[e.Backup.ID] = e.Backup
	case e.DelBackup != "":
		delete(n.pool.backups, e.DelBackup)
	case e.Restore != nil:
		n.pool.restores[e.Restore.ID] = e.Restore
	}
	return nil
}

// persistLocked durably records an event before the change is acted on.
// Every so often the journal is compacted into a snapshot of current state.
func (n *Node) persistLocked(e event) {
	b, _ := json.Marshal(e)
	if err := n.j.Append(b); err != nil {
		n.log.Error("journal write failed", "err", err)
		return
	}
	if n.j.Len() > 2000 {
		n.compactLocked()
	}
}

func (n *Node) compactLocked() {
	var recs [][]byte
	add := func(e event) {
		b, _ := json.Marshal(e)
		recs = append(recs, b)
	}
	add(event{JoinToken: n.joinToken})
	for _, a := range n.agents {
		add(event{Agent: a})
	}
	for _, t := range n.tasks {
		add(event{Task: t})
	}
	for _, s := range n.pool.snapshots {
		add(event{Snapshot: s})
	}
	for _, b := range n.pool.backups {
		add(event{Backup: b})
	}
	for _, r := range n.pool.restores {
		add(event{Restore: r})
	}
	if err := n.j.Compact(recs); err != nil {
		n.log.Error("journal compaction failed", "err", err)
	}
}

func (n *Node) saveTaskLocked(t *Task) {
	t.Updated = time.Now()
	n.persistLocked(event{Task: t})
}
