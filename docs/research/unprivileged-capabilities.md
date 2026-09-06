# Unprivileged capabilities for probes, discovery and wg-quick

Resolves [issue #7](https://github.com/dopeCape/better-nm/issues/7). The `bnm`
user-session daemon runs without root. This note establishes, from primary
sources, what that permits on NixOS, Arch, Debian/Ubuntu and Fedora, and what
needs a documented one-time setup. Claims were also checked read-only on the
author's NixOS 26.05 box (kernel 7.1.8, NetworkManager 1.56.0, uid 1000 in
`users`, `wheel`, `networkmanager`); those checks are marked *verified locally*.

## Summary table

| Capability | Unprivileged? | One-time setup (if needed) | Notes |
|---|---|---|---|
| ICMP echo (v4+v6) via `SOCK_DGRAM` "ping socket" | **Yes on NixOS, Arch, Fedora** (gid range `0 2147483647` shipped) | **Debian/Ubuntu: yes, one of:** (a) drop-in `/etc/sysctl.d/50-bnm.conf` with `net.ipv4.ping_group_range = 0 2147483647`; or (b) `setcap cap_net_raw+ep bnmd` and use raw ICMP | Kernel default is `1 0` = nobody. One sysctl governs v4 and v6. |
| TCP-connect latency fallback | **Yes, everywhere** | none | Handshake RTT via `connect()` timing or `TCP_INFO`. |
| Neighbour table (ARP/NDP cache) via netlink `RTM_GETNEIGH` | **Yes** | none | Same data as `/proc/net/arp` (v4 only) but incl. IPv6 + events. |
| Populating the cache by probing every subnet address | **Yes** (needs the ping-socket row above, or plain UDP) | none beyond ICMP row | *verified locally*: a probe to a silent address creates an `INCOMPLETE` entry. |
| mDNS / DNS-SD browse | **Yes** | none | Ephemeral-port one-shot query; or ask Avahi over D-Bus. |
| SSDP (UPnP) `M-SEARCH` | **Yes** | none | Plain UDP multicast to `239.255.255.250:1900`. |
| ARP scan (`AF_PACKET`) | No | `setcap cap_net_raw+ep bnmd` | Only needed for hosts that answer ARP but drop all IP probes. |
| Local listening sockets, all users (addr, port, uid, inode) | **Yes** (`sock_diag` or `/proc/net/{tcp,udp}{,6}`) | none | |
| ...with process name/PID | **Own uid only** | root / `CAP_SYS_PTRACE` helper for other uids | inode→pid needs `/proc/PID/fd`, gated by ptrace access mode. *verified locally*: 6 of 19 listeners attributed. |
| Port scan of other hosts | **Yes: TCP connect scan**; UDP send + ICMP-unreachable via error queue | `cap_net_raw` for SYN/stealth scans | nmap itself falls back to `-sT` without raw privileges. |
| `wg-quick up` | **No** (script `exec`s `sudo` when `$UID != 0`) | `pkexec` + custom polkit action, or a `setcap`'d Go helper, or NM-native WireGuard | `setcap` on `wg-quick` itself is useless (bash script; children need caps). |
| NM-native WireGuard profile, **user-owned** (`connection.permissions=user:<me>`) | **Yes, in an active local session** on all four distros | none | Uses `settings.modify.own` (allow_active **yes**) + `network-control` (allow_active **yes**). |
| NM-native WireGuard profile, **system-wide** | Prompt (`auth_admin_keep`) by default | NixOS: `networkmanager` group; Debian/Ubuntu: `netdev` or `sudo` group; Arch and Fedora ≥1.56: `wheel` (`polkit_noauth_group`); else a polkit rule | |

## 1. ICMP echo without root

