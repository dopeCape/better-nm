package daemon

import (
	"errors"
	"fmt"
	"github.com/dopeCape/better-nm/internal/core"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dopeCape/better-nm/internal/paths"
)

// LockName is the pid/lock file next to the socket.
const LockName = "bnmd.pid"

// ErrAlreadyRunning is returned by Listen when another bnmd holds the lock.
type ErrAlreadyRunning struct {
	PID  int
	Path string
}

func (e *ErrAlreadyRunning) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("bnmd is already running (pid %d, %s)", e.PID, e.Path)
	}
	return fmt.Sprintf("bnmd is already running (%s)", e.Path)
}

// Socket is the daemon's listening Unix socket plus the lock that makes it unique.
type Socket struct {
	Path string
	ln   net.Listener
	lock *os.File
}

// DefaultSocketPath is where bnmd listens unless told otherwise.
func DefaultSocketPath() string { return paths.Socket() }

// Listen binds the socket at path: the directory is created 0700, a lock file
// beside it guarantees a single daemon, a leftover socket nobody answers on is
// unlinked, and the socket itself is chmod 0600.
func Listen(path string) (*Socket, error) {
	// sockaddr_un.sun_path is 108 bytes including the NUL; longer paths fail with
	// the unhelpful EINVAL, so say what happened.
	if len(path) > 107 {
		return nil, core.Errorf(core.KindInvalid, "use a shorter --socket path (under $XDG_RUNTIME_DIR)",
			"daemon: socket path is %d bytes, Unix sockets allow 107", len(path))
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: chmod %s: %w", dir, err)
	}
	lock, err := acquireLock(filepath.Join(dir, LockName))
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			conn.Close()
			releaseLock(lock)
			return nil, &ErrAlreadyRunning{Path: path}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			releaseLock(lock)
			return nil, fmt.Errorf("daemon: remove stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		releaseLock(lock)
		return nil, fmt.Errorf("daemon: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		releaseLock(lock)
		return nil, fmt.Errorf("daemon: chmod %s: %w", path, err)
	}
	return &Socket{Path: path, ln: ln, lock: lock}, nil
}

// Listener is the bound listener.
func (s *Socket) Listener() net.Listener { return s.ln }

// Close stops listening, unlinks the socket and releases the lock.
func (s *Socket) Close() error {
	var first error
	if s.ln != nil {
		if err := s.ln.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
		first = err
	}
	if s.lock != nil {
		releaseLock(s.lock)
		s.lock = nil
	}
	return first
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := 0
		if data, rerr := os.ReadFile(path); rerr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &ErrAlreadyRunning{PID: pid, Path: path}
		}
		return nil, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		releaseLock(f)
		return nil, fmt.Errorf("daemon: truncate lock: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		releaseLock(f)
		return nil, fmt.Errorf("daemon: write lock: %w", err)
	}
	return f, nil
}

// releaseLock unlocks and closes but deliberately leaves the file in place:
// unlinking it would let two later starters lock different inodes of the same
// path. A stale pid file is harmless; the flock is what matters.
func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
