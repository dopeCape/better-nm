package diag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dopeCape/better-nm/internal/core"
)

func TestParseHexAddr(t *testing.T) {
	tests := []struct {
		in       string
		v6       bool
		wantAddr string
		wantPort int
		wantErr  bool
	}{
		{"00000000:699C", false, "0.0.0.0", 27036, false},
		{"0100007F:A4C1", false, "127.0.0.1", 42177, false},
		{"0A004064:8803", false, "100.64.0.10", 34819, false},
		{"010012AC:D268", false, "172.18.0.1", 53864, false},
		{"FB0000E0:14E9", false, "224.0.0.251", 5353, false},
		{"00000000000000000000000000000000:21C2", true, "::", 8642, false},
		{"5C117AFD0000E0A10000000033342DAA:9E40", true, "fd7a:115c:a1e0::aa2d:3433", 40512, false},
		{"0000000000000000FFFF00000100007F:0050", true, "127.0.0.1", 80, false}, // v4-mapped renders as v4
		{"garbage", false, "", 0, true},
		{"0100007F:ZZZZ", false, "", 0, true},
		{"0100007F:0050", true, "", 0, true}, // wrong width for v6
		{"zz00007F:0050", false, "", 0, true},
	}
	for _, tc := range tests {
		addr, port, err := parseHexAddr(tc.in, tc.v6)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseHexAddr(%q): err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if addr != tc.wantAddr || port != tc.wantPort {
			t.Errorf("parseHexAddr(%q) = %s:%d, want %s:%d", tc.in, addr, port, tc.wantAddr, tc.wantPort)
		}
	}
}

func TestParseProcNetFixtures(t *testing.T) {
	tests := []struct {
		proto string
		want  []core.ListeningPort
	}{
		{"tcp", []core.ListeningPort{
			{Proto: "tcp", Addr: "0.0.0.0", Port: 27036, UID: 1000},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 8233, UID: 0},
			{Proto: "tcp", Addr: "127.0.0.1", Port: 42177, UID: 1000},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 9090, UID: 0},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 8930, UID: 1001},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 8940, UID: 10001},
			{Proto: "tcp", Addr: "127.0.0.1", Port: 41675, UID: 1000},
		}},
		{"tcp6", []core.ListeningPort{
			{Proto: "tcp6", Addr: "fd7a:115c:a1e0::aa2d:3433", Port: 40512, UID: 0},
			{Proto: "tcp6", Addr: "::", Port: 8642, UID: 1000},
			{Proto: "tcp6", Addr: "::", Port: 8233, UID: 0},
		}},
		{"udp", []core.ListeningPort{
			{Proto: "udp", Addr: "100.64.0.10", Port: 34819, UID: 1000},
			{Proto: "udp", Addr: "172.18.0.1", Port: 53864, UID: 1000},
		}},
		{"udp6", []core.ListeningPort{
			{Proto: "udp6", Addr: "::", Port: 41641, UID: 0},
			{Proto: "udp6", Addr: "::", Port: 27036, UID: 1000},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.proto, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", "proc", "net", tc.proto))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := parseProcNet(tc.proto, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].ListeningPort != tc.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i].ListeningPort, tc.want[i])
				}
				if got[i].inode == 0 {
					t.Errorf("row %d has no inode", i)
				}
			}
		})
	}
}

func TestParseProcNetBadRows(t *testing.T) {
	bad := "header\n   0: 00000000:699C 00000000:0000 0A 00000000:00000000 00:00000000 00000000  abc        0 1623196 1\n"
	if _, err := parseProcNet("tcp", strings.NewReader(bad)); err == nil {
		t.Error("bad uid should fail")
	}
	short := "header\n 0: 00000000:699C\n"
	if got, err := parseProcNet("tcp", strings.NewReader(short)); err != nil || len(got) != 0 {
		t.Errorf("short row: got %v, %v", got, err)
	}
}

// TestListeningPorts builds a fake /proc with the fixtures plus two processes:
// pid 4242 owns inode 1623196 (readable), pid 999 is unreadable.
func TestListeningPorts(t *testing.T) {
	root := t.TempDir()
	mustCopyDir(t, filepath.Join("testdata", "proc", "net"), filepath.Join(root, "net"))

	fd := filepath.Join(root, "4242", "fd")
	if err := os.MkdirAll(fd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[1623196]", filepath.Join(fd, "7")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(fd, "0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[1623195]", filepath.Join(fd, "9")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "4242", "comm"), []byte("bnmd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "999", "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "999", "fd"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "999", "fd"), 0o755) })
	if err := os.MkdirAll(filepath.Join(root, "notapid"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	ports, err := ListeningPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 14 {
		t.Fatalf("got %d ports, want 14", len(ports))
	}
	attributed := 0
	for _, p := range ports {
		if p.User == "" {
			t.Errorf("%+v has no user", p)
		}
		if p.UID == 0 && p.User != "root" {
			t.Errorf("uid 0 -> %q, want root", p.User)
		}
		if p.PID != 0 {
			attributed++
			if p.PID != 4242 || p.Process != "bnmd" {
				t.Errorf("attributed %+v, want pid 4242 bnmd", p)
			}
			if !(p.Port == 27036 && (p.Proto == "tcp" || p.Proto == "udp6")) {
				t.Errorf("unexpected attribution %+v", p)
			}
		}
	}
	if attributed != 2 {
		t.Errorf("attributed %d sockets, want 2", attributed)
	}
	for i := 1; i < len(ports); i++ {
		if ports[i-1].Port > ports[i].Port {
			t.Errorf("not sorted by port: %d before %d", ports[i-1].Port, ports[i].Port)
		}
	}
}

func mustCopyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