The kernel's ICMP datagram ("ping") socket is `socket(AF_INET, SOCK_DGRAM,
IPPROTO_ICMP)`; the IPv6 variant shares the code (`ping_init_sock` sets
`sk_ipv6only` for `AF_INET6`). Creation is allowed only if the caller's egid or
any supplementary gid falls inside `net.ipv4.ping_group_range`, otherwise
`-EACCES` ([net/ipv4/ping.c][ping.c]). The sysctl's kernel default is `"1 0"`,
"meaning, that nobody (not even root) may create ping sockets"
([ip-sysctl][ipsysctl]; also [icmp(7)][icmp7], "since Linux 2.6.39").
Raw ICMP needs `CAP_NET_RAW` ([raw(7)][raw7]). *Verified locally*: `SOCK_DGRAM`
ICMP and ICMPv6 open fine; raw ICMP and `AF_PACKET` return `EPERM`.

Distro defaults (who ships the sysctl):

- **Upstream systemd** `sysctl.d/50-default.conf`: `# ping(8) without
  CAP_NET_ADMIN and CAP_NET_RAW` / `-net.ipv4.ping_group_range = 0 2147483647`
  ([systemd source][sd50]).
- **NixOS**: `boot.kernel.sysctl."net.ipv4.ping_group_range" = mkDefault
  "0 2147483647"` in `nixos/modules/tasks/network-interfaces.nix` ([nixpkgs][nixnet]).
  *Verified locally*: `sysctl net.ipv4.ping_group_range` → `0 2147483647`.
- **Arch**: ships upstream `usr/lib/sysctl.d/50-default.conf` unmodified
  (systemd 261.2-1 file list, [archlinux.org][archfiles]).
- **Fedora**: same file, packaged in `systemd-udev`
  (`usr/lib/sysctl.d/50-default.conf`, systemd 262~rc1-4.fc46,
  [packages.fedoraproject.org][fedfiles]); the spec's `split-files.py` routes
  `sysctl` paths to the udev subpackage.
- **Debian/Ubuntu**: **not shipped**. Debian's systemd packaging lists
  `usr/lib/sysctl.d/50-default.conf` in `debian/not-installed`
  ([sources.debian.org][debnotinst]); the bookworm and noble `systemd` file
  lists carry only `50-pid-max.conf` ([Debian][debfiles], [Ubuntu][ubufiles]),
  and `procps` ships nothing touching it ([Debian][debprocps],
  [Ubuntu][ubuprocps]). Result: kernel default `1 0`; ping sockets fail with
  `EACCES` for everyone. (Debian's own `ping` works because `iputils-ping` uses
  file capabilities, which does not help a Go binary.)

**Setup for Debian/Ubuntu** (pick one, document in the installer): a
`sysctl.d` drop-in with the systemd line above (needs root once, applies to all
users, survives upgrades); or `setcap cap_net_raw+ep /path/to/bnmd` and send raw
ICMP. Prefer the sysctl: it keeps `bnmd` capability-free, and file caps are
ignored on `nosuid` mounts and under `no_new_privs` ([execve(2)][execve2]).

Go: [`golang.org/x/net/icmp`][xicmp] `ListenPacket("udp4"|"udp6", ...)` is the
non-privileged endpoint ("you may need to adjust the net.ipv4.ping_group_range
kernel state"); [`prometheus-community/pro-bing`][probing] wraps it and
defaults to the unprivileged UDP mode, `SetPrivileged(true)` selects raw ICMP.

## 2. TCP-connect latency fallback

An ordinary `connect()` to a TCP port needs no privilege; timing it gives the
SYN/SYN-ACK RTT. After connect, `getsockopt(TCP_INFO)` returns `struct
tcp_info` ([tcp(7)][tcp7]) whose `Rtt`/`Rttvar` are exposed by
[`unix.GetsockoptTCPInfo`][xsys]. Use it as the fallback when the ping row above
is unavailable (Debian/Ubuntu without setup) or when ICMP is filtered.

## 3. LAN device discovery without `CAP_NET_RAW`

**Neighbour table (netlink).** `RTM_GETNEIGH` returns `struct ndmsg` entries
(family, ifindex, state, lladdr) ([rtnetlink(7)][rtnl7]). Kernel-side, only
non-GET rtnetlink messages require `CAP_NET_ADMIN`:
`if (kind != RTNL_KIND_GET && !netlink_net_capable(skb, CAP_NET_ADMIN)) return
-EPERM;` ([net/core/rtnetlink.c][rtnlc]). So dumps and `RTNLGRP_NEIGH`
subscriptions are unprivileged; adding/deleting entries is not (also
[arp(7)][arp7]: `SIOCSARP`/`SIOCDARP` need `CAP_NET_ADMIN`). `/proc/net/arp` is
the same v4 table as text ([proc_net(5)][procnet]); *verified locally* it is
mode `0444`. Prefer netlink: it also covers IPv6 (NDP) and gives change events.
Go: [`vishvananda/netlink`][vnl] `NeighList(linkIndex, family)`,
`NeighSubscribe(ch, done)`.

**Filling the table without ARP scanning.** The cache is populated by normal
traffic; sending an IP packet to each address of the connected subnet makes the
kernel resolve it. *Verified locally*: one ICMP echo to a silent
`192.168.1.250` created an `INCOMPLETE` entry; a live gateway shows `REACHABLE`
with its MAC. So "sweep the /24 with ping sockets (or UDP to a closed port),
then dump neighbours" finds any host that answers ARP/NDP, whether or not it
answers ICMP. Only hosts that ignore all IP traffic *and* ARP are missed.

**ARP scanning** needs `AF_PACKET`: "In order to create a packet socket, a
process must have the CAP_NET_RAW capability" ([packet(7)][packet7]). It is
only worth a `setcap` for the corner case above; not recommended by default.

**mDNS / DNS-SD.** Queries go to `224.0.0.251:5353` / `[FF02::FB]:5353`; a
one-shot resolver may send from "a high-numbered ephemeral UDP source port"
([RFC 6762][rfc6762]); service types are enumerated with a PTR query for
`_services._dns-sd._udp.local` ([RFC 6763 §9][rfc6763]). Multicast
send/join is an ordinary socket operation. *Verified locally*: the query is
sent without error from a plain user socket (zero responders on this Wi-Fi, so
responder handling was not exercised). If `avahi-daemon` runs, browse through
its D-Bus API instead of competing for port 5353: `ServiceTypeBrowserNew`,
`ServiceBrowserNew`, `ServiceResolverNew` ([org.freedesktop.Avahi.Server][avahi]).
Go: [`grandcat/zeroconf`][zeroconf] `Resolver.Browse(ctx, service, domain,
entries)`; [`holoplot/go-avahi`][goavahi] for the D-Bus route.

**SSDP.** `M-SEARCH * HTTP/1.1` to `239.255.255.250:1900` with `MAN:
"ssdp:discover"`, `MX`, `ST` ([UPnP Device Architecture 2.0 §1.3.2][upnp];
port 1900/udp = SSDP in the [IANA registry][iana]). Responses are unicast back
to the sender's port, so no privileged bind is needed. Go:
[`koron/go-ssdp`][gossdp] `Search(searchType, waitSec, localAddr, opts...)`.

## 4. Local open ports

`NETLINK_SOCK_DIAG` / `inet_diag` dumps every TCP/UDP socket in the netns with
`idiag_uid` and `idiag_inode` ([sock_diag(7)][sockdiag7]); those fields are
filled for all callers, only `INET_DIAG_MARK` and mark-based filters are gated on
`CAP_NET_ADMIN` ([net/ipv4/inet_diag.c][inetdiag]), and only `SOCK_DESTROY`
checks `CAP_NET_ADMIN` ([net/core/sock_diag.c][sockdiagc]). `/proc/net/tcp`
carries the same rows incl. the creator's effective uid ([proc_net(5)][procnet]).
*Verified locally*: `ss -tlnp` lists all 19 listeners as a plain user.

Process attribution is the limit: mapping inode→PID means reading
`/proc/PID/fd`, which "is governed by a ptrace access mode
PTRACE_MODE_READ_FSCREDS check" ([proc_pid_fd(5)][procfd]); `hidepid` can hide
other users' `/proc/PID` entirely ([proc(5)][proc5]). *Verified locally*: `ss`
attributed 6 of 19 listeners (my uid); `/proc/1/fd` is `Permission denied`.
Show the uid/user for the rest, and offer "run helper as root" for full names.
Go: [`vishvananda/netlink`][vnl] `SocketDiagTCP(family)`, `SocketDiagUDP`,
`SocketDiagTCPInfo`.

## 5. Port scanning other hosts

TCP connect scan is unprivileged and is exactly what nmap does when it lacks
raw privileges: "TCP connect scan is the default TCP scan type when SYN scan is
not an option. This is the case when a user does not have raw packet
privileges" ([nmap reference][nmapct]); downsides are more packets and full
connections that targets may log. SYN/FIN/idle scans need `CAP_NET_RAW`
([raw(7)][raw7]). UDP probes are plain sockets; closed ports surface as
`ECONNREFUSED` from the ICMP unreachable. Go: `net.Dialer` with per-port
timeouts; no library needed.

## 6. WireGuard without root

**`wg-quick` cannot run unprivileged.** It is `#!/bin/bash` and its first
action is `[[ $UID == 0 ]] || exec sudo -p "$PROGRAM must be run as root..." --
"$BASH" -- "$SELF" "${ARGS[@]}"` ([wireguard-tools linux.bash][wgq]). It then
shells out to `ip link add ... type wireguard`, `wg set ... fwmark`, `sysctl`,
`nft`/`iptables-restore`, `resolvconf` (same source), each of which needs
`CAP_NET_ADMIN` (interface/route/firewall configuration,
[capabilities(7)][caps7]; the WireGuard genl `GET_DEVICE`/`SET_DEVICE` ops are
flagged `GENL_UNS_ADMIN_PERM` [drivers/net/wireguard/generated/netlink.c][wggenl]).
`setcap` on `wg-quick` is meaningless: "Linux ... ignores the set-user-ID and
set-group-ID bits on scripts" and file capabilities are handled likewise
([execve(2)][execve2]), and caps would not flow to `ip`/`wg` children anyway
(only ambient caps survive `execve`, [capabilities(7)][caps7]).

