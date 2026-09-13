package nm

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

// deviceKind maps NMDeviceType onto core.DeviceKind.
func deviceKind(t uint32) core.DeviceKind {
	switch t {
	case devTypeEthernet:
		return core.DeviceEthernet
	case devTypeWifi:
		return core.DeviceWifi
	case devTypeBridge:
		return core.DeviceBridge
	case devTypeVlan:
		return core.DeviceVlan
	case devTypeTun:
		return core.DeviceTun
	case devTypeVeth:
		return core.DeviceVeth
	case devTypeWireGuard:
		return core.DeviceWireGuard
	case devTypeLoopback:
		return core.DeviceLoopback
	}
	return core.DeviceOther
}

// deviceState maps NMDeviceState onto bnm's simplified state. external is the
// ActiveConnection StateFlags EXTERNAL bit: the device is up but NM did not
// configure it (docker0, tailscale0).
func deviceState(s uint32, external bool) core.DeviceState {
	switch {
	case s == devStateUnmanaged:
		return core.DeviceUnmanaged
	case s == devStateUnknown, s == devStateUnavailable:
		return core.DeviceUnavailable
	case s == devStateDisconnected, s == devStateDeactivating:
		return core.DeviceDisconnected
	case s >= devStatePrepare && s <= devStateSecondaries:
		return core.DeviceConnecting
	case s == devStateActivated:
		if external {
			return core.DeviceExternal
		}
		return core.DeviceConnected
	case s == devStateFailed:
		return core.DeviceFailed
	}
	return core.DeviceUnavailable
}

// sysfsProbe answers "does /sys/class/net/<name> exist, and does it have a
// device link?". It is a function so tests can fake the filesystem.
type sysfsProbe func(name string) (known, physical bool)

func sysfsClassNet(root string) sysfsProbe {
	return func(name string) (known, physical bool) {
		if name == "" || strings.ContainsAny(name, "/\x00") {
			return false, false
		}
		base := filepath.Join(root, name)
		if _, err := os.Lstat(base); err != nil {
			return false, false
		}
		_, err := os.Stat(filepath.Join(base, "device"))
		return true, err == nil
	}
}

// deviceClass decides physical / infra / loopback. When sysfs does not know the
// interface at all (a test bus, a container) the kind decides.
func deviceClass(name string, kind core.DeviceKind, probe sysfsProbe) core.DeviceClass {
	if kind == core.DeviceLoopback || name == "lo" {
		return core.ClassLoopback
	}
	known, physical := probe(name)
	if known {
		if physical {
			return core.ClassPhysical
		}
		return core.ClassInfra
	}
	switch kind {
	case core.DeviceWifi, core.DeviceEthernet:
		return core.ClassPhysical
	}
	return core.ClassInfra
}

var (
	reDockerBridge = regexp.MustCompile(`^br-[0-9a-f]{12}$`)
)

// deviceOwner guesses which runtime created an infra interface from its name.
func deviceOwner(name string, kind core.DeviceKind) string {
	switch {
	case name == "docker0", reDockerBridge.MatchString(name):
		return "docker"
	case strings.HasPrefix(name, "podman"):
		return "podman"
	case strings.HasPrefix(name, "virbr"), strings.HasPrefix(name, "vnet"):
		return "libvirt"
	case strings.HasPrefix(name, "incusbr"), strings.HasPrefix(name, "lxdbr"), strings.HasPrefix(name, "lxcbr"):
		return "incus"
	case strings.HasPrefix(name, "ve-"), strings.HasPrefix(name, "vb-"), strings.HasPrefix(name, "vz-"):
		return "nspawn"
	case name == "cni0", strings.HasPrefix(name, "flannel"), strings.HasPrefix(name, "cali"):
		return "k8s"
	case name == "tailscale0":
		return "tailscale"
	case strings.HasPrefix(name, "veth"), kind == core.DeviceVeth:
		return "veth"
	case strings.HasPrefix(name, "wg"), kind == core.DeviceWireGuard:
		return "wireguard"
	}
	return ""
}

// apSecurity decodes AccessPoint Flags/WpaFlags/RsnFlags into a security family.
// Transition-mode networks (PSK+SAE) report wpa-psk because a wpa-psk profile
// joins them; SAE-only networks report sae.
func apSecurity(flags, wpa, rsn uint32) core.WifiSecurity {
	all := wpa | rsn
	switch {
	case all&(apSecKeyMgmt8021X|apSecKeyMgmtEAPB) != 0:
		return core.SecWPAEAP
	case all&apSecKeyMgmtPSK != 0:
		return core.SecWPAPSK
	case all&apSecKeyMgmtSAE != 0:
		return core.SecSAE
	case all&(apSecKeyMgmtOWE|apSecKeyMgmtOWETM) != 0:
		return core.SecOWE
	case flags&apFlagPrivacy != 0 && all == 0:
		return core.SecWEP
	case all&(apSecPairWEP40|apSecPairWEP104|apSecGroupWEP40|apSecGroupWEP104) != 0:
		return core.SecWEP
	}
	return core.SecOpen
}

// keyMgmtFor is the 802-11-wireless-security.key-mgmt to use when creating a
// profile for a network of the given security; "" means no security setting.
func keyMgmtFor(sec core.WifiSecurity) string {
	switch sec {
	case core.SecWPAPSK:
		return "wpa-psk"
	case core.SecSAE:
		return "sae"
	case core.SecOWE:
		return "owe"
	case core.SecWEP:
		return "none"
	case core.SecWPAEAP:
		return "wpa-eap"
	}
	return ""
}

