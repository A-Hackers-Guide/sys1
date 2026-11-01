package node

import (
	"net/http"
	"os"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/crash"
)

// Crash reports: machines send the crashes of their sys0 to the node, the
// node adds its own, and the owner sees them on the dashboard. They go on
// to the project's crash intake, which files GitHub issues, unless the
// owner turns that off. See docs/CRASHES.md.

func (n *Node) crashIntake() string {
	if v, ok := os.LookupEnv("SYS1_CRASH_INTAKE"); ok {
		return v
	}
	if n.cfg.CrashIntake != "" {
		return n.cfg.CrashIntake
	}
	return crash.DefaultIntake
}

// AddCrash records a crash of this node's own sys1.
func (n *Node) AddCrash(r crash.Report) { n.crashes.Add(r) }

// Each machine may send a few reports a day; a machine stuck crashing
// doesn't need to report every time, and nobody can fill the store.
const crashReportsPerDay = 20

func (n *Node) handleCrash(w http.ResponseWriter, r *http.Request, a *Agent) {
	n.mu.Lock()
	day := time.Now().UTC().Format("2006-01-02")
	if n.crashQuota == nil || n.crashQuotaDay != day {
		n.crashQuota, n.crashQuotaDay = map[string]int{}, day
	}
	n.crashQuota[a.ID]++
	over := n.crashQuota[a.ID] > crashReportsPerDay
	n.mu.Unlock()
	if over {
		writeJSON(w, struct{}{}) // accepted and dropped, so the machine stops resending
		return
	}
	var rep crash.Report
	if !readJSON(w, r, 64<<10, &rep) {
		return
	}
	if rep.App != "sys0" || !rep.Valid(n.scrubber) {
		http.Error(w, "invalid crash report", http.StatusBadRequest)
		return
	}
	if rep.Count > 100 {
		rep.Count = 100
	}
	n.crashes.Add(rep)
	writeJSON(w, struct{}{})
}

type crashView struct {
	crash.Entry
	Link string `json:"link"`
}

func (n *Node) handleCrashes(w http.ResponseWriter, r *http.Request) {
	var list []crashView
	for _, e := range n.crashes.List() {
		list = append(list, crashView{Entry: e, Link: e.IssueLink()})
	}
	if list == nil {
		list = []crashView{}
	}
	writeJSON(w, map[string]any{
		"sending":   n.crashes.Sending(),
		"automatic": n.crashIntake() != "",
		"crashes":   list,
	})
}