Alternatives, in order of preference:

1. **NM-native WireGuard (recommended).** NetworkManager ≥ 1.16 has a
   `wireguard` connection type with `private-key`, `listen-port`, `fwmark`,
   `peers`, `peer-routes`, `ip4-auto-default-route`/`ip6-auto-default-route`
   (≥ 1.20, "policy routing like wg-quick with TABLE=auto")
   ([settings-wireguard][nmwg], [Haller, 2019][thaller]). `nmcli connection
   import type wireguard file wg0.conf` reads wg-quick files but "PreUp,
   PostUp, PreDown, and PostDown keys are ignored" ([Haller][thaller]). NM runs
   as root and does the netlink work; `bnm` only needs D-Bus authorisation:
   - `network-control` (activate/deactivate): `allow_active` **yes**
     ([NM policy][nmpol]).
   - Adding/changing the profile: NM picks `settings.modify.own` when "the
     caller is the only user in the connection's permissions"
     (`nm_setting_connection_get_num_permissions(s_con) == 1`), else
     `settings.modify.system` ([nm-settings.c][nmsettings]). `modify.own` is
     `allow_active` **yes**, `modify.system` is `auth_admin_keep` everywhere
     ([NM policy][nmpol]). Set `connection.permissions = user:<name>:`
     ([settings-connection][nmconn]) and no prompt appears in an active local
     session, on every distro. Trade-off from the same doc: such a profile
     "can be active only when one of the specified users is logged into an
     active session", i.e. no always-on/boot-time tunnel.
   - For system-wide profiles the distros differ: NixOS adds a rule granting
     all `org.freedesktop.NetworkManager.*` actions to group `networkmanager`
     ([nixpkgs module][nixnm]; *verified locally* in
     `/etc/polkit-1/rules.d/10-nixos.rules`); Debian/Ubuntu ship
     `org.freedesktop.NetworkManager.rules` granting `modify.system` to
     `subject.local && subject.active` members of `sudo` or `netdev`
     ([Debian packaging][debnmrules], [Ubuntu file list][ubunm]) and build with
     `-Dmodify_system=false` ([debian/rules][debnmbuild]); Arch and Fedora
     (since 1.56.0-1, rhbz#2437985) build with `-Dpolkit_noauth_group=wheel`
     ([Arch PKGBUILD][archnm], [Fedora spec][fednm]), which installs NM's
     `org.freedesktop.NetworkManager.rules.in` for that group
     ([data/meson.build][nmmeson]). Otherwise the user sees a polkit agent
     prompt (`auth_admin_keep`), which is acceptable for a desktop surface.
   Go: [`Wifx/gonetworkmanager/v2`][gonm] (`Settings.AddConnection`,
   `NetworkManager.ActivateConnection`).
2. **Privileged Go helper via `setcap`**: give a separate `bnm-wg` binary
   `cap_net_admin+ep` and do what wg-quick does in-process with
   [`wgctrl`][wgctrl] (`ConfigureDevice`, netlink backend on Linux) and
   [`vishvananda/netlink`][vnl] (`LinkAdd(&Wireguard{})`, addresses, routes,
   rules). Same `nosuid`/`no_new_privs` caveat as §1; DNS still needs
   `systemd-resolved`'s `set-dns-servers` (`auth_admin_keep`,
   [resolve1 policy][resolvepol]) or a resolvconf hook.
