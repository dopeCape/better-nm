package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/version"
)

func TestReadSSE(t *testing.T) {
	in := ": connected\n\n" +
		"event: change\ndata: {\"kind\":\"wifi\"}\n\n" +
		": ping\n\n" +
		"event: event\ndata: {\"type\":\"connected\",\n" + "data: \"title\":\"x\"}\n\n" +
		"data: no-name\n\n" +
		"event: tail\ndata: last"
	var got []sseEvent
	if err := readSSE(strings.NewReader(in), func(e sseEvent) bool { got = append(got, e); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("events = %d: %+v", len(got), got)
	}
	if got[0].Name != "change" || string(got[0].Data) != `{"kind":"wifi"}` {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Name != "event" || string(got[1].Data) != "{\"type\":\"connected\",\n\"title\":\"x\"}" {
		t.Errorf("multi-line data = %q", got[1].Data)
	}
	if got[2].Name != "" || string(got[2].Data) != "no-name" {
		t.Errorf("nameless = %+v", got[2])
	}
	if got[3].Name != "tail" || string(got[3].Data) != "last" {
		t.Errorf("unterminated tail = %+v", got[3])
	}
	// stopping early
	n := 0
	_ = readSSE(strings.NewReader(in), func(sseEvent) bool { n++; return false })
	if n != 1 {
		t.Errorf("stop early: %d", n)
	}
}

func TestAPIErrorSemantics(t *testing.T) {
	err := error(&APIError{Status: 404, Code: core.KindNotFound, Message: "profile x not found", Hint: "run bnm profiles"})
	if !errors.Is(err, core.ErrNotFound) || core.KindOf(err) != core.KindNotFound || core.HintOf(err) != "run bnm profiles" {
		t.Errorf("mapping: is=%v kind=%s hint=%q", errors.Is(err, core.ErrNotFound), core.KindOf(err), core.HintOf(err))
	}
	if !strings.Contains(err.Error(), "run bnm profiles") {
		t.Errorf("message should carry the hint: %s", err)
	}
	if kindForStatus(403) != core.KindPermission || kindForStatus(418) != core.KindInternal {
		t.Error("kindForStatus")
	}
}

func TestFindDaemonEnvAndPath(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, DaemonBinary)
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BNM_DAEMON", "/explicit/bnmd")
	if p, _ := FindDaemon(); p != "/explicit/bnmd" {
		t.Errorf("env override: %s", p)
	}
	t.Setenv("BNM_DAEMON", "")
	t.Setenv("PATH", dir)
	p, err := FindDaemon()
	if err != nil || p != fakeBin {
		t.Errorf("PATH lookup: %s %v", p, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := FindDaemon(); err == nil {
		t.Error("want not found")
	}
}

const stubSource = `package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
)

func main() {
	socket := flag.String("socket", "", "")
	flag.Parse()
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("stub bnmd listening on", *socket)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"api_version\":%%d,\"version\":\"stub\",\"nm_state\":\"stub\"}\n", %d)
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		go func() { ln.Close(); os.Exit(0) }()
	})
	_ = http.Serve(ln, mux)
}
`

// buildStub compiles a stand-in bnmd that only answers /v1/status.
func buildStub(t *testing.T, apiVersion int) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bnmstub")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fmt.Sprintf(stubSource, apiVersion)), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, DaemonBinary)
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GO111MODULE=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build stub daemon: %v\n%s", err, out)
	}
	return bin
}

func quitStub(socket string) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "POST /quit HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n")
	buf := make([]byte, 256)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = conn.Read(buf)
}

func TestAutoStartSpawnsDaemon(t *testing.T) {
	bin := buildStub(t, version.APIVersion)
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	t.Cleanup(func() { quitStub(socket) })

	c, err := New(WithSocket(socket), WithDaemonPath(bin), WithStartTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if !c.StartedDaemon() {
		t.Error("client should report it started the daemon")
	}
	st, err := c.Status(context.Background())
	if err != nil || st.Version != "stub" {
		t.Errorf("status via spawned daemon = %+v %v", st, err)
	}
	// the daemon's stdio landed in the log file
	logData, err := os.ReadFile(filepath.Join(dir, "bnm", "bnmd.log"))
	if err != nil || !strings.Contains(string(logData), "stub bnmd listening") {
		t.Errorf("log = %q %v", logData, err)
	}
	// a second client finds it running and does not spawn again
	c2, err := New(WithSocket(socket), WithDaemonPath(filepath.Join(dir, "does-not-exist")))
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer c2.Close()
	if c2.StartedDaemon() {
		t.Error("second client must not spawn")
	}
}

func TestAutoStartFindsDaemonOnPath(t *testing.T) {
	bin := buildStub(t, version.APIVersion)
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("BNM_DAEMON", "")
	t.Setenv("PATH", filepath.Dir(bin))
	t.Cleanup(func() { quitStub(socket) })
	c, err := New(WithSocket(socket), WithStartTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.Close()
}

func TestAutoStartVersionMismatch(t *testing.T) {
	bin := buildStub(t, version.APIVersion+1)
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	t.Cleanup(func() { quitStub(socket) })
	_, err = New(WithSocket(socket), WithDaemonPath(bin), WithStartTimeout(5*time.Second))
	var vm *ErrVersionMismatch
	if !errors.As(err, &vm) {
		t.Fatalf("want version mismatch, got %v", err)
	}
}

func TestAutoStartFailsWhenBinaryMissing(t *testing.T) {
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	_, err = New(WithSocket(filepath.Join(dir, "bnmd.sock")), WithDaemonPath(filepath.Join(dir, "missing")))
	if !IsNotRunning(err) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}

func TestAutoStartTimesOutWhenSocketNeverAppears(t *testing.T) {
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sleeper := filepath.Join(dir, "bnmd")
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", dir)
	_, err = New(WithSocket(filepath.Join(dir, "bnmd.sock")), WithDaemonPath(sleeper), WithStartTimeout(200*time.Millisecond))
	if !IsNotRunning(err) || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("want timeout ErrNotRunning, got %v", err)
	}
}

func TestTransportErrorIsNotRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "bnmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bnmd.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"api_version":%d}`, version.APIVersion)
	})
	go func() { _ = http.Serve(ln, mux) }()
	c, err := New(WithSocket(socket), WithAutoStart(false))
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	os.Remove(socket)
	c.Close()
	_, err = c.Devices(context.Background())
	if !IsNotRunning(err) {
		t.Errorf("after daemon exit: %v", err)
	}
}
