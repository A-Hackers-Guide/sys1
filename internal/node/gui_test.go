package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDashboardAuth: the key works only as a header, never as a cookie
// (which every port on the machine would receive), and changes need the
// custom header too.
func TestDashboardAuth(t *testing.T) {
	n, _ := startNode(t)
	h := n.guiHandler("127.0.0.1:7700")
	do := func(method, path string, set func(*http.Request)) int {
		r := httptest.NewRequest(method, "http://127.0.0.1:7700"+path, strings.NewReader("{}"))
		if set != nil {
			set(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+n.guiKey) }
	for _, c := range []struct {
		name   string
		method string
		set    func(*http.Request)
		want   int
	}{
		{"no key", "GET", nil, 401},
		{"wrong key", "GET", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, 401},
		{"key as a cookie", "GET", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: oldKeyCookie, Value: n.guiKey}) }, 401},
		{"key as a header", "GET", bearer, 200},
		{"change without X-Sys1", "POST", bearer, 403},
		{"other host", "GET", func(r *http.Request) { bearer(r); r.Host = "evil.example:7700" }, 403},
	} {
		path := "/api/state"
		if c.method == "POST" {
			path = "/api/crashes/sending"
		}
		if got := do(c.method, path, c.set); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
