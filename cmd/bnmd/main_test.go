package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
)

func TestVersionFlag(t *testing.T) {
	out, err := os.CreateTemp("", "out")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out.Name())
	if code := run([]string{"--version"}, out, os.Stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	data, _ := os.ReadFile(out.Name())
	if !strings.HasPrefix(string(data), "bnmd ") || !strings.Contains(string(data), "api v1") {
		t.Errorf("output = %q", data)
	}
}

func TestBadFlag(t *testing.T) {
	if code := run([]string{"--bogus"}, os.Stdout, os.Stderr); code != 2 {
		t.Errorf("exit = %d", code)
	}
}

func TestPolicyFromConfig(t *testing.T) {
	n := config.Notify{Connected: true, Degraded: true, MutedNetworks: []string{"wifi:x"}}
	p := policyFromConfig(n)
	if !p.Enabled[core.EventConnected] || !p.Enabled[core.EventDegraded] || p.Enabled[core.EventDisconnected] {
		t.Errorf("enabled map = %v", p.Enabled)
	}
	if len(p.MutedNetworks) != 1 || p.MutedNetworks[0] != "wifi:x" {
		t.Errorf("muted = %v", p.MutedNetworks)
	}
}

func TestFakeServesAndStopsOnSignal(t *testing.T) {
	dir, err := os.MkdirTemp("", "bnmd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"--fake", "--socket", socket, "--config", filepath.Join(dir, "c.toml"), "--log-level", "error"}, os.Stdout, os.Stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, err := client.New(client.WithSocket(socket), client.WithAutoStart(false))
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Status(context.Background())
	if err != nil || st.NetworkKey != "wifi:HomeNet" {
		t.Errorf("status = %+v %v", st, err)
	}
	vs, err := c.VPNs(context.Background())
	if err != nil || len(vs) != 3 {
		t.Errorf("vpns = %v %v", vs, err)
	}
	body, code, _ := c.Raw(context.Background(), http.MethodGet, "/nope", nil)
	if code != 404 || !strings.Contains(string(body), `"code":"not-found"`) {
		t.Errorf("404 = %d %s", code, body)
	}
	c.Close()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop on SIGTERM")
	}
	if _, err := os.Lstat(socket); err == nil {
		t.Error("socket left behind")
	}
}

func TestSecondInstanceRefused(t *testing.T) {
	dir, err := os.MkdirTemp("", "bnmd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"--fake", "--socket", socket, "--config", filepath.Join(dir, "c.toml"), "--log-level", "error"}, os.Stdout, os.Stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code := run([]string{"--fake", "--socket", socket, "--config", filepath.Join(dir, "c.toml"), "--log-level", "error"}, os.Stdout, os.Stderr); code != 3 {
		t.Errorf("second instance exit = %d, want 3", code)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}
