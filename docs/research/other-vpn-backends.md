# VPN backends beyond Tailscale, WireGuard and OpenVPN

Ticket: [#22](https://github.com/dopeCape/better-nm/issues/22). Researched 2026-09-07 against project repos, official docs and NetworkManager source. Feeds the VPN-model grilling ([#13](https://github.com/dopeCape/better-nm/issues/13)) and the v1 scoping ticket ([#24](https://github.com/dopeCape/better-nm/issues/24)).

## The two adapter shapes we already have

`bnm` will ship exactly two kinds of VPN adapter, and every candidate below is judged by which one it fits:

- **NM-VPN adapter** (what OpenVPN uses). A `vpn`-type NM connection whose `vpn.service-type` names a plugin D-Bus service [1]. NetworkManager (root) reads the plugin's `.name` file from `/usr/lib/NetworkManager/VPN`, spawns `program=` itself [2], and the plugin's D-Bus policy lets only `root` own its bus name [3]. The unprivileged user only needs polkit `network-control` (default `allow_active=yes`) to activate and `settings.modify.own` to add a profile [4]. Secrets come through NM's secret-agent path; import is `nmcli connection import type <t> file <f>` (VPN-only, needs the plugin's `[libnm] plugin=` .so) [5]. **Consequence: every NM VPN plugin rides the same code path as OpenVPN; the per-backend cost is only the service-type string, the secret names, and an import-format check.**
- **Local-socket adapter** (what Tailscale uses): a root daemon exposing an unprivileged Unix socket / loopback API with a Go-consumable schema [6].

Anything that offers neither (CLI-only, text-scraped, or no runtime API) is a third shape we do not have and should not build for v1.

## Table 1: NetworkManager VPN plugins

| Backend | Control surface | Root? | Import format | Install footprint | Maintenance | Fits existing adapter? |
|---|---|---|---|---|---|---|
| OpenConnect (AnyConnect, Pulse, GlobalProtect, F5, Fortinet, Array) | NM plugin `org.freedesktop.NetworkManager.openconnect` [7] | No (NM spawns as root) | Yes, but the plugin's own `[openconnect]` keyfile, not a native file [8] | `openconnect` + libopenconnect; auth dialog pulls WebKitGTK for SAML [9] | 1.2.10 (2023-05); commits to 2026-08 are translations [10] | **NM-VPN, with one gap**: no `supports-external-ui-mode`, so nmcli cannot drive its auth dialog; secrets `cookie/gateway/gwcert` must be produced by `openconnect --authenticate` or the GTK dialog [11] |
| strongSwan IKEv2 (charon-nm) | NM plugin `…NetworkManager.strongswan`; lives in strongSwan `src/charon-nm` + `src/frontends/gnome` [12] | No | **None** (capabilities = IPV6 only) [13] | strongSwan built `--enable-nm` + frontend package (Debian `network-manager-strongswan`, Fedora `strongswan-charon-nm`) [14] | NM-strongswan 1.6.5 (2026-04); strongSwan 6.0.7 (2026-06) [12] | **NM-VPN, yes**: eap/cert/psk/agent methods, `password` secret, external-UI auth dialog [15] |
| Libreswan IKEv1/XAUTH (Cisco IPsec) | NM plugin `…NetworkManager.libreswan` (alias `.openswan`) [16] | No | Yes, an `ipsec.conf` `conn` file [17] | libreswan; plugin spawns its own pluto [18] | 1.2.30 (2025-12), three releases late 2025 [19] | **NM-VPN, yes**: `pskvalue`/`xauthpassword`, interactive secrets, external-UI [20] |
| L2TP/IPsec | NM plugin `…NetworkManager.l2tp` [21] | No | Yes, plugin's own GKeyFile (`[connection]`/`[vpn]`/`[ipv4]`) [22] | `kl2tpd` or `xl2tpd`, `pppd`, plus Libreswan or strongSwan for IPsec; needs NM >= 1.52.2 [23] | 1.52.4 (2026-07) [24] | **NM-VPN, yes** |
| PPTP | NM plugin `…NetworkManager.pptp` [25] | No | No (stub returns "not implemented") [26] | `pppd` + `pptp` [27] | 1.2.12 (2023-03); only translation commits since [28] | NM-VPN, yes, but the protocol is obsolete |
| SSTP | NM plugin `…NetworkManager.sstp` (moved to GNOME GitLab) [29] | No | No; advertises IMPORT|EXPORT but both return "not implemented" so `nmcli import` fails [30] | `sstpc` + `pppd` [31] | 1.3.2 (2023) [29] | NM-VPN, yes |
| Fortinet SSL (openfortivpn) | NM plugin `…NetworkManager.fortisslvpn` [32] | No | None [33] | `openfortivpn` >= 1.10 + `pppd` [34] | **EOL**: README says use OpenConnect instead [35] | Skip; OpenConnect covers it |
| Cisco vpnc | NM plugin `…NetworkManager.vpnc` [36] | No | Yes, Cisco `.pcf` [37] | `vpnc` | **EOL** (README, 2026-08) [38] | Skip |

