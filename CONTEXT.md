# bnm glossary

The words bnm uses, and what they mean. Code in `internal/core` implements these.

- **Device**: a network interface the host has. *Physical* (Wi-Fi card, ethernet port) or *Infra* (bridge, veth, vlan, tun created by Docker, Podman, libvirt, Incus, systemd-nspawn, Kubernetes or a VPN). NetworkManager lists both; bnm groups Infra devices under "Virtual" with an owner tag.
- **Wi-Fi Network**: an SSID as seen by a Wi-Fi Device, aggregated over its access points. *Known* when a Profile exists for it.
- **Profile**: a saved NetworkManager connection: settings for joining a Network (Wi-Fi, wired, WireGuard, plugin VPN). The user-facing word is "profile"; NM's own word "connection" is not used in the UI.
- **Active Connection**: a Profile currently activated on one or more Devices. The **primary** one carries the default route.
- **VPN**: one thing the user toggles, whatever the backend. Backends: Tailscale (via its local API), WireGuard (an NM-native profile), NM-VPN (any NetworkManager plugin such as OpenVPN, keyed on its service type). Every VPN has the same states: disconnected, connecting, connected, error, needs-setup, needs-auth, unavailable.
- **Network Key**: the identity monitoring history is filed under: `wifi:<ssid>` for Wi-Fi, `<type>:<profile-uuid>` otherwise. The same SSID at two locations shares one key; this is a known trade-off.
- **Anchor**: a probe target. The default gateway plus configurable public hosts.
- **Sample**: one probe round against one Anchor on one Network Key: round-trip time, loss, and DNS resolve time.
- **Baseline**: rolling statistics of Samples for a (Network Key, Anchor). States: learning (too few samples), ok, degraded, idle.
- **Degradation**: the Baseline engine's verdict that the network is meaningfully slower than its own history. Not an absolute speed judgement.
- **Speed test**: an on-demand bandwidth measurement. Never runs in the background.
- **Event**: a fact the daemon emits (connected, disconnected, no-internet, internet-restored, vpn-up, vpn-down, degraded, recovered). Surfaces render Events; the Notifier delivers the notable ones as desktop notifications.
- **Infra Network**: a bridge and its member links, attributed to a runtime. Inspect-only.
- **Daemon** (`bnmd`): the one user-session process that owns the NM subscription, VPN adapters, probes, history and the event stream. The CLI, TUI and desktop app are its clients.
