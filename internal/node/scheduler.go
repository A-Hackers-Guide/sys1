package node

import (
	"strconv"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
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
	if s.OwnerOnly && a.Trust != protocol.TrustOwner {
		return false
	}
	if s.Sandboxed(a.Trust, a.Resources.OpenInternet) && s.SandboxProblem() != "" {
		return false
	}
	if s.MinVRAMMB > 0 {
		if g, ok := a.Resources.BestGPU(s.Kind); !ok || g.Avail() < s.MinVRAMMB {
			return false
		}
	}
	switch s.Kind {
	case protocol.KindVM:
		arch := "x86_64"
		if s.VM != nil && s.VM.Arch != "" {
			arch = s.VM.Arch
		}
		return a.Trust == protocol.TrustOwner && a.Resources.HasRuntime("qemu-"+arch)
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
	if s.GPU || s.MinVRAMMB > 0 {
		if g, ok := r.BestGPU(s.Kind); ok {
			sc += 100 + float64(g.Avail())/1024*5 // more free GPU memory wins
		}
	}
	if a.Local {
		sc -= 5 // leave the node's own machine for the GUI when possible
	}
	if s.Kind == protocol.KindVM && s.VM != nil {
		// A VM of the machine's own architecture gets hardware
		// acceleration; any other is emulated, far slower.
		native := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[r.Arch]
		want := s.VM.Arch
		if want == "" {
			want = "x86_64"
		}
		if native == want {
			sc += 50
		}
	}
	return sc
}

// scheduleLocked moves work along: tasks on machines that vanished go back
// in the queue, unconfirmed assignments time out, and queued tasks go to
// the best machine available.
func (n *Node) scheduleLocked(now time.Time) {
	n.groupsLocked(now)
	n.verifyLocked(now)
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

	busy := n.spreadBusyLocked()
	for _, t := range n.tasks {
		if t.State != StateQueued {
			continue
		}
		var best *Agent
		bestScore := -1.0
		for _, a := range n.agents {
			if !buildReady(t.Spec) || !n.online(a, now) || t.excludes(a.ID) || !canRun(a, t.Spec) || busy[t.SpreadKey][a.ID] {
				continue
			}
			if j := n.jobs[t.Parent]; j != nil && j.Verify > 0 && a.Disagreed >= disagreeLimit {
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
		if t.SpreadKey != "" {
			if busy[t.SpreadKey] == nil {
				busy[t.SpreadKey] = map[string]bool{}
			}
			busy[t.SpreadKey][best.ID] = true
		}
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
	if t.Attempt-t.AttemptBase > MaxAttempts {
		t.State, t.Message = StateFailed, "gave up after "+strconv.Itoa(MaxAttempts)+" attempts: "+why
	} else {
		t.State, t.Message = StateQueued, why
	}
	n.saveTaskLocked(t)
}
