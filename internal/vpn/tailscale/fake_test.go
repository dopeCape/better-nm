package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon is a LocalAPI look-alike bound to a temp Unix socket. It replays
// the recorded fixtures in testdata/ and mimics tailscaled's Host/Origin checks
// and its 403 on writes without operator mode.
type fakeDaemon struct {
	t      *testing.T
	socket string
	srv    *http.Server

	mu       sync.Mutex
	status   map[string]any
	prefs    map[string]any
	writable bool
	authURL  string // set on status by login-interactive
	requests []fakeRequest
	notify   chan string // extra IPN bus lines pushed to every watcher
	watchers int
}

type fakeRequest struct {
	Method, Path, Body string
}

func loadJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

// socketPath returns a Unix socket path short enough for sun_path (108 bytes).
func socketPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ts.sock")
	if len(p) < 100 {
		return p
	}
	dir, err := os.MkdirTemp("", "bnmts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "ts.sock")
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{
		t:        t,
		socket:   socketPath(t),
		status:   loadJSON(t, "status.json"),
		prefs:    loadJSON(t, "prefs.json"),
		writable: true,
		authURL:  "https://login.tailscale.com/a/0123456789ab",
		notify:   make(chan string, 16),
	}
	ln, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	d.srv = &http.Server{Handler: http.HandlerFunc(d.serve)}
	go d.srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.srv.Shutdown(ctx)
		d.srv.Close()
	})
	return d
}

func (d *fakeDaemon) client() *Client { return New(d.socket) }

func (d *fakeDaemon) setStatus(k string, v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status[k] = v
}

func (d *fakeDaemon) setPref(k string, v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prefs[k] = v
}

func (d *fakeDaemon) pref(k string) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prefs[k]
}

func (d *fakeDaemon) setWritable(w bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writable = w
}

func (d *fakeDaemon) calls() []fakeRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]fakeRequest(nil), d.requests...)
}

// countCalls returns how many requests matched method and a path prefix.
func (d *fakeDaemon) countCalls(method, pathPrefix string) int {
	n := 0
	for _, r := range d.calls() {
		if r.Method == method && strings.HasPrefix(r.Path, pathPrefix) {
			n++
		}
	}
	return n
}

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	if r.Referer() != "" || r.Header.Get("Origin") != "" || (r.Host != "" && r.Host != LocalAPIHost) {
		http.Error(w, "invalid localapi request", http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	d.requests = append(d.requests, fakeRequest{r.Method, r.URL.RequestURI(), string(body)})
	writable := d.writable
	d.mu.Unlock()
	w.Header().Set("Tailscale-Version", "1.98.10")
	w.Header().Set("Tailscale-Cap", "138")

	path := strings.TrimPrefix(r.URL.Path, "/localapi/v0/")
	switch path {
	case "status":
		d.mu.Lock()
		st := d.status
		if r.URL.Query().Get("peers") == "false" {
			st = make(map[string]any, len(d.status))
			for k, v := range d.status {
				if k != "Peer" {
					st[k] = v
				}
			}
		}
		b, _ := json.Marshal(st)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "prefs":
		switch r.Method {
		case http.MethodGet:
		case http.MethodPatch:
			if !writable {
				http.Error(w, "prefs write access denied", http.StatusForbidden)
				return
			}
			var mp map[string]any
			if err := json.Unmarshal(body, &mp); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			d.mu.Lock()
			for k, v := range mp {
				if field, ok := strings.CutSuffix(k, "Set"); ok && v == true {
					val, present := mp[field]
					if !present {
						// Omitted zero value: keep the JSON type of the current pref.
						switch d.prefs[field].(type) {
						case bool:
							val = false
						default:
							val = ""
						}
					}
					d.prefs[field] = val
				}
			}
			d.mu.Unlock()
		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
			return
		}
		d.writePrefs(w)
	case "set-use-exit-node-enabled":
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		if !writable {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
		on := r.URL.Query().Get("enabled") == "true"
		d.mu.Lock()
		if on {
			if prior, _ := d.prefs["InternalExitNodePrior"].(string); prior != "" {
				d.prefs["ExitNodeID"] = prior
			}
		} else {
			d.prefs["InternalExitNodePrior"] = d.prefs["ExitNodeID"]
			d.prefs["ExitNodeID"] = ""
		}
		d.mu.Unlock()
		d.writePrefs(w)
	case "login-interactive":
		if !writable {
			http.Error(w, "login access denied", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.status["AuthURL"] = d.authURL
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "logout":
		if !writable {
			http.Error(w, "logout access denied", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.status["BackendState"] = StateNeedsLogin
		d.prefs["LoggedOut"] = true
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "watch-ipn-bus":
		f, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/json")
		initial, err := os.ReadFile(filepath.Join("testdata", "notify-initial.json"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(initial)
		f.Flush()
		d.mu.Lock()
		d.watchers++
		d.mu.Unlock()
		for {
			select {
			case <-r.Context().Done():
				return
			case line := <-d.notify:
				if line == "" { // sentinel: drop the stream
					return
				}
				io.WriteString(w, line+"\n")
				f.Flush()
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func (d *fakeDaemon) writePrefs(w http.ResponseWriter) {
	d.mu.Lock()
	b, _ := json.Marshal(d.prefs)
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
