package nm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dopeCape/better-nm/internal/core"
)

func TestDeviceState(t *testing.T) {
	tests := []struct {
		name     string
		state    uint32
		external bool
		want     core.DeviceState
	}{
		{"unmanaged", devStateUnmanaged, false, core.DeviceUnmanaged},
		{"unknown", devStateUnknown, false, core.DeviceUnavailable},
		{"unavailable", devStateUnavailable, false, core.DeviceUnavailable},
		{"disconnected", devStateDisconnected, false, core.DeviceDisconnected},
		{"deactivating", devStateDeactivating, false, core.DeviceDisconnected},
		{"prepare", devStatePrepare, false, core.DeviceConnecting},
		{"need-auth", devStateNeedAuth, false, core.DeviceConnecting},
		{"secondaries", devStateSecondaries, false, core.DeviceConnecting},
		{"activated", devStateActivated, false, core.DeviceConnected},
		{"activated external", devStateActivated, true, core.DeviceExternal},
		{"failed", devStateFailed, false, core.DeviceFailed},
		{"garbage", 999, false, core.DeviceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceState(tt.state, tt.external); got != tt.want {
				t.Fatalf("deviceState(%d,%v)=%q want %q", tt.state, tt.external, got, tt.want)
			}
		})
	}
}

func TestDeviceKind(t *testing.T) {
	tests := map[uint32]core.DeviceKind{
		devTypeEthernet: core.DeviceEthernet, devTypeWifi: core.DeviceWifi, devTypeBridge: core.DeviceBridge,
		devTypeVlan: core.DeviceVlan, devTypeTun: core.DeviceTun, devTypeVeth: core.DeviceVeth,
		devTypeWireGuard: core.DeviceWireGuard, devTypeLoopback: core.DeviceLoopback, 30: core.DeviceOther, 0: core.DeviceOther,
	}
	for in, want := range tests {
		if got := deviceKind(in); got != want {
			t.Errorf("deviceKind(%d)=%q want %q", in, got, want)
		}
	}
}

