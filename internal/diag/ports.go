package diag

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

const tcpListen = "0A"

// ListeningPorts lists TCP sockets in LISTEN and UDP sockets bound with no
// remote, from /proc/net/{tcp,tcp6,udp,udp6}. PID/process are filled where
// /proc/<pid>/fd is readable (own uid, or root); the user name always is.
func ListeningPorts(ctx context.Context) ([]core.ListeningPort, error) {
	var out []procPort
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(procRoot, "net", proto))
		if err != nil {
			if os.IsNotExist(err) { // IPv6 disabled
				continue
			}
			return nil, fmt.Errorf("diag: open /proc/net/%s: %w", proto, err)
		}
		ports, err := parseProcNet(proto, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("diag: parse /proc/net/%s: %w", proto, err)
		}
		out = append(out, ports...)
	}

	inodes := make(map[int]bool, len(out))
	for _, p := range out {
		inodes[p.inode] = true
	}
	owners := socketOwners(ctx, inodes)
	users := map[int]string{}
	for i := range out {
		p := &out[i]
		if o, ok := owners[p.inode]; ok {
			p.PID, p.Process = o.pid, o.comm
		}
		name, ok := users[p.UID]
		if !ok {
			name = userName(p.UID)
			users[p.UID] = name
		}
		p.User = name
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	res := make([]core.ListeningPort, len(out))
	for i, p := range out {
		res[i] = p.ListeningPort
	}
	return res, nil
}

type procPort struct {
	core.ListeningPort
	inode int
}

// parseProcNet reads one /proc/net/{tcp,udp}{,6} table and keeps the
// listening (tcp state 0A) or unconnected-bound (udp, remote all-zero) rows.
func parseProcNet(proto string, r io.Reader) ([]procPort, error) {
	var out []procPort
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		local, remote, state, uidS, inodeS := f[1], f[2], f[3], f[7], f[9]
		isTCP := strings.HasPrefix(proto, "tcp")
		if isTCP && state != tcpListen {
			continue
		}
		if !isTCP && !zeroRemote(remote) {
			continue
		}
		addr, port, err := parseHexAddr(local, strings.HasSuffix(proto, "6"))
		if err != nil {
			return nil, err
		}
		uid, err := strconv.Atoi(uidS)
		if err != nil {
			return nil, fmt.Errorf("uid %q: %w", uidS, err)
		}
		inode, err := strconv.Atoi(inodeS)
		if err != nil {
			return nil, fmt.Errorf("inode %q: %w", inodeS, err)
		}
		out = append(out, procPort{
			ListeningPort: core.ListeningPort{Proto: proto, Addr: addr, Port: port, UID: uid},
			inode:         inode,
		})
	}
	return out, sc.Err()
}

func zeroRemote(s string) bool {
	return strings.Trim(s, "0:") == ""
}

// parseHexAddr decodes /proc/net's "HEXADDR:PORT". IPv4 is one little-endian
// 32-bit word; IPv6 is four little-endian 32-bit words.
func parseHexAddr(s string, v6 bool) (string, int, error) {
	host, portS, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(portS, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("bad port %q: %w", portS, err)
	}
	b, err := hex.DecodeString(host)
	if err != nil {
		return "", 0, fmt.Errorf("bad host %q: %w", host, err)
	}
	want := 4
	if v6 {
		want = 16
	}
	if len(b) != want {
		return "", 0, fmt.Errorf("bad host %q: %d bytes", host, len(b))
	}
	ip := make(net.IP, want)
	for w := 0; w < want; w += 4 {
		ip[w], ip[w+1], ip[w+2], ip[w+3] = b[w+3], b[w+2], b[w+1], b[w]
	}
	return ip.String(), int(port), nil
}

type owner struct {
	pid  int
	comm string
}

// socketOwners scans /proc/*/fd for socket:[inode] links. Unreadable
// directories (other users' processes) are skipped silently.
func socketOwners(ctx context.Context, want map[int]bool) map[int]owner {
	out := map[int]owner{}
	if len(want) == 0 {
		return out
	}
	procs, err := os.ReadDir(procRoot)
	if err != nil {
		return out
	}
	for _, p := range procs {
		if ctx.Err() != nil {
			return out
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil || !p.IsDir() {
			continue
		}
		fdDir := filepath.Join(procRoot, p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		var comm string
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			ino, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"))
			if err != nil || !want[ino] {
				continue
			}
			if _, seen := out[ino]; seen {
				continue
			}
			if comm == "" {
				comm = readComm(pid)
			}
			out[ino] = owner{pid: pid, comm: comm}
		}
	}
	return out
}

func readComm(pid int) string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func userName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}