## Table 2: Mesh / overlay networks

| Backend | Control surface | Root? | Import format | Install footprint | Maintenance | Fits existing adapter? |
|---|---|---|---|---|---|---|
| Headscale-backed Tailscale | Unchanged Tailscale LocalAPI (`/var/run/tailscale/tailscaled.sock`); client is the stock Tailscale binary with `--login-server` [39] | Same as Tailscale (`tailscale set --operator=<user>`) [40] | Pre-auth key + login-server URL [41] | Nothing extra | headscale 0.29.3 (2026-07); client 1.102.3 [42] | **Tailscale adapter, zero new code**: set `ControlURL` in prefs. Funnel/Serve/flow logs unsupported by headscale [43] |
| NetBird | gRPC `DaemonService` on `unix:///var/run/netbird.sock`, proto in repo with `go_package`; socket chmod 0666 with peer-cred gating [44][45] | Daemon root (systemd); `up/down/status` unprivileged; management-URL change and SSH ops root-gated [45] | Setup key + management URL (`netbird up --setup-key … --management-url …`) [46] | Single Go binary; apt/yum repos; community Arch/Nix; iface `wt0` [47] | 0.78.1 (2026-09-04), BSD-3 client [48] | **Local-socket adapter, yes**; closest analogue to Tailscale. Importing the Go package drags the whole netbird module; vendor `daemon.proto` instead |
| ZeroTier | Local JSON/HTTP on `127.0.0.1:9993`, header `X-ZT1-Auth` from `authtoken.secret`; `zerotier-cli -j` [49] | Daemon root; token file is owner-only, but `zerotier-cli` also reads `~/.zeroTierOneAuthToken` (source only, undocumented) [50] | 16-hex network ID only [51] | C++ `zerotier-one`; apt/yum via install script; iface `zt<base32>` [52] | 1.16.2 (2026-05); agent now MPL-2.0, controller source-available [53] | Local-socket adapter, **conditionally**: needs a one-time token copy; small hand-rolled HTTP client (no Go lib) |
| Nebula | **No runtime API**: YAML config, SIGHUP reload, optional SSH debug console on `127.0.0.1:2222`, Prometheus stats [54] | Needs `CAP_NET_ADMIN` (ambient cap or setcap); one process = one overlay [55] | YAML config + `ca.crt`/`host.crt`/`host.key` [56] | Single Go binary; distro packages [57] | 1.11.1 (2026-08), MIT [58] | **No**: would be "manage a systemd unit", a third adapter shape. Go library exists but embedding needs CAP_NET_ADMIN in `bnm` [59] |

## Table 3: Commercial clients

