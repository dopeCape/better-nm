package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/dopeCape/better-nm/internal/core"
)

// Networks lists every bridge with its member links (by master), the owner
// guessed from the name, and the neighbours the kernel has seen on it. Docker
// bridges get their network name when the Docker socket is connectable; the
// Reachable field says why not otherwise.
func Networks(ctx context.Context) ([]core.InfraNetwork, error) {
	raw, err := linkList()
	if err != nil {
		return nil, fmt.Errorf("infra: list links: %w", err)
	}
	names := make(map[int]string, len(raw))
	for _, l := range raw {
		names[l.Attrs().Index] = l.Attrs().Name
	}
	links := make([]core.Link, 0, len(raw))
	for _, l := range raw {
		links = append(links, toLink(l, names))
	}
	byMaster := make(map[string][]core.Link)
	for _, l := range links {
		if l.Master != "" {
			byMaster[l.Master] = append(byMaster[l.Master], l)
		}
	}

	var out []core.InfraNetwork
	for _, l := range links {
		if l.Kind != "bridge" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, own := Classify(l.Name, l.Kind, HasSysfsDevice(l.Name))
		if own == "" {
			own = "unknown"
		}
		n := core.InfraNetwork{
			Bridge:  l,
			Owner:   own,
			Members: byMaster[l.Name],
			Source:  "netlink",
		}
		if n.Members == nil {
			n.Members = []core.Link{}
		}
		n.Neighbours = bridgeNeighbours(l, n.Members)
		out = append(out, n)
	}

	enrichDocker(ctx, out)
	return out, nil
}

// bridgeNeighbours merges the neighbour tables of the bridge and its members,
// deduplicated by IP.
func bridgeNeighbours(bridge core.Link, members []core.Link) []core.LANHost {
	seen := make(map[string]bool)
	var hosts []core.LANHost
	add := func(link core.Link) {
		neighs, err := neighList(link.IfIndex, netlink.FAMILY_ALL)
		if err != nil {
			return
		}
		for _, n := range neighs {
			if n.IP == nil || n.State&(netlink.NUD_NOARP) != 0 || n.State == netlink.NUD_NONE {
				continue
			}
			key := n.IP.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			h := core.LANHost{IP: key, State: neighState(n.State), Device: bridge.Name}
			if len(n.HardwareAddr) > 0 {
				h.MAC = n.HardwareAddr.String()
			}
			hosts = append(hosts, h)
		}
	}
	add(bridge)
	for _, m := range members {
		add(m)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].IP < hosts[j].IP })
	return hosts
}

func neighState(s int) string {
	switch {
	case s&(netlink.NUD_REACHABLE|netlink.NUD_PERMANENT) != 0:
		return "reachable"
	case s&(netlink.NUD_STALE|netlink.NUD_DELAY|netlink.NUD_PROBE) != 0:
		return "stale"
	case s&netlink.NUD_INCOMPLETE != 0:
		return "incomplete"
	case s&netlink.NUD_FAILED != 0:
		return "failed"
	}
	return "stale"
}

// ---- Docker enrichment -----------------------------------------------------

// dockerSocket is resolved lazily so tests and DOCKER_HOST can point elsewhere.
var dockerSocket = func() string {
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		return strings.TrimPrefix(h, "unix://")
	}
	return "/var/run/docker.sock"
}

// dockerNetwork is the subset of the Engine API's Network object we use.
type dockerNetwork struct {
	Name    string            `json:"Name"`
	ID      string            `json:"Id"`
	Driver  string            `json:"Driver"`
	Options map[string]string `json:"Options"`
}

// bridgeName is the host link Docker created for this network.
func (n dockerNetwork) bridgeName() string {
	if b := n.Options["com.docker.network.bridge.name"]; b != "" {
		return b
	}
	if len(n.ID) >= 12 {
		return "br-" + n.ID[:12]
	}
	return ""
}

// enrichDocker fills OwnerName/Reachable on docker-owned bridges. It never
// fails the caller: a missing or forbidden socket just leaves Reachable set.
func enrichDocker(ctx context.Context, nets []core.InfraNetwork) {
	hasDocker := false
	for _, n := range nets {
		if n.Owner == "docker" {
			hasDocker = true
			break
		}
	}
	if !hasDocker {
		return
	}
	list, reachable := dockerNetworks(ctx)
	byBridge := make(map[string]string, len(list))
	for _, d := range list {
		if d.Driver == "bridge" {
			if b := d.bridgeName(); b != "" {
				byBridge[b] = d.Name
			}
		}
	}
	for i := range nets {
		if nets[i].Owner != "docker" {
			continue
		}
		nets[i].Reachable = reachable
		if name, ok := byBridge[nets[i].Bridge.Name]; ok {
			nets[i].OwnerName = name
			nets[i].Source = "docker-api"
		}
	}
}

// dockerNetworks GETs /networks over the unix socket with net/http; no moby
// import. reachable is ok | needs-group | not-running.
func dockerNetworks(ctx context.Context) (list []dockerNetwork, reachable string) {
	sock := dockerSocket()
	if _, err := os.Stat(sock); err != nil {
		return nil, "not-running"
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/networks", nil)
	if err != nil {
		return nil, "not-running"
	}
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
			return nil, "needs-group"
		default:
			return nil, "not-running"
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "not-running"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "not-running"
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, "not-running"
	}
	return list, "ok"
}