func TestDeviceClass(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, physical bool) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if physical {
			if err := os.MkdirAll(filepath.Join(dir, "device"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("wlp4s0", true)
	mk("eno1", true)
	mk("docker0", false)
	mk("tailscale0", false)
	mk("lo", false)
	probe := sysfsClassNet(root)

	tests := []struct {
		name string
		kind core.DeviceKind
		want core.DeviceClass
	}{
		{"wlp4s0", core.DeviceWifi, core.ClassPhysical},
		{"eno1", core.DeviceEthernet, core.ClassPhysical},
		{"docker0", core.DeviceBridge, core.ClassInfra},
		{"tailscale0", core.DeviceTun, core.ClassInfra},
		{"lo", core.DeviceLoopback, core.ClassLoopback},
		// unknown to sysfs (dbusmock, container): the kind decides
		{"wlan0", core.DeviceWifi, core.ClassPhysical},
		{"eth0", core.DeviceEthernet, core.ClassPhysical},
		{"br0", core.DeviceBridge, core.ClassInfra},
		{"../etc", core.DeviceEthernet, core.ClassPhysical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceClass(tt.name, tt.kind, probe); got != tt.want {
				t.Fatalf("deviceClass(%q,%q)=%q want %q", tt.name, tt.kind, got, tt.want)
			}
		})
	}
}

func TestDeviceOwner(t *testing.T) {
	tests := []struct {
		name string
		kind core.DeviceKind
		want string
	}{
		{"docker0", core.DeviceBridge, "docker"},
		{"br-0cd29c6f6e64", core.DeviceBridge, "docker"},
		{"br-0cd29c6f6e6", core.DeviceBridge, ""}, // 11 hex chars: not docker's pattern
		{"podman0", core.DeviceBridge, "podman"},
		{"podman1", core.DeviceBridge, "podman"},
		{"virbr0", core.DeviceBridge, "libvirt"},
		{"vnet3", core.DeviceTun, "libvirt"},
		{"incusbr0", core.DeviceBridge, "incus"},
		{"lxdbr0", core.DeviceBridge, "incus"},
		{"lxcbr0", core.DeviceBridge, "incus"},
		{"ve-web", core.DeviceVeth, "nspawn"},
		{"vb-web", core.DeviceVeth, "nspawn"},
		{"vz-web", core.DeviceBridge, "nspawn"},
		{"cni0", core.DeviceBridge, "k8s"},
		{"flannel.1", core.DeviceOther, "k8s"},
		{"cali1234abcd", core.DeviceVeth, "k8s"},
		{"tailscale0", core.DeviceTun, "tailscale"},
		{"vethd0d5b88", core.DeviceVeth, "veth"},
		{"wg0", core.DeviceWireGuard, "wireguard"},
		{"tun-corp", core.DeviceWireGuard, "wireguard"},
		{"br0", core.DeviceBridge, ""},
		{"tun0", core.DeviceTun, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceOwner(tt.name, tt.kind); got != tt.want {
				t.Fatalf("deviceOwner(%q)=%q want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestAPSecurity(t *testing.T) {
	tests := []struct {
		name            string
		flags, wpa, rsn uint32
		want            core.WifiSecurity
	}{
		{"open", 0, 0, 0, core.SecOpen},
		{"wep privacy only", apFlagPrivacy, 0, 0, core.SecWEP},
		{"wep pairwise", apFlagPrivacy, apSecPairWEP104 | apSecGroupWEP104, 0, core.SecWEP},
		{"wpa1 psk", apFlagPrivacy, apSecPairTKIP | apSecGroupTKIP | apSecKeyMgmtPSK, 0, core.SecWPAPSK},
		{"wpa2 psk (live AP)", 1, 0, 392, core.SecWPAPSK},
		{"wpa3 sae only", apFlagPrivacy, 0, apSecPairCCMP | apSecGroupCCMP | apSecKeyMgmtSAE, core.SecSAE},
		{"wpa2/wpa3 transition", apFlagPrivacy, 0, apSecPairCCMP | apSecGroupCCMP | apSecKeyMgmtPSK | apSecKeyMgmtSAE, core.SecWPAPSK},
		{"owe", 0, 0, apSecPairCCMP | apSecGroupCCMP | apSecKeyMgmtOWE, core.SecOWE},
		{"owe transition", 0, 0, apSecKeyMgmtOWETM, core.SecOWE},
		{"eap", apFlagPrivacy, 0, apSecPairCCMP | apSecKeyMgmt8021X, core.SecWPAEAP},
		{"eap suite-b", apFlagPrivacy, 0, apSecKeyMgmtEAPB, core.SecWPAEAP},
		{"eap beats psk", apFlagPrivacy, 0, apSecKeyMgmtPSK | apSecKeyMgmt8021X, core.SecWPAEAP},
		{"dbusmock psk flag", apFlagPrivacy, apSecKeyMgmtPSK, apSecKeyMgmtPSK, core.SecWPAPSK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apSecurity(tt.flags, tt.wpa, tt.rsn); got != tt.want {
				t.Fatalf("apSecurity(%#x,%#x,%#x)=%q want %q", tt.flags, tt.wpa, tt.rsn, got, tt.want)
			}
		})
	}
}

func TestKeyMgmtRoundTrip(t *testing.T) {
	for _, sec := range []core.WifiSecurity{core.SecWPAPSK, core.SecSAE, core.SecOWE, core.SecWEP, core.SecWPAEAP} {
		if got := profileSecurity(keyMgmtFor(sec)); got != sec {
			t.Errorf("profileSecurity(keyMgmtFor(%q))=%q", sec, got)
		}
	}
	if keyMgmtFor(core.SecOpen) != "" || profileSecurity("") != core.SecOpen {
		t.Error("open must map to no key-mgmt and back")
	}
	if profileSecurity("wpa-eap-suite-b-192") != core.SecWPAEAP {
		t.Error("suite-b is EAP")
	}
}

func TestWifiChannel(t *testing.T) {
	tests := []struct {
		freq uint32
		ch   int
		band string
	}{
		{2412, 1, "2.4"}, {2437, 6, "2.4"}, {2472, 13, "2.4"}, {2484, 14, "2.4"},
		{5180, 36, "5"}, {5500, 100, "5"}, {5805, 161, "5"}, {5825, 165, "5"},
		{5955, 1, "6"}, {6115, 33, "6"}, {7115, 233, "6"}, {5935, 2, "6"},
		{0, 0, ""}, {900, 0, ""},
	}
	for _, tt := range tests {
		ch, band := wifiChannel(tt.freq)
		if ch != tt.ch || band != tt.band {
			t.Errorf("wifiChannel(%d)=(%d,%q) want (%d,%q)", tt.freq, ch, band, tt.ch, tt.band)
		}
	}
}

func TestProfileTypeAndStates(t *testing.T) {
	if profileType("802-11-wireless") != core.ProfileWifi || profileType("802-3-ethernet") != core.ProfileEthernet ||
		profileType("wireguard") != core.ProfileWireGuard || profileType("vpn") != core.ProfileVPN ||
		profileType("bridge") != core.ProfileBridge || profileType("tun") != core.ProfileOther {
		t.Error("profileType mapping wrong")
	}
	if activeState(2) != core.ActiveActivated || activeState(1) != core.ActiveActivating ||
		activeState(3) != core.ActiveDeactivating || activeState(4) != core.ActiveDeactivated || activeState(9) != core.ActiveUnknown {
		t.Error("activeState mapping wrong")
	}
	if nmStateName(70) != "connected-global" || nmStateName(20) != "disconnected" || nmStateName(5) != "unknown" {
		t.Error("nmStateName mapping wrong")
	}
	if connectivity(4) != core.ConnFull || connectivity(2) != core.ConnPortal || connectivity(0) != core.ConnUnknown || connectivity(9) != core.ConnUnknown {
		t.Error("connectivity mapping wrong")
	}
	if vpnStateName(5) != "activated" || vpnStateName(2) != "need-auth" || vpnStateName(0) != "unknown" {
		t.Error("vpnStateName mapping wrong")
	}
}

func TestReasonTexts(t *testing.T) {
	if !activeReasonIsAuth(activeReasonNoSecrets) || !activeReasonIsAuth(activeReasonLoginFailed) || activeReasonIsAuth(activeReasonConnectTimeout) {
		t.Error("activeReasonIsAuth wrong")
	}
	for _, r := range []uint32{devReasonNoSecrets, devReasonSupplicantDisconnect, devReasonSupplicantConfigFail, devReasonSupplicantFailed, devReasonSupplicantTimeout} {
		if !devReasonIsAuth(r) || devReasonText(r) == "" {
			t.Errorf("device reason %d should be an auth failure with text", r)
		}
	}
	if devReasonIsAuth(devReasonSSIDNotFound) || devReasonText(devReasonSSIDNotFound) == "" || devReasonText(devReasonNone) != "" {
		t.Error("ssid-not-found / none handling wrong")
	}
	if activeReasonText(activeReasonNoSecrets) == "" || activeReasonText(200) == "" {
		t.Error("activeReasonText must always say something")
	}
}
