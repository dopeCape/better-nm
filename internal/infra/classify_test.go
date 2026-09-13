package infra

import (
	"testing"

	"github.com/dopeCape/better-nm/internal/core"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name, kind string
		sysfs      bool
		wantClass  core.DeviceClass
		wantOwner  string
	}{
		{"lo", "loopback", false, core.ClassLoopback, "loopback"},
		{"lo", "device", false, core.ClassLoopback, "loopback"},
		{"wlp4s0", "device", true, core.ClassPhysical, ""},
		{"eno1", "device", true, core.ClassPhysical, ""},
		{"docker0", "bridge", false, core.ClassInfra, "docker"},
		{"br-f1926d4e98a8", "bridge", false, core.ClassInfra, "docker"},
		{"br-0cd29c6f6e64", "bridge", false, core.ClassInfra, "docker"},
		{"br-notdocker", "bridge", false, core.ClassInfra, ""},
		{"br0", "bridge", false, core.ClassInfra, ""},
		{"podman0", "bridge", false, core.ClassInfra, "podman"},
		{"cni-podman1", "bridge", false, core.ClassInfra, "podman"},
		{"virbr0", "bridge", false, core.ClassInfra, "libvirt"},
		{"vnet3", "tun", false, core.ClassInfra, "libvirt"},
		{"incusbr0", "bridge", false, core.ClassInfra, "incus"},
		{"lxdbr0", "bridge", false, core.ClassInfra, "incus"},
		{"lxcbr0", "bridge", false, core.ClassInfra, "incus"},
		{"ve-mymachine", "veth", false, core.ClassInfra, "nspawn"},
		{"vb-mymachine", "veth", false, core.ClassInfra, "nspawn"},
		{"vz-zone", "bridge", false, core.ClassInfra, "nspawn"},
		{"cni0", "bridge", false, core.ClassInfra, "k8s"},
		{"flannel.1", "vxlan", false, core.ClassInfra, "k8s"},
		{"cali1234abcd", "veth", false, core.ClassInfra, "k8s"},
		{"kube-ipvs0", "dummy", false, core.ClassInfra, "k8s"},
		{"tailscale0", "tun", false, core.ClassInfra, "tailscale"},
		{"ts0", "tun", false, core.ClassInfra, "tailscale"},
		{"ts0", "device", false, core.ClassInfra, ""},
		{"wg0", "wireguard", false, core.ClassInfra, "wireguard"},
		{"wgcorp", "device", false, core.ClassInfra, "wireguard"},
		{"proton", "wireguard", false, core.ClassInfra, "wireguard"},
		{"vethd0d5b88", "veth", false, core.ClassInfra, "veth"},
		{"dummy0", "dummy", false, core.ClassInfra, ""},
		// a physical NIC named like a runtime is still physical
		{"veth0", "device", true, core.ClassPhysical, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+tc.kind, func(t *testing.T) {
			class, owner := Classify(tc.name, tc.kind, tc.sysfs)
			if class != tc.wantClass || owner != tc.wantOwner {
				t.Fatalf("Classify(%q,%q,%v) = (%q,%q), want (%q,%q)",
					tc.name, tc.kind, tc.sysfs, class, owner, tc.wantClass, tc.wantOwner)
			}
		})
	}
}

func TestHasSysfsDevice(t *testing.T) {
	root := t.TempDir()
	old := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = old })

	mustMkdir(t, root+"/eth0/device")
	mustMkdir(t, root+"/docker0")

	tests := []struct {
		name string
		want bool
	}{
		{"eth0", true},
		{"docker0", false},
		{"missing", false},
		{"", false},
		{"../etc", false},
	}
	for _, tc := range tests {
		if got := HasSysfsDevice(tc.name); got != tc.want {
			t.Errorf("HasSysfsDevice(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
