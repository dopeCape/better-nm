# Testing the NetworkManager core without a real network

Research for [issue #8](https://github.com/dopeCape/better-nm/issues/8). Written 2026-09-07 against
python-dbusmock 0.38.1 (released 2026-01-30 [S1]), NetworkManager `main`, and the GitHub Actions
`ubuntu-24.04` image version 20260823.283.1 [S13]. Sources are numbered `[Sn]` and listed at the end.

## TL;DR

| Layer | Runs | What | Needs |
|---|---|---|---|
| 1 | every push | Go unit tests against a hand-written fake of bnm's own NM port interface | nothing |
| 2 | every push | Adapter contract tests against `python-dbusmock`'s `networkmanager` template on a private `dbus-daemon` system bus | `dbus-daemon`, `python3-dbusmock`; no root |
| 3 | nightly | Real NetworkManager 1.46 in a `--privileged` container with `veth`/`dummy`/`wireguard` devices and dnsmasq DHCP: ethernet, DHCP, WireGuard, connectivity, secret-agent flows | passwordless sudo, Podman/Docker (both on the runner) |
| 4 | nightly, allowed to fail | Wi-Fi flows against real NM + `mac80211_hwsim` + hostapd inside a QEMU/KVM VM with a `-generic` kernel | `/dev/kvm` on the runner; nested virt is "not officially supported" |
| 5 | manual | Real Wi-Fi hardware (WPA3, roaming, captive portals), real Tailscale/OpenVPN peers, desktop surface | a laptop |

The reason for layer 4 being a VM rather than a container: the runner's kernel is
`6.17.0-1022-azure` [S13] and its `linux-modules-extra` package ships `mac80211.ko`/`cfg80211.ko` but
no `drivers/net/wireless/` modules at all, so there is no `mac80211_hwsim` to `modprobe` [S15].

## 1. python-dbusmock's NetworkManager template

### What it models

The template (`dbusmock/templates/networkmanager.py`) owns the bus name `org.freedesktop.NetworkManager`
on the system bus, exports an ObjectManager rooted at `/org/freedesktop` (`IS_OBJECT_MANAGER = True`),
and on `load()` creates two objects and nothing else [S2]:

- `/org/freedesktop/NetworkManager` (`org.freedesktop.NetworkManager`): properties `ActiveConnections`,
  `Devices`, `NetworkingEnabled`, `Connectivity`, `State`, `Startup`, `Version` (default **`"0.9.6.0"`**),
  `Wimax*`, `Wireless*`, `Wwan*`; methods `GetDevices`, `GetPermissions` (returns `{}`), `state`,
  `CheckConnectivity` (just returns the property), `ActivateConnection`, `DeactivateConnection`,
  `AddAndActivateConnection`, `GetDeviceByIpIface`, `Reload` (no-op), `Enable` [S2].
- `/org/freedesktop/NetworkManager/Settings`: `Hostname`, `CanModify`, `Connections`; `ListConnections`,
  `GetConnectionByUuid`, `AddConnection`, `AddConnectionUnsaved` (same code as `AddConnection`),
  `SaveHostname` (no-op) [S2].

Test-side state is driven through extra methods on the `org.freedesktop.DBus.Mock` interface of the
manager object: `SetProperty`, `SetGlobalConnectionState` (also emits `StateChanged`), `SetConnectivity`,
`SetNetworkingEnabled`, `SetDeviceActive`, `SetDeviceDisconnected`, `AddEthernetDevice`, `AddWiFiDevice`,
`AddAccessPoint`, `AddWiFiConnection`, `AddActiveConnection`, `RemoveAccessPoint`, `RemoveWifiConnection`,
`RemoveActiveConnection` [S2]. Devices get `org.freedesktop.NetworkManager.Device` plus either
`Device.Wired` (`Carrier`, `HwAddress`, `Speed`) or `Device.Wireless` (`AccessPoints`, `GetAccessPoints`,
`GetAllAccessPoints`, `RequestScan` as a no-op) and emit `DeviceAdded`; `Driver` is the string
`"dbusmock"` [S2]. Settings connections store whatever `a{sa{sv}}` you pass, unvalidated; `GetSettings`
strips a `secrets` key and `GetSecrets` returns it, `Update` and `Delete` keep `Settings.Connections`,
device `AvailableConnections`, and active connections consistent and emit `NewConnection`,
`ConnectionRemoved`, `Removed` [S2]. Activation is instantaneous: `ActivateConnection` creates an
`ActiveConnection` object already in state `ACTIVATED` [S2].

Upstream's own test drives it with `nmcli --nocheck general|networking|connection|dev|dev wifi list` and
`nmcli dev wifi connect`, asserting on nmcli's output [S3], so the surface is known to satisfy libnm's
ObjectManager client for those operations.

### Gaps that matter for bnm

- **No secret agent.** `AgentManager` is absent; since NM 1.49.3 `nmcli dev wifi connect ... password`
  hangs against the mock and upstream skips that test (open issue #216, filed 2024-08-25) [S3][S4].
  Anything in bnm that registers an agent or expects `GetSecrets` round-trips cannot be tested here.
- **No WireGuard, VPN, or tunnel objects.** The real API has `Device.WireGuard`, `VPN.Connection`,
  `Device.IPTunnel`, etc. [S5]; the template has only Wired and Wireless devices and marks an active
  connection `Vpn` purely from `connection.type == "vpn"` [S2]. WireGuard *settings* can be stored (the
  dict is opaque) but no device, peer, or handshake state exists.
- **No IP state.** No `IP4Config`/`IP6Config`/`DHCP4Config` objects, no `Ip4Config` device properties,
  no `Checkpoint`, no `DnsManager`, no `PrimaryConnection` [S2][S5].
- **No state machine.** No `ACTIVATING` step, no failure injection, no `StateChanged` on devices beyond
  what `SetDeviceActive` sets, `RequestScan` never emits `AccessPointAdded` [S2].
- `Version` defaults to `0.9.6.0`; pass `{"Version": "1.46.0"}` in template parameters if the adapter
  gates on it [S2].
- `AddAndActivateConnection` assumes the specific object is an access point (it derives the connection
  name from the AP path and calls `AddWiFiConnection`), so ethernet `AddAndActivate` is not modelled [S2].

### Driving it from Go

dbusmock is language-agnostic: "You can use this with any programming language, as you can run the
mocker as a normal program. The actual setup of the mock ... all happen via D-Bus methods on the
`org.freedesktop.DBus.Mock` interface" [S1]. Recipe:

1. Start a private system bus. dbusmock's `DBusTestCase.start_system_bus()` runs
   `dbus-daemon --config-file=... --print-pid=1` and exports its address as `DBUS_SYSTEM_BUS_ADDRESS`
   [S6]; from Go, spawn `dbus-daemon --session --print-address --nofork` (or a config file with
   `<type>system</type>`) yourself and set the variable in the test process.
2. `python3 -m dbusmock --system --template networkmanager` (optionally
   `--parameters '{"Version":"1.46.0"}'`) [S1][S7]. Add `-l logfile` or read stdout for the call log,
   or call `GetCalls`/`GetMethodCalls`/`ClearCalls` on the Mock interface [S1].
3. godbus's `SystemBus()`/`ConnectSystemBus()` read `DBUS_SYSTEM_BUS_ADDRESS` before falling back to
   `unix:path=/var/run/dbus/system_bus_socket` [S8], so the production adapter needs no test hook.
4. From the test, call `org.freedesktop.DBus.Mock.AddEthernetDevice(...)` etc. with a plain
   `conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager").Call(...)`.

Packaging: `python3-dbusmock` is a distro package (the runner's image already has Python 3.12 and
`dbus` 1.14.10 [S13]); `pip install python-dbusmock` also works. Expect a `dbus-python` dependency.

## 2. NetworkManager's own test tooling

**`tools/test-networkmanager-service.py`** is NM's stub daemon, used by `src/tests/client/test-client.py`,
which "starts NetworkManager stub service in a user D-Bus session, and runs nmcli against it", comparing
against committed expected output (`NM_TEST_REGENERATE=1` rewrites it) [S9][S10]. It is considerably
richer than dbusmock's template: it models `IP4Config`/`IP6Config`/`DHCP4Config`/`DHCP6Config`,
`VPN.Connection`, `Device.Vlan`/`Macvlan`/`Modem`, `AccessPoint`, an `AgentManager` that really calls
`GetSecrets` on registered agents, and `ActiveConnection` with staged activation and per-connection
forced failure [S10]. Its control interface `org.freedesktop.NetworkManager.LibnmGlibTest` offers
`AddWiredDevice`, `AddWifiDevice`, `AddWifiAp`, `RemoveDevice`, `SetActiveConnectionFailure`,
`SetActiveConnectionStateChangedDelay`, `SetCarrierStatus`, `SetProperties`, `AutoRemoveNextConnection`,
`Restart`, `Quit` [S10]. It validates submitted settings with libnm (`NM.SimpleConnection.new_from_dbus`,
`normalize()`, `verify()`) and returns real NM error names such as
`org.freedesktop.NetworkManager.Settings.Connection.InvalidProperty` [S10].

Costs: it imports `gi.repository.NM` (needs `gir1.2-nm-1.0`/libnm typelib) and `dbus-python`, reports
`Version` `0.9.9.0` unless `NM_TEST_NETWORKMANAGER_SERVICE_VERSION` is set, and is not shipped as a
package; you would vendor the single file from the NM tree [S10]. It still has no WireGuard device.
Verdict: worth vendoring for the secret-agent and activation-failure paths that dbusmock cannot cover,
if bnm's adapter turns out to need them; otherwise dbusmock's smaller surface is enough for layer 2.

**Platform tests** (`src/core/platform/tests`) exercise NM's netlink layer against real kernel devices
created with `ip link add ... type veth|dummy|bridge|...` [S11]. When not root they `unshare(CLONE_NEWUSER)`
then `unshare(CLONE_NEWNET | CLONE_NEWNS)`, skipping (or failing with `REQUIRE_ROOT_TESTS`) if that is
denied [S11]. They are C and NM-internal, so not reusable, but the pattern (a fresh user+net namespace
per test binary) is the cheapest way to get root-like device control on a CI box.

**`tools/nm-in-container` / `nm-in-vm`** build a Fedora podman image and start it with
`podman run --privileged`, bind-mounting the tree; `nm-env-prepare.sh` creates a `net1` test
interface [S12]. This is upstream's own answer to "run a real NM without touching the host".

**NetworkManager-ci** is the behave-based integration suite that runs nightly in CentOS VMs on CentOS
CI [S14]. Its `prepare/` directory shows what a full bed looks like: `vethsetup.sh` ("creates whole 11
device wide test bed"), `hostapd_wireless.sh` (hostapd instances on `wlanN` radios found via `iw`,
optional `wlan_ns` namespace, dnsmasq DHCP on `10.0.254.0/24`, WPA-PSK/EAP/WPA3/OWE profiles gated on
hostapd version), `wireguard.sh`, `netdevsim.sh`, `libreswan.sh`, `strongswan.sh`, `captive_portal.sh`
[S14]. Those scripts assume `wlanN` interfaces already exist, i.e. `mac80211_hwsim` is loaded on the VM.

## 3. Real NM in a container or VM on GitHub Actions

**What the runner allows.** "The Linux and macOS virtual machines both run using passwordless `sudo`"
[S16]; public-repo Linux runners have 4 vCPU / 16 GB / 14 GB [S16]. The `ubuntu-24.04` image ships
kernel `6.17.0-1022-azure`, Docker 28, Podman 5.8.4, Python 3.12, `dbus` 1.14.10, and **no
NetworkManager, hostapd or wpa_supplicant** [S13]; `apt install network-manager` gives 1.46.0 on
noble [S17]. `/dev/kvm` is exposed on the Linux runners (GitHub's 2024-04-02 changelog adds a udev rule
`KERNEL=="kvm", GROUP="kvm", MODE="0666"` to use it) [S18], while the docs say "nested virtualization
is technically possible while using runners, it is not officially supported" [S19].

**Kernel modules.** For the exact runner kernel, `linux-modules-6.17.0-1022-azure` contains `veth.ko`,
`dummy.ko`, `bridge.ko` and `wireguard.ko` [S20], and `linux-modules-extra-6.17.0-1022-azure`
(installable with `sudo apt install linux-modules-extra-$(uname -r)` [S21]) adds `netdevsim.ko`,
`mac80211.ko`, `cfg80211.ko` but contains **no `drivers/net/wireless/` modules and therefore no
`mac80211_hwsim`** [S15]. (Older `6.8.0-*-azure-fde` builds did ship it [S22]; do not rely on that.)

**Container layer (nightly, reliable).** Run Ubuntu 24.04 + `network-manager` in
`docker run --privileged` on the runner. Inside: `dbus-daemon --system`, `NetworkManager --no-daemon`,
then create `veth` pairs with one end in a peer namespace running `dnsmasq` for DHCP, and `dummy`
devices for static tests. This covers: device enumeration, ethernet activation, DHCP/static IP config,
`IP4Config`, connectivity-state transitions, `Settings` CRUD with NM's real validation, secret-agent
registration, checkpoints, and WireGuard (create two `wireguard` connections in two namespaces and
verify a handshake) with the real `wireguard.ko` [S20]. `--privileged` is what upstream's own
`nm-in-container` uses [S12].

**VM layer (nightly, best-effort).** Boot a `-generic` Ubuntu cloud image under QEMU with KVM (from
`linux-modules-extra-*-generic`, which does include `mac80211_hwsim` [S22]), `modprobe mac80211_hwsim
radios=2` [S23], run hostapd on `wlan0` and NM on `wlan1`. The kernel doc: hwsim "can be used to test
most of the mac80211 functionality and user space tools (e.g., hostapd and wpa_supplicant) in a way that
matches very closely with the normal case of using real WLAN hardware", copies frames between radios on
the same channel, and uses real software encryption [S23]. This gives scan results, WPA2-PSK, WPA3/OWE
(hostapd version permitting) and reconnect/roam scenarios. Because nested virtualization is unsupported
by GitHub [S19], keep this job `continue-on-error` or on a self-hosted runner; do not gate merges on it.

## 4. Interface-level fakes in Go

The daemon should talk to NM only through a narrow port interface it owns (e.g. `nm.Client` with
`Devices()`, `Connections()`, `Activate()`, `Watch()`), implemented once over D-Bus and once by an
in-memory fake with a scriptable event channel. Precedent: `gonetworkmanager` exposes every NM object as
a Go interface (`NetworkManager`, `Device`, `Connection`, `ActiveConnection`, ...) [S24], which is the
same shape; note its README says it has "no automated tests" and targets NM "0.9 to 1.40" [S25], so treat
it as a reference, not a dependency. If a Go-only D-Bus fake is ever wanted (to drop the Python dep),
godbus can serve one: `Conn.Export`/`ExportSubtree` register Go values as D-Bus objects and
`Conn.RequestName` claims the bus name [S26], so a Go program can be `org.freedesktop.NetworkManager`
on the private bus from step 1 of section 1.

Fakes cannot catch marshalling mistakes (`a{sa{sv}}` variants, object-path vs string), signal
subscription bugs, or ObjectManager ordering; that is exactly what layer 2 exists for.

## 5. Recommendation

**Every push (minutes, no root).**
- `go test ./...` with the in-memory fake for daemon, CLI and TUI logic.
- Adapter contract tests in `internal/nm/dbus` behind a build tag: start `dbus-daemon`, start
  `python3 -m dbusmock --system --template networkmanager --parameters '{"Version":"1.46.0"}'`, and
  exercise enumerate / watch / add / update / delete / activate / deactivate. Keep secret-agent, WireGuard
  device, IP-config and failure-path assertions out of this layer; they are not modelled [S2][S4].
- Optional: the same tests against vendored `test-networkmanager-service.py` for agent and
  activation-failure paths, if the adapter grows those.

**Nightly.**
- Privileged-container job with real NM 1.46, veth/dummy/wireguard, dnsmasq: end-to-end `bnm` daemon
  plus CLI against a real daemon; WireGuard peer handshake; DHCP; checkpoints.
- KVM VM job with hwsim + hostapd for Wi-Fi; `continue-on-error: true`.
- Re-run the push-layer tests against a second NM version (e.g. 1.54 from a Fedora container) to catch
  API drift, since dbusmock freezes one shape of the API.

**Manual (release checklist).** Real Wi-Fi hardware incl. WPA3-SAE, enterprise EAP and captive portals;
Tailscale and OpenVPN against real endpoints; the desktop surface; suspend/resume and roaming.

**What the architecture ticket must know.** (1) Put a Go port interface between the daemon and D-Bus
from day one, or layer 1 does not exist. (2) The adapter must not require a secret agent for read paths,
must tolerate `Version` strings it does not recognise, and must not assume `Driver`/`IP4Config` are
populated, or it cannot run against dbusmock. (3) WireGuard and VPN device semantics have no mock
anywhere; those code paths get their first real test only in the nightly container. (4) Wi-Fi beyond
"list APs / connect open or PSK" is VM-only and cannot block merges on GitHub-hosted runners.

## Sources

- [S1] python-dbusmock README, https://github.com/martinpitt/python-dbusmock/blob/main/README.md; release 0.38.1, https://github.com/martinpitt/python-dbusmock/releases/tag/0.38.1
- [S2] `dbusmock/templates/networkmanager.py`, https://github.com/martinpitt/python-dbusmock/blob/main/dbusmock/templates/networkmanager.py
- [S3] `tests/test_networkmanager.py`, https://github.com/martinpitt/python-dbusmock/blob/main/tests/test_networkmanager.py
- [S4] "Needs updating for NetworkManager 1.49.3 secret agent", https://github.com/martinpitt/python-dbusmock/issues/216
- [S5] NetworkManager D-Bus API reference (interface index), https://networkmanager.dev/docs/api/latest/spec.html
- [S6] `dbusmock/testcase.py` (`start()` sets the bus address env var), https://github.com/martinpitt/python-dbusmock/blob/main/dbusmock/testcase.py
- [S7] `dbusmock/__main__.py` (`--system`, `--template`, `--parameters`), https://github.com/martinpitt/python-dbusmock/blob/main/dbusmock/__main__.py
- [S8] godbus `conn_unix.go` `getSystemBusPlatformAddress`, https://github.com/godbus/dbus/blob/master/conn_unix.go
- [S9] NetworkManager `src/tests/client/test-client.py`, https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/blob/main/src/tests/client/test-client.py
- [S10] NetworkManager `tools/test-networkmanager-service.py`, https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/blob/main/tools/test-networkmanager-service.py
- [S11] NetworkManager `src/core/platform/tests/test-common.c` (`nmtstp_link_veth_add`, `unshare_user`, `main`), https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/blob/main/src/core/platform/tests/test-common.c
- [S12] NetworkManager `tools/nm-in-container` and `tools/nm-guest-data/README.md`, https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/blob/main/tools/nm-in-container
- [S13] actions/runner-images Ubuntu 24.04 image readme (image 20260823.283.1), https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md
- [S14] NetworkManager-ci README and `prepare/` scripts, https://gitlab.freedesktop.org/NetworkManager/NetworkManager-ci
- [S15] File list of `linux-modules-extra-6.17.0-1022-azure` (noble-updates, amd64), https://packages.ubuntu.com/noble-updates/amd64/linux-modules-extra-6.17.0-1022-azure/filelist
- [S16] GitHub Docs, "GitHub-hosted runners reference", https://docs.github.com/en/actions/reference/runners/github-hosted-runners
- [S17] Ubuntu noble `network-manager` 1.46.0-1ubuntu2, https://packages.ubuntu.com/noble/network-manager
- [S18] GitHub Changelog 2024-04-02, "Hardware accelerated Android virtualization now available", https://github.blog/changelog/2024-04-02-github-actions-hardware-accelerated-android-virtualization-now-available/
- [S19] GitHub Docs, "About GitHub-hosted runners" (nested virtualization statement), https://docs.github.com/en/actions/using-github-hosted-runners/using-github-hosted-runners/about-github-hosted-runners
- [S20] File list of `linux-modules-6.17.0-1022-azure`, https://packages.ubuntu.com/noble-updates/amd64/linux-modules-6.17.0-1022-azure/filelist
- [S21] actions/runner-images issue #7587 (`linux-modules-extra-$(uname -r)` workaround), https://github.com/actions/runner-images/issues/7587
- [S22] Ubuntu contents search for `mac80211_hwsim.ko` (noble-updates, amd64), https://packages.ubuntu.com/search?searchon=contents&keywords=mac80211_hwsim.ko&mode=filename&suite=noble-updates&arch=amd64
- [S23] Linux kernel, `Documentation/networking/mac80211_hwsim/mac80211_hwsim.rst`, https://github.com/torvalds/linux/blob/master/Documentation/networking/mac80211_hwsim/mac80211_hwsim.rst
- [S24] gonetworkmanager `NetworkManager.go`, https://github.com/Wifx/gonetworkmanager/blob/master/NetworkManager.go
- [S25] gonetworkmanager README, https://github.com/Wifx/gonetworkmanager/blob/master/README.md
- [S26] godbus `export.go` (`Export`, `ExportSubtree`, `RequestName`), https://github.com/godbus/dbus/blob/master/export.go
