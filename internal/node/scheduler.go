package node

import (
	"strconv"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
)

// perAgentLimit caps how many tasks the node places on one machine.
const perAgentLimit = 4

// assignTimeout is how long a machine has to confirm it started a task
// before the task is handed to someone else.
const assignTimeout = 4 * protocol.HeartbeatInterval

func (n *Node) online(a *Agent, now time.Time) bool {
	return now.Sub(a.LastSeen) < protocol.OfflineAfter
}

// canRun reports whether agent a may run spec at all.
func canRun(a *Agent, s protocol.TaskSpec) bool {
	switch s.Kind {
	case protocol.KindContainer:
		return a.Resources.HasContainers()
	case protocol.KindExec:
		if a.Trust != protocol.TrustOwner || !a.Resources.HasRuntime("exec") {
			return false
		}
		for _, os := range s.OS {
			if os == a.Resources.OS || os == "any" {
				return true
			}
		}
	}
	return false
}

// score ranks machines for a task: most spare CPU and memory wins, GPU
// machines win GPU tasks, busy machines and laptops on battery lose.
// A negative score means "does not fit right now".
func score(a *Agent, s protocol.TaskSpec, load int) float64 {
	r := a.Resources
	if load >= perAgentLimit || r.OnBattery {
		return -1
	}
	if s.MemoryMB > 0 && r.MemFree < uint64(s.MemoryMB)<<20 {
		return -1
	}
	if r.CPUIdlePercent < 10 {
		return -1
	}
	spareCores := float64(r.CPUCores) * r.CPUIdlePercent / 100
	spareGB := float64(r.MemFree) / (1 << 30)
	sc := spareCores*10 + spareGB*2 - float64(load)*15
	if s.GPU && resources.NvidiaGPU(r) {
		sc += 100
	}
	if a.Local {
		sc -= 5 // leave the node's own machine for the GUI when possible
	}
	return sc
}

// scheduleLocked moves work along: tasks on machines that vanished go back
// in the queue, unconfirmed assignments time out, and queued tasks go to
// the best machine available.
func (n *Node) scheduleLocked(now time.Time) {
	load := map[string]int{}
	for _, t := range n.tasks {
		if t.AgentID == "" || terminal(t.State) {
			continue
		}
		a := n.agents[t.AgentID]
		switch {
		case a == nil || !n.online(a, now):
			if t.State == StateStopping {
				t.State, t.Message = StateStopped, "machine went offline while stopping"
				n.saveTaskLocked(t)
			} else {
				n.requeueLocked(t, "machine went offline; moving it", false)
			}
			continue
		case t.State == StateAssigned && now.Sub(t.Updated) > assignTimeout:
			n.requeueLocked(t, "machine did not start it in time; moving it", true)
			continue
		}
		load[t.AgentID]++
	}

	for _, t := range n.tasks {
		if t.State != StateQueued {
			continue
		}
		var best *Agent
		bestScore := -1.0
		for _, a := range n.agents {
			if !n.online(a, now) || t.excludes(a.ID) || !canRun(a, t.Spec) {
				continue
			}
			if sc := score(a, t.Spec, load[a.ID]); sc > bestScore {
				best, bestScore = a, sc
			}
		}
		if best == nil {
			if t.Message != "Waiting for a machine with room for it" {
				t.Message = "Waiting for a machine with room for it"
				n.saveTaskLocked(t)
			}
			continue
		}
		t.State, t.AgentID, t.Message = StateAssigned, best.ID, "Sent to "+best.Name
		load[best.ID]++
		n.saveTaskLocked(t)
	}
}

// requeueLocked puts a task back in the queue for another machine. If
// blame is set, the machine it was on is excluded from future attempts.
func (n *Node) requeueLocked(t *Task, why string, blame bool) {
	if blame && t.AgentID != "" && !t.excludes(t.AgentID) {
		t.Excluded = append(t.Excluded, t.AgentID)
	}
	t.AgentID = ""
	t.Attempt++
	if t.Attempt > MaxAttempts {
		t.State, t.Message = StateFailed, "gave up after "+strconv.Itoa(MaxAttempts)+" attempts: "+why
	} else {
		t.State, t.Message = StateQueued, why
	}
	n.saveTaskLocked(t)
}