3. **`pkexec`**: runs a program as root after polkit authorisation; the
   default action `org.freedesktop.policykit.exec` is `auth_admin` for all
   three contexts ([policy][pkpolicy]); a dedicated action with the
   `org.freedesktop.policykit.exec.path` annotation ([pkexec(1)][pkexec]) plus
   a rules-file (`polkit.addRule`, `subject.isInGroup`, `polkit.Result.YES`;
   `/etc/polkit-1/rules.d` wins over `/usr/share/...` on ties
   ([polkit(8)][polkit8])) can make it prompt-free for a group. Simplest wrapper
   for `wg-quick` itself, but a per-invocation root process and no
   `PostUp`-free security story; keep as a fallback for non-NM setups.

## Sources

[ping.c]: https://github.com/torvalds/linux/blob/master/net/ipv4/ping.c
[ipsysctl]: https://www.kernel.org/doc/html/latest/networking/ip-sysctl.html
[icmp7]: https://man7.org/linux/man-pages/man7/icmp.7.html
[raw7]: https://man7.org/linux/man-pages/man7/raw.7.html
[packet7]: https://man7.org/linux/man-pages/man7/packet.7.html
[caps7]: https://man7.org/linux/man-pages/man7/capabilities.7.html
[execve2]: https://man7.org/linux/man-pages/man2/execve.2.html
[tcp7]: https://man7.org/linux/man-pages/man7/tcp.7.html
[rtnl7]: https://man7.org/linux/man-pages/man7/rtnetlink.7.html
[rtnlc]: https://github.com/torvalds/linux/blob/master/net/core/rtnetlink.c
[arp7]: https://man7.org/linux/man-pages/man7/arp.7.html
[procnet]: https://man7.org/linux/man-pages/man5/proc_net.5.html
[procfd]: https://man7.org/linux/man-pages/man5/proc_pid_fd.5.html
[proc5]: https://man7.org/linux/man-pages/man5/proc.5.html
[sockdiag7]: https://man7.org/linux/man-pages/man7/sock_diag.7.html
[inetdiag]: https://github.com/torvalds/linux/blob/master/net/ipv4/inet_diag.c
[sockdiagc]: https://github.com/torvalds/linux/blob/master/net/core/sock_diag.c
[wggenl]: https://github.com/torvalds/linux/blob/master/drivers/net/wireguard/generated/netlink.c
[sd50]: https://github.com/systemd/systemd/blob/main/sysctl.d/50-default.conf
[nixnet]: https://github.com/NixOS/nixpkgs/blob/master/nixos/modules/tasks/network-interfaces.nix
[archfiles]: https://archlinux.org/packages/core/x86_64/systemd/files/
[fedfiles]: https://packages.fedoraproject.org/pkgs/systemd/systemd-udev/fedora-rawhide.html
[debnotinst]: https://sources.debian.org/src/systemd/latest/debian/not-installed/
[debfiles]: https://packages.debian.org/bookworm/amd64/systemd/filelist
[ubufiles]: https://packages.ubuntu.com/noble/amd64/systemd/filelist
[debprocps]: https://packages.debian.org/bookworm/amd64/procps/filelist
[ubuprocps]: https://packages.ubuntu.com/noble/amd64/procps/filelist
[xicmp]: https://pkg.go.dev/golang.org/x/net/icmp#ListenPacket
[probing]: https://pkg.go.dev/github.com/prometheus-community/pro-bing
[xsys]: https://pkg.go.dev/golang.org/x/sys/unix#GetsockoptTCPInfo
[vnl]: https://pkg.go.dev/github.com/vishvananda/netlink
[rfc6762]: https://www.rfc-editor.org/rfc/rfc6762.html
[rfc6763]: https://www.rfc-editor.org/rfc/rfc6763.html
[avahi]: https://github.com/avahi/avahi/blob/master/avahi-daemon/org.freedesktop.Avahi.Server.xml
[zeroconf]: https://pkg.go.dev/github.com/grandcat/zeroconf
[goavahi]: https://pkg.go.dev/github.com/holoplot/go-avahi
[upnp]: https://openconnectivity.org/upnp-specs/UPnP-arch-DeviceArchitecture-v2.0-20200417.pdf
[iana]: https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml?search=ssdp
[gossdp]: https://pkg.go.dev/github.com/koron/go-ssdp
[nmapct]: https://nmap.org/book/scan-methods-connect-scan.html
[wgq]: https://github.com/WireGuard/wireguard-tools/blob/master/src/wg-quick/linux.bash
[nmwg]: https://networkmanager.dev/docs/api/latest/settings-wireguard.html
[nmconn]: https://networkmanager.dev/docs/api/latest/settings-connection.html
[thaller]: https://blogs.gnome.org/thaller/2019/03/15/wireguard-in-networkmanager/
[nmpol]: https://github.com/NetworkManager/NetworkManager/blob/main/data/org.freedesktop.NetworkManager.policy.in
[nmsettings]: https://github.com/NetworkManager/NetworkManager/blob/main/src/core/settings/nm-settings.c
[nmmeson]: https://github.com/NetworkManager/NetworkManager/blob/main/data/meson.build
[nixnm]: https://github.com/NixOS/nixpkgs/blob/master/nixos/modules/services/networking/networkmanager.nix
[debnmrules]: https://sources.debian.org/src/network-manager/latest/debian/org.freedesktop.NetworkManager.rules/
[debnmbuild]: https://sources.debian.org/src/network-manager/latest/debian/rules/
[ubunm]: https://packages.ubuntu.com/noble/amd64/network-manager/filelist
[archnm]: https://gitlab.archlinux.org/archlinux/packaging/packages/networkmanager/-/blob/main/PKGBUILD
[fednm]: https://src.fedoraproject.org/rpms/NetworkManager/blob/rawhide/f/NetworkManager.spec
[gonm]: https://pkg.go.dev/github.com/Wifx/gonetworkmanager/v2
[wgctrl]: https://pkg.go.dev/golang.zx2c4.com/wireguard/wgctrl
[resolvepol]: https://github.com/systemd/systemd/blob/main/src/resolve/org.freedesktop.resolve1.policy
[pkpolicy]: https://github.com/polkit-org/polkit/blob/main/actions/org.freedesktop.policykit.policy.in
[pkexec]: https://github.com/polkit-org/polkit/blob/main/docs/man/pkexec.xml
[polkit8]: https://github.com/polkit-org/polkit/blob/main/docs/man/polkit.xml
