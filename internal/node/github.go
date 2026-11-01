package node

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Apps built from a GitHub repository follow their branch: the node asks
// GitHub for the branch's latest commit every few minutes and, when it
// changes, sends the app to be built and started again at that commit.
// It keeps the app's data, like any move.

const githubPoll = 3 * time.Minute

type githubWatch struct {
	mu   sync.Mutex
	api  string // GitHub API base URL (tests point it elsewhere)
	http *http.Client
	// Per repo@branch: the ETag of the last answer (conditional requests
	// don't count against GitHub's rate limit) and when it was asked.
	etag    map[string]string
	sha     map[string]string
	checked map[string]time.Time
	kick    chan struct{}
}

func newGithubWatch() *githubWatch {
	return &githubWatch{api: "https://api.github.com", http: &http.Client{Timeout: 20 * time.Second},
		etag: map[string]string{}, sha: map[string]string{}, checked: map[string]time.Time{}, kick: make(chan struct{}, 1)}
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// head returns the latest commit of repo's branch ("" = default branch).
func (g *githubWatch) head(ctx context.Context, repo, branch string) (string, error) {
	ref := branch
	if ref == "" {
		ref = "HEAD"
	}
	key := repo + "@" + ref
	req, _ := http.NewRequestWithContext(ctx, "GET", g.api+"/repos/"+repo+"/commits/"+ref, nil)
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("User-Agent", "sys1-node")
	g.mu.Lock()
	if e := g.etag[key]; e != "" {
		req.Header.Set("If-None-Match", e)
	}
	g.mu.Unlock()
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	g.mu.Lock()
	defer g.mu.Unlock()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return g.sha[key], nil
	case http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		sha := strings.TrimSpace(string(b))
		if !shaRe.MatchString(sha) {
			return "", errBadAnswer
		}
		g.etag[key], g.sha[key] = resp.Header.Get("ETag"), sha
		return sha, nil
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return "", errNoRepo
	default:
		return "", &githubError{resp.Status}
	}
}

type githubError struct{ status string }

func (e *githubError) Error() string { return "GitHub answered " + e.status }

var (
	errBadAnswer = &githubError{"with something that isn't a commit"}
	errNoRepo    = &githubError{"that the repository or branch doesn't exist (or is private)"}
)

// watchGitHub runs on the leader: it resolves the commit of newly deployed
// apps and redeploys apps whose branch moved.
func (n *Node) watchGitHub(ctx context.Context) {
	g := n.github
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-g.kick:
		}
		if !n.leading() {
			continue
		}
		type want struct{ repo, branch string }
		var wants []want
		n.mu.Lock()
		seen := map[want]bool{}
		for _, t := range n.tasks {
			b := t.Spec.Build
			if b == nil || terminal(t.State) || t.State == StateStopping {
				continue
			}
			w := want{b.Repo, b.Branch}
			g.mu.Lock()
			last := g.checked[b.Repo+"@"+b.Branch]
			g.mu.Unlock()
			if !seen[w] && (b.Commit == "" || time.Since(last) >= githubPoll) {
				seen[w] = true
				wants = append(wants, w)
			}
		}
		n.mu.Unlock()
		for _, w := range wants {
			sha, err := g.head(ctx, w.repo, w.branch)
			g.mu.Lock()
			g.checked[w.repo+"@"+w.branch] = time.Now()
			g.mu.Unlock()
			n.mu.Lock()
			for _, t := range n.tasks {
				b := t.Spec.Build
				if b == nil || b.Repo != w.repo || b.Branch != w.branch || terminal(t.State) || t.State == StateStopping {
					continue
				}
				switch {
				case err != nil:
					if b.Commit == "" {
						t.Message = "Couldn't get " + w.repo + " from GitHub: " + err.Error()
						n.saveTaskLocked(t)
					}
				case sha != b.Commit:
					n.rebuildLocked(t, sha)
				}
			}
			n.mu.Unlock()
		}
	}
}

// rebuildLocked points t at a new commit and starts it again, keeping its
// data. Failures of earlier commits don't count against the new one.
func (n *Node) rebuildLocked(t *Task, sha string) {
	b := *t.Spec.Build
	first := b.Commit == ""
	b.Commit = sha
	t.Spec.Build = &b
	if first {
		t.Message = "Building " + b.Repo + " at " + sha[:7]
		n.saveTaskLocked(t)
		return
	}
	t.Attempt++
	t.AttemptBase = t.Attempt - 1
	t.Excluded = nil
	t.AgentID = ""
	t.State, t.Message = StateQueued, "New commit "+sha[:7]+"; building and deploying it"
	n.saveTaskLocked(t)
}

// buildReady reports whether a task can be scheduled: apps built from a
// repository wait until the node knows which commit to build.
func buildReady(s protocol.TaskSpec) bool { return s.Build == nil || s.Build.Commit != "" }
