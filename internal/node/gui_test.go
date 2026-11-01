package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+n.dashKey()) }
	for _, c := range []struct {
		name   string
		method string
		set    func(*http.Request)
		want   int
	}{
		{"no key", "GET", nil, 401},
		{"wrong key", "GET", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, 401},
		{"key as a cookie", "GET", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: oldKeyCookie, Value: n.dashKey()}) }, 401},
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
	old := n.dashKey()
	if _, err := n.RotateGUIKey(); err != nil || n.dashKey() == old || GUIKey(n.cfg.DataDir) != n.dashKey() {
		t.Fatal("the dashboard key didn't change")
	}
	if do("GET", "/api/state", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+old) }) != 401 {
		t.Fatal("the old key still works")
	}
}

func TestMetrics(t *testing.T) {
	n, ctx := startNode(t)
	joinAgent(t, ctx, n, "box")
	h := n.guiHandler("127.0.0.1:7700")
	waitFor(t, n, 10*time.Second, "the machine", func() bool { return len(n.agents) == 1 })
	r := httptest.NewRequest("GET", "http://127.0.0.1:7700/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+n.dashKey())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{"sys1_build_info{", `sys1_machine_online{machine="box"`, "sys1_machine_cpu_cores{", `sys1_apps{state="running"} 0`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:7700/metrics", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("metrics without the key: %d", w.Code)
	}
}
