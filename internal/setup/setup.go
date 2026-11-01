// Package setup is sys0's graphical installer wizard. It runs a tiny web
// page on localhost so the same friendly installer works on every OS
// without a native UI toolkit per platform. Once setup finishes, sys0
// installs itself as a background service and becomes invisible.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

//go:embed web
var webFS embed.FS

type Options struct {
	DataDir string
	// Install registers and starts the background service. It returns a
	// human-readable error if, for example, admin rights are missing.
	Install func() error
	Log     *slog.Logger
	// OpenBrowser is false in tests.
	OpenBrowser bool
}

// Run shows the wizard and returns once the user finishes or ctx ends.
func Run(ctx context.Context, opts Options) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	b := make([]byte, 16)
	rand.Read(b)
	key := hex.EncodeToString(b)
	url := fmt.Sprintf("http://%s/%s/", ln.Addr(), key)

	done := make(chan struct{})
	sampler := &resources.Sampler{DataDir: opts.DataDir}
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /"+key+"/", http.StripPrefix("/"+key, http.FileServerFS(static)))
	mux.HandleFunc("GET /"+key+"/api/info", func(w http.ResponseWriter, r *http.Request) {
		res := sampler.Sample(false)
		_, err := agent.LoadConfig(opts.DataDir)
		json.NewEncoder(w).Encode(map[string]any{
			"version": version.Version, "resources": res, "configured": err == nil, "os": runtime.GOOS,
		})
	})
	mux.HandleFunc("POST /"+key+"/api/finish", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JoinCode     string         `json:"joinCode"`
			Name         string         `json:"name"`
			Trust        protocol.Trust `json:"trust"`
			MaxTasks     int            `json:"maxTasks"`
			StorageGB    *int           `json:"storageGB"`
			OpenInternet bool           `json:"openInternet"`
		}
		reply := func(ok bool, msg string) { json.NewEncoder(w).Encode(map[string]any{"ok": ok, "message": msg}) }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			reply(false, "Something went wrong reading the form.")
			return
		}
		code, err := protocol.ParseJoinCode(req.JoinCode)
		if err != nil {
			reply(false, strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".")
			return
		}
		if !req.Trust.Valid() {
			req.Trust = protocol.TrustCommunity
		}
		rctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		cfg, err := agent.Register(rctx, code, req.Name, req.Trust, sampler.Sample(req.Trust == protocol.TrustOwner), version.Version)
		if err != nil {
			reply(false, "Could not join the node: "+err.Error()+". Check that the node is running and reachable from this machine.")
			return
		}
		if req.MaxTasks >= 1 && req.MaxTasks <= 64 {
			cfg.MaxTasks = req.MaxTasks
		}
		if req.StorageGB != nil && *req.StorageGB >= 0 {
			cfg.StorageGB = *req.StorageGB
		}
		cfg.OpenInternet = req.OpenInternet && req.Trust == protocol.TrustCommunity
		if err := agent.SaveConfig(opts.DataDir, cfg); err != nil {
			reply(false, "Joined, but could not save settings: "+err.Error())
			return
		}
		if opts.Install != nil {
			if err := opts.Install(); err != nil {
				reply(false, "Joined the node, but could not start the background service: "+err.Error())
				return
			}
		}
		reply(true, "")
		close(done)
	})

	srv := &http.Server{Handler: loopbackOnly(ln.Addr().String(), mux), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	opts.Log.Info("setup is open in your browser", "url", url)
	fmt.Println("\n  sys0 setup is open in your browser. If it did not open, visit:\n\n   ", url+"\n")
	if opts.OpenBrowser {
		OpenURL(url)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		time.Sleep(2 * time.Second) // let the success page load its last bits
		return nil
	}
}

func loopbackOnly(addr string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Host), []byte(addr)) != 1 {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// OpenURL opens url in the default browser.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
