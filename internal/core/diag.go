package core

import "time"

// LANHost is a neighbour on the current subnet.
type LANHost struct {
	IP       string    `json:"ip"`
	MAC      string    `json:"mac,omitempty"`
	Hostname string    `json:"hostname,omitempty"` // mDNS / reverse DNS
	State    string    `json:"state"`              // reachable | stale | incomplete | failed
	Device   string    `json:"device"`
	Self     bool      `json:"self,omitempty"`
	Gateway  bool      `json:"gateway,omitempty"`
	Seen     time.Time `json:"seen,omitempty"`
}

// ListeningPort is a socket in LISTEN (tcp) or bound (udp) on this host.
type ListeningPort struct {
	Proto   string `json:"proto"` // tcp | tcp6 | udp | udp6
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	User    string `json:"user"`
	UID     int    `json:"uid"`
}

// Route is one kernel route.
type Route struct {
	Dest    string `json:"dest"`
	Gateway string `json:"gateway,omitempty"`
	Device  string `json:"device"`
	Metric  int    `json:"metric"`
	Proto   string `json:"proto,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Table   int    `json:"table,omitempty"`
	Family  int    `json:"family"` // 4 | 6
}

// DNSAnswer is one resolution.
type DNSAnswer struct {
	Name     string        `json:"name"`
	Server   string        `json:"server,omitempty"`
	Type     string        `json:"type"`
	Answers  []string      `json:"answers"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// PublicIP is what the world sees.
type PublicIP struct {
	IP       string `json:"ip"`
	Colo     string `json:"colo,omitempty"`
	Location string `json:"location,omitempty"`
	Warp     string `json:"warp,omitempty"`
	Via      string `json:"via"`
}

// Link is one kernel network link, for the infra graph.
type Link struct {
	Name      string   `json:"name"`
	IfIndex   int      `json:"ifindex"`
	Kind      string   `json:"kind"` // bridge, veth, vlan, tun, wireguard, device, ...
	Master    string   `json:"master,omitempty"`
	Up        bool     `json:"up"`
	HwAddr    string   `json:"hwaddr,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	PeerNetNS int      `json:"peer_netns,omitempty"` // veth link-netnsid, -1 unknown
	MTU       int      `json:"mtu,omitempty"`
}

// InfraNetwork is a bridge plus its members, attributed to a runtime.
type InfraNetwork struct {
	Bridge     Link      `json:"bridge"`
	Owner      string    `json:"owner"`                // docker | podman | libvirt | incus | lxc | nspawn | k8s | unknown
	OwnerName  string    `json:"owner_name,omitempty"` // docker network name etc. when the API is reachable
	Members    []Link    `json:"members"`
	Neighbours []LANHost `json:"neighbours,omitempty"` // IP/MAC seen on the bridge
	Source     string    `json:"source"`               // netlink | docker-api | libvirt | machined
	Reachable  string    `json:"reachable,omitempty"`  // for runtime APIs: ok | needs-group | not-running
}
