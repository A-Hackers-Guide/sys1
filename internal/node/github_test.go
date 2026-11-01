package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGitHubAppsFollowTheirBranch: a new app waits for its commit, and a
// new commit on the branch redeploys it with a fresh failure count.
func TestGitHubAppsFollowTheirBranch(t *testing.T) {
	var mu sync.Mutex
	sha := strings.Repeat("a", 40)
	calls, notModified := 0, 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.URL.Path != "/repos/someone/app/commits/main" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == `"`+sha+`"` {
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"`+sha+`"`)
		w.Write([]byte(sha))
	}))
	defer gh.Close()

	n, _ := startNode(t)
	n.github.api = gh.URL
	task, err := n.Deploy("github-app", map[string]string{"REPO": "https://github.com/someone/app.git", "BRANCH": "main"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, n, 10*time.Second, "the first commit", func() bool { return task.Spec.Build.Commit == sha })
	if task.Spec.Build.Repo != "someone/app" {
		t.Fatalf("repo is %q", task.Spec.Build.Repo)
	}

	recheck := func() {
		n.github.mu.Lock()
		n.github.checked = map[string]time.Time{}
		n.github.mu.Unlock()
		n.github.kick <- struct{}{}
	}
	recheck() // unchanged: answered "not modified", nothing happens
	waitFor(t, n, 10*time.Second, "a conditional request", func() bool { mu.Lock(); defer mu.Unlock(); return notModified > 0 })
	n.mu.Lock()
	if task.Attempt != 1 {
		t.Fatalf("redeployed without a new commit (attempt %d)", task.Attempt)
	}
	n.mu.Unlock()

	mu.Lock()
	sha = strings.Repeat("b", 40)
	mu.Unlock()
	recheck()
	waitFor(t, n, 10*time.Second, "the redeploy", func() bool { return task.Spec.Build.Commit == strings.Repeat("b", 40) })
	n.mu.Lock()
	defer n.mu.Unlock()
	if task.Attempt != 2 || task.AttemptBase != 1 || task.State != StateQueued {
		t.Fatalf("after a new commit: attempt %d base %d state %s", task.Attempt, task.AttemptBase, task.State)
	}
}