// profileSecurity maps 802-11-wireless-security.key-mgmt back to a family.
func profileSecurity(keyMgmt string) core.WifiSecurity {
	switch keyMgmt {
	case "":
		return core.SecOpen
	case "none":
		return core.SecWEP
	case "wpa-psk":
		return core.SecWPAPSK
	case "sae":
		return core.SecSAE
	case "owe":
		return core.SecOWE
	case "wpa-eap", "wpa-eap-suite-b-192", "ieee8021x":
		return core.SecWPAEAP
	}
	return core.SecWPAPSK
}

// wifiChannel converts a centre frequency in MHz to (channel, band).
func wifiChannel(freq uint32) (int, string) {
	switch {
	case freq == 2484:
		return 14, "2.4"
	case freq >= 2412 && freq <= 2472:
		return int(freq-2407) / 5, "2.4"
	case freq >= 5955 && freq <= 7115:
		return int(freq-5950) / 5, "6"
	case freq == 5935:
		return 2, "6"
	case freq >= 5000 && freq < 5935:
		return int(freq-5000) / 5, "5"
	case freq >= 4900 && freq < 5000:
		return int(freq-4000) / 5, "5"
	}
	return 0, ""
}

// profileType normalises NM connection.type.
func profileType(t string) core.ProfileType {
	switch t {
	case typeWifi:
		return core.ProfileWifi
	case typeEthernet:
		return core.ProfileEthernet
	case typeWireGuard:
		return core.ProfileWireGuard
	case typeVPN:
		return core.ProfileVPN
	case typeBridge:
		return core.ProfileBridge
	}
	return core.ProfileOther
}

func activeState(s uint32) core.ActiveState {
	switch s {
	case activeActivating:
		return core.ActiveActivating
	case activeActivated:
		return core.ActiveActivated
	case activeDeactivating:
		return core.ActiveDeactivating
	case activeDeactivated:
		return core.ActiveDeactivated
	}
	return core.ActiveUnknown
}

// nmStateName names NMState.
func nmStateName(s uint32) string {
	switch s {
	case 0:
		return "unknown"
	case 10:
		return "asleep"
	case 20:
		return "disconnected"
	case 30:
		return "disconnecting"
	case 40:
		return "connecting"
	case 50:
		return "connected-local"
	case 60:
		return "connected-site"
	case 70:
		return "connected-global"
	}
	return "unknown"
}

func connectivity(c uint32) core.Connectivity {
	switch c {
	case 1:
		return core.ConnNone
	case 2:
		return core.ConnPortal
	case 3:
		return core.ConnLimited
	case 4:
		return core.ConnFull
	}
	return core.ConnUnknown
}

// vpnStateName names NMVpnConnectionState.
func vpnStateName(s uint32) string {
	switch s {
	case 1:
		return "prepare"
	case 2:
		return "need-auth"
	case 3:
		return "connect"
	case 4:
		return "ip-config-get"
	case 5:
		return "activated"
	case 6:
		return "failed"
	case 7:
		return "disconnected"
	}
	return "unknown"
}

// activeReasonText explains why an activation ended.
func activeReasonText(r uint32) string {
	switch r {
	case activeReasonUnknown, activeReasonNone:
		return "activation failed"
	case activeReasonUserDisconnected:
		return "disconnected by user"
	case activeReasonDeviceDisconnected:
		return "device disconnected"
	case activeReasonServiceStopped:
		return "VPN service stopped"
	case activeReasonIPConfigInvalid:
		return "invalid IP configuration"
	case activeReasonConnectTimeout:
		return "connect timed out"
	case activeReasonServiceStartTO:
		return "VPN service start timed out"
	case activeReasonServiceStartFailed:
		return "VPN service failed to start"
	case activeReasonNoSecrets:
		return "authentication failed (no valid secrets)"
	case activeReasonLoginFailed:
		return "authentication failed (login rejected)"
	case activeReasonConnectionRemoved:
		return "profile was removed"
	case activeReasonDependencyFailed:
		return "a dependency failed"
	case activeReasonDeviceRealizeFail:
		return "device could not be created"
	case activeReasonDeviceRemoved:
		return "device removed"
	}
	return "activation failed"
}

// activeReasonIsAuth says whether an ActiveConnection reason means bad credentials.
func activeReasonIsAuth(r uint32) bool {
	return r == activeReasonNoSecrets || r == activeReasonLoginFailed
}

// devReasonText explains a device StateChanged reason; "" when it adds nothing.
func devReasonText(r uint32) string {
	switch r {
	case devReasonNoSecrets:
		return "authentication failed: NetworkManager needed new secrets (wrong password?)"
	case devReasonSupplicantDisconnect:
		return "authentication failed: the access point disconnected during association (wrong password?)"
	case devReasonSupplicantConfigFail:
		return "authentication failed: the supplicant rejected the configuration"
	case devReasonSupplicantFailed:
		return "authentication failed: the supplicant failed"
	case devReasonSupplicantTimeout:
		return "authentication failed: the supplicant timed out (wrong password or weak signal)"
	case devReasonSSIDNotFound:
		return "the network was not found"
	}
	return ""
}

func devReasonIsAuth(r uint32) bool {
	switch r {
	case devReasonNoSecrets, devReasonSupplicantDisconnect, devReasonSupplicantConfigFail,
		devReasonSupplicantFailed, devReasonSupplicantTimeout:
		return true
	}
	return false
}
