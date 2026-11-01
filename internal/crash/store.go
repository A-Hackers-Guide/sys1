package crash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultIntake is the project's crash intake (see intake/ in the source).
// It is the only place that holds a GitHub token; apps never do. Empty
// until the intake is deployed: then the dashboard offers a prefilled
// GitHub link instead of sending.
var DefaultIntake = ""

// IssuesURL is where crash reports go when filed by hand.
const IssuesURL = "https://github.com/A-Hackers-Guide/sys1/issues/new"

// Entry is one distinct crash seen in a pool.
type Entry struct {
	Report
	Versions []string `json:"versions"`
	// SentCount is Count when the intake last heard of it; Issue is the
	// GitHub issue it filed.
	SentCount int       `json:"sentCount,omitempty"`
	SentAt    time.Time `json:"sentAt,omitzero"`
	Issue     string    `json:"issue,omitempty"`
}

// Store keeps the crashes a node has seen, in <data>/crashes.json.
type Store struct {
	mu   sync.Mutex
	path string
	data storeData
}

type storeData struct {
	Off     bool              `json:"off,omitempty"` // the owner turned sending off
	Entries map[string]*Entry `json:"entries"`
}

const maxEntries = 200

func OpenStore(dataDir string) *Store {
	s := &Store{path: filepath.Join(dataDir, "crashes.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.data)
	}
	if s.data.Entries == nil {
		s.data.Entries = map[string]*Entry{}
	}
	return s
}

func (s *Store) saveLocked() {
	b, _ := json.MarshalIndent(s.data, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.path)
	}
}

// Add records a crash, merging it with earlier ones of the same bug.
func (s *Store) Add(r Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.data.Entries[r.Signature]
	if e == nil {
		if len(s.data.Entries) >= maxEntries {
			return
		}
		e = &Entry{Report: r}
		s.data.Entries[r.Signature] = e
	} else {
		e.Count += r.Count
		if r.Last.After(e.Last) {
			e.Last, e.Message, e.Frames, e.Version = r.Last, r.Message, r.Frames, r.Version
		}
		if r.First.Before(e.First) {
			e.First = r.First
		}
	}
	found := false
	for _, v := range e.Versions {
		found = found || v == r.Version
	}
	if !found {
		e.Versions = append(e.Versions, r.Version)
	}
	s.saveLocked()
}

// List returns every crash, most recent first.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.data.Entries))
	for _, e := range s.data.Entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

func (s *Store) Sending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.data.Off
}

func (s *Store) SetSending(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Off = !on
	s.saveLocked()
}

// Forget removes a crash (it was fixed, or the owner doesn't care).
func (s *Store) Forget(sig string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Entries, sig)
	s.saveLocked()
}

// due lists crashes the intake hasn't heard of, or whose count grew since,
// at most once a day each.
func (s *Store) due(now time.Time) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Entry
	for _, e := range s.data.Entries {
		if e.SentCount == 0 || (e.Count > e.SentCount && now.Sub(e.SentAt) > 24*time.Hour) {
			out = append(out, *e)
		}
	}
	return out
}

func (s *Store) markSent(sig string, count int, issue string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.data.Entries[sig]; e != nil {
		e.SentCount, e.SentAt = count, now
		if issue != "" {
			e.Issue = issue
		}
		s.saveLocked()
	}
}

// Sender forwards a node's crashes to the intake.
type Sender struct {
	Store  *Store
	Intake string
	HTTP   *http.Client
	Log    *slog.Logger
}

// Payload is what the intake receives: the report plus how many times it
// happened, nothing else.
type Payload struct {
	Report
	Versions []string `json:"versions"`
}

// SendDue sends what's due; it returns how many reports went out.
func (s *Sender) SendDue(ctx context.Context) int {
	if s.Intake == "" || !s.Store.Sending() {
		return 0
	}
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	sent := 0
	for _, e := range s.Store.due(time.Now()) {
		body, _ := json.Marshal(Payload{Report: e.Report, Versions: e.Versions})
		req, _ := http.NewRequestWithContext(ctx, "POST", s.Intake, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.HTTP.Do(req)
		if err != nil {
			s.Log.Debug("couldn't send a crash report", "err", err)
			return sent
		}
		var out struct {
			Issue string `json:"issue"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			s.Log.Debug("the crash intake refused a report", "status", resp.Status)
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				return sent
			}
		}
		if !strings.HasPrefix(out.Issue, "https://github.com/A-Hackers-Guide/sys1/issues/") {
			out.Issue = ""
		}
		s.Store.markSent(e.Signature, e.Count, out.Issue, time.Now())
		sent++
	}
	return sent
}

// Loop sends due reports every few minutes until ctx ends.
func (s *Sender) Loop(ctx context.Context) {
	for {
		s.SendDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// Title is the issue title for a crash.
func (e *Entry) Title() string {
	m := e.Message
	if len(m) > 80 {
		m = m[:80] + "…"
	}
	return fmt.Sprintf("crash %s: %s", e.Signature, m)
}

// Body is the issue text for a crash.
func (e *Entry) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s %s** on %s/%s crashed", e.App, e.Version, e.OS, e.Arch)
	if e.Count > 1 {
		fmt.Fprintf(&b, " (%d times in one pool; versions %s)", e.Count, strings.Join(e.Versions, ", "))
	}
	fmt.Fprintf(&b, ".\n\n```\n%s\n\n", e.Message)
	for _, f := range e.Frames {
		b.WriteString(f + "\n")
	}
	fmt.Fprintf(&b, "```\n\nSignature `%s`. Reported automatically; scrubbed of names, addresses and paths.\n", e.Signature)
	return b.String()
}

// IssueLink is a prefilled "new issue" link for filing a crash by hand.
func (e *Entry) IssueLink() string {
	return IssuesURL + "?" + url.Values{"labels": {"crash"}, "title": {e.Title()}, "body": {e.Body()}}.Encode()
}
