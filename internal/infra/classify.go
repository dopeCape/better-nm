package infra

import (
	"regexp"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

var dockerBridgeRe = regexp.MustCompile(`^br-[0-9a-f]{12}$`)

// Classify separates hardware from plumbing and guesses which runtime owns a
// virtual link. hasSysfsDevice is HasSysfsDevice(name). The owner is one of
// docker, podman, libvirt, incus, nspawn, k8s, tailscale, wireguard, veth,
// loopback, or "" when nothing matched. Names are heuristics; a runtime API is
// the authority where reachable.
func Classify(name, kind string, hasSysfsDevice bool) (core.DeviceClass, string) {
	if name == "lo" || kind == "loopback" {
		return core.ClassLoopback, "loopback"
	}
	if hasSysfsDevice {
		return core.ClassPhysical, ""
	}
	return core.ClassInfra, owner(name, kind)
}

func owner(name, kind string) string {
	has := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	switch {
	case name == "docker0" || dockerBridgeRe.MatchString(name):
		return "docker"
	case has("podman", "cni-podman"):
		return "podman"
	case has("virbr", "vnet"):
		return "libvirt"
	case has("incusbr", "lxdbr", "lxcbr"):
		return "incus"
	case has("ve-", "vb-", "vz-"):
		return "nspawn"
	case name == "cni0" || has("flannel", "cali", "kube"):
		return "k8s"
	case has("tailscale"), has("ts") && kind == "tun":
		return "tailscale"
	case has("wg") || kind == "wireguard":
		return "wireguard"
	case has("veth"):
		return "veth"
	}
	return ""
}
