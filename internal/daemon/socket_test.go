package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestListenCreatesDirAndSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt", "bnm")
	path := filepath.Join(dir, "bnmd.sock")
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v %v", st.Mode(), err)
	}
	st, err = os.Lstat(path)
	if err != nil || st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket mode = %v %v", st.Mode(), err)
	}
	pid, err := os.ReadFile(filepath.Join(dir, LockName))
	if err != nil || len(pid) == 0 {
		t.Errorf("pid file: %q %v", pid, err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket not unlinked: %v", err)
	}
}

func TestListenTwiceIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bnmd.sock")
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = Listen(path)
	var already *ErrAlreadyRunning
	if !errors.As(err, &already) || already.PID != os.Getpid() {
		t.Fatalf("second Listen = %v", err)
	}
	if already.Error() == "" {
		t.Error("message")
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bnmd.sock")
	// a socket file nobody listens on
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// close the listener but keep the file (net's Close unlinks it, so re-create a stale one)
	ln.Close()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	defer s.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
}

func TestListenAfterCloseWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bnmd.sock")
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Listen(path)
	if err != nil {
		t.Fatalf("relisten: %v", err)
	}
	s2.Close()
}

func TestDefaultSocketPath(t *testing.T) {
	t.Setenv("BNM_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := DefaultSocketPath(); got != "/run/user/1000/bnm/bnmd.sock" {
		t.Errorf("DefaultSocketPath = %s", got)
	}
}