| Backend | Control surface | Root? | Import format | Install footprint | Maintenance | Fits existing adapter? |
|---|---|---|---|---|---|---|
| Mullvad | gRPC `ManagementService` on `/var/run/mullvad-vpn`; `EventsListen` stream; `mullvad status --json` [60][61] | Daemon root; socket mode 0766 by default (0760 + group if `MULLVAD_MANAGEMENT_SOCKET_GROUP`) [62] | None needed (account login); WireGuard configs downloadable for the NM adapter; OpenVPN ends 2026-01-15 [63] | Official deb/rpm, Arch official repo, nixpkgs; GPLv3 [64] | 2026.4 (2026-08) [65] | Local-socket adapter, yes; proto lacks `go_package`, compile with `--go_opt=M` [60]. Already covered by NM-WireGuard via config export |
| Proton VPN | **No daemon API**. GTK app and new `protonvpn` CLI drive NetworkManager; ships its own NM plugin `org.freedesktop.NetworkManager.protun` [66] | No (goes through NM) | WireGuard `.conf` and `.ovpn` downloads, documented with `nmcli connection import` [67] | Official repos, snap; GPLv3 [68] | GTK app 4.18.1 (2026-08); CLI 1.0.x [69] | **Already covered** by NM-WireGuard/OpenVPN; `protun` connections appear via the NM-VPN adapter for free |
| Cloudflare WARP | `warp-svc` + `warp-cli`; IPC exists but protocol and socket undocumented; no JSON flag documented [70] | `warp-cli` runs as the normal user [71] | None (registration/team enrolment); no WireGuard export [72] | Proprietary deb/rpm; nixpkgs unfree wrapper [73] | 2026.7.1377.0 (2026-08) [74] | **No**: CLI-scraping only |
| NordVPN | gRPC `Daemon` on `/run/nordvpn/nordvpnd.sock`; protos with `go_package github.com/NordSecurity/nordvpn-linux/daemon/pb`; `nordvpn status` text only [75] | Daemon root; socket 0660, user must be in group `nordvpn` [76] | `.ovpn` archive for NM; no WireGuard/NordLynx export [77] | Official deb/rpm/snap, nixpkgs; GPLv3 [78] | 5.3.0 (2026-08) [79] | Local-socket adapter, yes (GPLv3 package import) |
| ExpressVPN | `expressvpnctl`; needs GUI running or `background enable`; IPC undocumented [80] | Unverified | `.ovpn` for manual setup [81] | Proprietary `.run` installer [80] | 14.2.1 (2026-08) [82] | **No** |

## Notes

