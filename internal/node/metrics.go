package node

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/version"
)

// handleMetrics serves the pool's state in Prometheus' text format, for
// Prometheus, Grafana Agent or anything that reads it. It needs the
// dashboard key like the rest of the dashboard (a bearer token in
// Prometheus' scrape config).
func (n *Node) handleMetrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	var b strings.Builder
	metric := func(name, help, typ string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	q := func(s string) string { // label value
		return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	}

	n.mu.Lock()
	metric("sys1_build_info", "The node's version.", "gauge")
	fmt.Fprintf(&b, "sys1_build_info{version=\"%s\"} 1\n", q(version.Version))
	metric("sys1_up_seconds", "Seconds since the node started.", "gauge")
	fmt.Fprintf(&b, "sys1_up_seconds %d\n", int(now.Sub(n.started).Seconds()))

	agents := make([]*Agent, 0, len(n.agents))
	for _, a := range n.agents {
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	metric("sys1_machine_online", "1 if the machine is connected.", "gauge")
	for _, a := range agents {
		on := 0
		if n.online(a, now) {
			on = 1
		}
		fmt.Fprintf(&b, "sys1_machine_online{machine=\"%s\",os=\"%s\",trust=\"%s\",version=\"%s\"} %d\n",
			q(a.Name), q(a.Resources.OS), a.Trust, q(a.Version), on)
	}
	for _, m := range []struct {
		name, help string
		val        func(a *Agent) float64
	}{
		{"sys1_machine_cpu_cores", "CPU cores.", func(a *Agent) float64 { return float64(a.Resources.CPUCores) }},
		{"sys1_machine_cpu_idle_percent", "Idle CPU, percent.", func(a *Agent) float64 { return a.Resources.CPUIdlePercent }},
		{"sys1_machine_memory_free_bytes", "Memory available.", func(a *Agent) float64 { return float64(a.Resources.MemFree) }},
		{"sys1_machine_memory_total_bytes", "Memory installed.", func(a *Agent) float64 { return float64(a.Resources.MemTotal) }},
		{"sys1_machine_disk_free_bytes", "Disk free.", func(a *Agent) float64 { return float64(a.Resources.DiskFree) }},
		{"sys1_machine_disk_total_bytes", "Disk size.", func(a *Agent) float64 { return float64(a.Resources.DiskTotal) }},
		{"sys1_machine_on_battery", "1 while on battery (paused).", func(a *Agent) float64 { return b2f(a.Resources.OnBattery) }},
		{"sys1_machine_problems", "Problems the machine reports (Docker not running…).", func(a *Agent) float64 { return float64(len(a.Resources.Problems)) }},
		{"sys1_machine_auto_update", "1 if the machine updates itself.", func(a *Agent) float64 { return b2f(a.Resources.Updates == "automatic") }},
	} {
		metric(m.name, m.help, "gauge")
		for _, a := range agents {
			if !n.online(a, now) {
				continue
			}
			fmt.Fprintf(&b, "%s{machine=\"%s\"} %g\n", m.name, q(a.Name), m.val(a))
		}
	}

	states := map[string]int{}
	attempts := 0
	for _, t := range n.tasks {
		states[t.State]++
		if t.Attempt > 1 {
			attempts += t.Attempt - 1
		}
	}
	metric("sys1_apps", "Apps by state.", "gauge")
	for _, s := range []string{StateQueued, StateAssigned, StateRunning, StateStopping, StateDone, StateStopped, StateFailed, StateWaiting} {
		fmt.Fprintf(&b, "sys1_apps{state=\"%s\"} %d\n", s, states[s])
	}
	metric("sys1_app_restarts", "Extra attempts apps needed (crashes, moves) among apps the node still knows.", "gauge")
	fmt.Fprintf(&b, "sys1_app_restarts %d\n", attempts)
	n.mu.Unlock()

	metric("sys1_crashes", "Distinct sys0/sys1 crashes the node has seen.", "gauge")
	fmt.Fprintf(&b, "sys1_crashes %d\n", len(n.crashes.List()))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

func b2f(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