- **The NM-plugin family is nearly free.** The adapter already needed for OpenVPN (activate/deactivate an NM `vpn` connection, watch `VpnState`, answer secret requests) applies unchanged; only openconnect's auth flow is special because its `.name` file omits `supports-external-ui-mode` [7][11]. Discovery is trivial: list `.name` files or filter NM connections by `vpn.service-type`, and show whatever plugin the distro installed. The unverified bits are the D-Bus `.conf` policies for pptp/fortisslvpn/vpnc (inferred from siblings).
- **OpenConnect is the one enterprise backend worth design time.** It subsumes AnyConnect, Pulse, GlobalProtect, F5, Fortinet and Array via libopenconnect's protocol list [83], and the fortisslvpn plugin's own README points users at it [35]. SAML/SSO logins need a browser (the plugin's dialog embeds WebKit). For headless surfaces (CLI/TUI) `bnm` would run `openconnect --authenticate` and hand NM the resulting `cookie/gateway/gwcert` secrets [11].
- **Import formats are inconsistent.** Only libreswan (`ipsec.conf`) and vpnc (`.pcf`) import a native file; openconnect and l2tp import their own keyfiles; strongswan, pptp, fortisslvpn have none, and sstp lies about it [8][13][17][22][26][30][33][37]. For the VPN-model "how is a VPN added" question this means: the generic answer is "import via `nmcli`-equivalent libnm call when the plugin says it can, otherwise create from fields".
- **Headscale is not a backend.** It is a different control server for the same client; `bnm` only needs to display the control URL and accept a login-server + auth-key pair [39][41].
- **NetBird is the natural fourth adapter.** Its shape (root daemon, unprivileged socket, published proto, `SubscribeStatus` stream, setup key as the "import") mirrors Tailscale almost one-to-one [44][45]. Two caveats from source: changing the management URL from an unprivileged client is refused, and the Go proto package lives inside the main module.
- **ZeroTier works but is awkward:** the only unprivileged path is `~/.zeroTierOneAuthToken`, which the source honours but the docs never mention (they say "try again as root") [50]. Fine as a documented one-time setup step; not zero-design.
- **Nebula has no control plane at all** by design; anything `bnm` did would be systemd + file management, which the "no root code path" preference rules out [54][55].
- **Commercial clients split cleanly.** Mullvad and Proton are already reachable through the NM WireGuard/OpenVPN adapters via their config downloads [63][67], so a native adapter buys only live state and relay picking. NordVPN and Mullvad have clean gRPC sockets if someone wants them; WARP and ExpressVPN have no usable API.
- **NM sees overlay interfaces as unmanaged.** Tailscale's own DNS code documents NM ignoring DNS on unmanaged interfaces [84]; `wt0`, `zt*`, `nebula*` behaviour is unverified but presumably the same. This matters for the device list labelling question in #24.

## Ranked effort-to-value for a v1 follow-on

1. **Generic NM-VPN adapter covering any installed plugin** (strongswan, libreswan, l2tp, sstp, pptp). Effort: near zero beyond OpenVPN; a service-type-to-display-name map and per-plugin secret names. Value: every corporate IPsec/L2TP user for free.
2. **Headscale**: zero code; expose `ControlURL` in the Tailscale adapter's status and add a login-server field to "add". Value: self-hosters.
3. **OpenConnect**: same adapter plus a `--authenticate` helper and a "needs browser" state for SAML. Effort: small-medium. Value: largest enterprise population (Cisco/Palo Alto/Pulse/Fortinet).
4. **NetBird**: new local-socket adapter mirroring Tailscale; vendor `daemon.proto`. Effort: medium. Value: growing open-source mesh, second most requested overlay.
5. **Mullvad / NordVPN native adapters**: gRPC over root-owned sockets; value mostly live state, since configs already import. Effort: medium each. Post-v1.
6. **ZeroTier**: small HTTP client + token setup step. Effort: small, but setup friction. Post-v1.
7. **Proton native**: nothing to build; document the `.conf` import path and let `protun` connections show through the NM-VPN adapter.
8. **Cloudflare WARP, ExpressVPN, Nebula, vpnc, fortisslvpn**: no API, EOL, or wrong shape. Out.

**Near-zero extra design (flag for #24):** items 1, 2 and 7. Item 3 needs one auth-flow decision; item 4 needs one new adapter but no new abstraction.

## Sources

1. https://networkmanager.dev/docs/api/latest/settings-vpn.html
2. https://raw.githubusercontent.com/NetworkManager/NetworkManager/main/src/core/vpn/nm-vpn-connection.c (`nm_vpn_service_daemon_exec`); `.name` parsing in https://raw.githubusercontent.com/NetworkManager/NetworkManager/main/src/libnm-core-impl/nm-vpn-plugin-info.c
3. e.g. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan/-/raw/main/nm-libreswan-service.conf, https://gitlab.gnome.org/GNOME/NetworkManager-openconnect/-/raw/main/nm-openconnect-service.conf
4. https://raw.githubusercontent.com/NetworkManager/NetworkManager/main/data/org.freedesktop.NetworkManager.policy.in; https://raw.githubusercontent.com/NetworkManager/NetworkManager/main/src/core/nm-manager.c
5. https://networkmanager.dev/docs/api/latest/nmcli.html (connection import/export); https://networkmanager.dev/docs/libnm/latest/NMVpnEditorPlugin.html
6. https://pkg.go.dev/tailscale.com/client/local
7. https://gitlab.gnome.org/GNOME/NetworkManager-openconnect/-/raw/main/nm-openconnect-service.name.in
8. https://gitlab.gnome.org/GNOME/NetworkManager-openconnect/-/raw/main/properties/nm-openconnect-editor-plugin.c
9. https://gitlab.gnome.org/GNOME/NetworkManager-openconnect/-/raw/main/configure.ac
10. https://gitlab.gnome.org/GNOME/NetworkManager-openconnect (tags/commits, 2026-09-07)
11. https://gitlab.gnome.org/GNOME/NetworkManager-openconnect/-/raw/main/auth-dialog/README; https://raw.githubusercontent.com/NetworkManager/NetworkManager/main/src/libnmc-base/nm-secret-agent-simple.c (`try_spawn_vpn_auth_helper`); https://www.infradead.org/openconnect/manual.html (`--authenticate`)
12. https://github.com/strongswan/strongswan (src/charon-nm, src/frontends/gnome); https://download.strongswan.org/NetworkManager/
13. https://raw.githubusercontent.com/strongswan/strongswan/master/src/frontends/gnome/properties/nm-strongswan-plugin.c
14. https://docs.strongswan.org/docs/latest/features/networkManager.html
15. https://raw.githubusercontent.com/strongswan/strongswan/master/src/frontends/gnome/nm-strongswan-service.name.in; https://raw.githubusercontent.com/strongswan/strongswan/master/src/charon-nm/nm/nm_service.c
16. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan/-/raw/main/nm-libreswan-service.name.in
17. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan/-/raw/main/properties/nm-libreswan-editor-plugin.c
18. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan/-/raw/main/src/nm-libreswan-service.c
19. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan (tags)
20. https://gitlab.gnome.org/GNOME/NetworkManager-libreswan/-/raw/main/src/nm-libreswan-service.c (`connect_interactive`, `secrets_required`)
21. https://raw.githubusercontent.com/nm-l2tp/NetworkManager-l2tp/master/nm-l2tp-service.name.in
22. https://raw.githubusercontent.com/nm-l2tp/NetworkManager-l2tp/main/properties/import-export.c
23. https://raw.githubusercontent.com/nm-l2tp/NetworkManager-l2tp/master/README.md
24. https://github.com/nm-l2tp/NetworkManager-l2tp/releases
25. https://gitlab.gnome.org/GNOME/NetworkManager-pptp/-/raw/main/nm-pptp-service.name.in
26. https://gitlab.gnome.org/GNOME/NetworkManager-pptp/-/raw/main/properties/nm-pptp-editor-plugin.c
27. https://gitlab.gnome.org/GNOME/NetworkManager-pptp/-/raw/main/src/nm-pptp-service.c
28. https://gitlab.gnome.org/GNOME/NetworkManager-pptp (tags/commits)
29. https://gitlab.gnome.org/GNOME/network-manager-sstp; https://github.com/enaess/network-manager-sstp (moved notice)
30. https://gitlab.gnome.org/GNOME/network-manager-sstp/-/raw/master/properties/nm-sstp-editor-plugin.c
31. https://gitlab.gnome.org/GNOME/network-manager-sstp/-/raw/master/src/nm-sstp-service.c
32. https://gitlab.gnome.org/GNOME/NetworkManager-fortisslvpn/-/raw/main/nm-fortisslvpn-service.name.in
33. https://gitlab.gnome.org/GNOME/NetworkManager-fortisslvpn/-/raw/main/properties/nm-fortisslvpn-editor-plugin.c
34. https://gitlab.gnome.org/GNOME/NetworkManager-fortisslvpn/-/raw/main/src/nm-fortisslvpn-service.c
35. https://gitlab.gnome.org/GNOME/NetworkManager-fortisslvpn/-/raw/main/README
36. https://gitlab.gnome.org/GNOME/NetworkManager-vpnc/-/raw/main/nm-vpnc-service.name.in
37. https://gitlab.gnome.org/GNOME/NetworkManager-vpnc/-/raw/main/properties/nm-vpnc-editor-plugin.c
38. https://gitlab.gnome.org/GNOME/NetworkManager-vpnc/-/raw/main/README
39. https://headscale.net/stable/usage/getting-started/; https://headscale.net/stable/about/clients/; https://raw.githubusercontent.com/tailscale/tailscale/main/paths/paths.go
40. https://tailscale.com/kb/1080/cli
41. https://headscale.net/stable/usage/getting-started/ (`headscale preauthkeys create`)
42. https://github.com/juanfont/headscale/releases/tag/v0.29.3; https://github.com/tailscale/tailscale/releases/tag/v1.102.3
43. https://raw.githubusercontent.com/juanfont/headscale/main/docs/about/features.md
44. https://docs.netbird.io/get-started/cli; https://raw.githubusercontent.com/netbirdio/netbird/main/client/proto/daemon.proto
45. https://raw.githubusercontent.com/netbirdio/netbird/main/client/cmd/service_socket.go; https://raw.githubusercontent.com/netbirdio/netbird/main/client/cmd/service_controller.go
46. https://docs.netbird.io/how-to/register-machines-using-setup-keys
47. https://docs.netbird.io/get-started/install/linux; https://docs.netbird.io/help/troubleshooting-client
48. https://github.com/netbirdio/netbird/releases/tag/v0.78.1
49. https://raw.githubusercontent.com/zerotier/ZeroTierOne/dev/service/README.md; https://docs.zerotier.com/cli/
50. https://raw.githubusercontent.com/zerotier/ZeroTierOne/dev/one.cpp (token lookup order); https://docs.zerotier.com/tokens/
51. https://docs.zerotier.com/start/
52. https://docs.zerotier.com/; https://raw.githubusercontent.com/zerotier/ZeroTierOne/dev/osdep/LinuxEthernetTap.cpp
53. https://github.com/zerotier/ZeroTierOne/releases/tag/1.16.2; https://raw.githubusercontent.com/zerotier/ZeroTierOne/dev/LICENSE.txt
54. https://nebula.defined.net/docs/config/sshd/; https://nebula.defined.net/docs/guides/debug-ssh-commands/; https://nebula.defined.net/docs/config/stats/
55. https://nebula.defined.net/docs/guides/running-nebula-as-non-root/
56. https://nebula.defined.net/docs/guides/quick-start/
57. https://raw.githubusercontent.com/slackhq/nebula/master/README.md
58. https://github.com/slackhq/nebula/releases/tag/v1.11.1
59. https://pkg.go.dev/github.com/slackhq/nebula
60. https://raw.githubusercontent.com/mullvad/mullvadvpn-app/main/mullvad-management-interface/proto/management_interface.proto
61. https://raw.githubusercontent.com/mullvad/mullvadvpn-app/main/mullvad-cli/src/cmds/status.rs; https://mullvad.net/en/help/how-use-mullvad-cli
62. https://raw.githubusercontent.com/mullvad/mullvadvpn-app/main/mullvad-management-interface/src/lib.rs; https://raw.githubusercontent.com/mullvad/mullvadvpn-app/main/mullvad-paths/src/rpc_socket.rs
63. https://mullvad.net/en/help/easy-wireguard-mullvad-setup-linux; https://mullvad.net/en/help/linux-openvpn-installation
64. https://mullvad.net/en/help/install-mullvad-app-linux; https://github.com/mullvad/mullvadvpn-app
65. https://github.com/mullvad/mullvadvpn-app/releases/latest
66. https://github.com/ProtonVPN/python-proton-vpn-api-core; https://raw.githubusercontent.com/ProtonVPN/python-proton-vpn-api-core/master/resources/nm-protun.name; https://github.com/ProtonVPN/proton-vpn-cli
67. https://protonvpn.com/support/wireguard-linux; https://protonvpn.com/support/linux-openvpn
68. https://protonvpn.com/support/linux-vpn-setup; https://github.com/ProtonVPN/proton-vpn-gtk-app
69. https://api.github.com/repos/ProtonVPN/proton-vpn-gtk-app/tags; https://protonvpn.com/support/release-notes-linux-cli
70. https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/; https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/troubleshooting/diagnostic-logs/
71. https://developers.cloudflare.com/cloudflare-one/connections/connect-devices/warp/deployment/manual-deployment/
72. https://developers.cloudflare.com/cloudflare-one/connections/connect-devices/warp/configure-warp/warp-settings/
73. https://pkg.cloudflareclient.com/; https://raw.githubusercontent.com/NixOS/nixpkgs/master/pkgs/by-name/cl/cloudflare-warp/package.nix
74. https://developers.cloudflare.com/cloudflare-one/connections/connect-devices/warp/download-warp/
75. https://raw.githubusercontent.com/NordSecurity/nordvpn-linux/main/internal/constants.go; https://github.com/NordSecurity/nordvpn-linux/blob/main/protobuf/daemon/service.proto; https://raw.githubusercontent.com/NordSecurity/nordvpn-linux/main/cli/cli_status.go
76. https://raw.githubusercontent.com/NordSecurity/nordvpn-linux/main/cmd/daemon/main.go; https://support.nordvpn.com/hc/en-us/articles/20196094470929-Installing-NordVPN-on-Linux-distributions
77. https://support.nordvpn.com/hc/en-us/articles/20347784574097-Connecting-to-NordVPN-Linux-Network-Manager
78. https://github.com/NordSecurity/nordvpn-linux; https://raw.githubusercontent.com/NixOS/nixpkgs/master/pkgs/by-name/no/nordvpn/package.nix
79. https://api.github.com/repos/NordSecurity/nordvpn-linux/releases/latest
80. https://www.expressvpn.com/support/vpn-setup/app-for-linux-cli/; https://www.expressvpn.com/blog/qt-linux-macos-apps/
81. https://www.expressvpn.com/support/vpn-setup/manual-config-for-linux-with-openvpn/
82. https://www.expressvpn.com/support/vpn-setup/release-notes/linux-app/
83. https://gitlab.com/openconnect/openconnect/-/raw/master/library.c; https://www.infradead.org/openconnect/protocols.html
84. https://raw.githubusercontent.com/tailscale/tailscale/main/net/dns/manager_linux.go
