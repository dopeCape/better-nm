# Tailscale control surface from Go

Research for [#4](https://github.com/dopeCape/better-nm/issues/4). Sources are the
`tailscale/tailscale` repo pinned at **v1.98.10** (the version installed on the dev
machine), tailscale.com docs, pkg.go.dev, the nixpkgs Tailscale module, and probes run
on the dev machine (NixOS, `tailscaled` 1.98.10, Go 1.25.3, user uid 1000, no
passwordless sudo). `src:` links point at the pinned source lines.

## TL;DR

Use the Go LocalAPI client `tailscale.com/client/local`, not a shell-out. Reasons:

1. **Same wire format either way.** `tailscale status --json` is literally
   `json.MarshalIndent(ipnstate.Status)` with the flag documented as
   "WARNING: format subject to change" ([src: status.go#L51,L88-98][status]). Shelling
   out gives no stability that the Go client lacks, and we would still need Go structs
   to decode it.
2. **Only the Go client gives an event stream.** `WatchIPNBus` streams `ipn.Notify`
   (state, prefs, netmap, login URL, health) ([src: local.go#L1320][watch]); the CLI has
   no watch mode, so a daemon would have to poll.
3. **Permissions are identical.** tailscaled authorises by the peer uid on the Unix
   socket, regardless of whether the CLI or our binary opened it
   ([src: ipnauth.go#L166-208][ro]). Both paths need operator mode for writes.
4. **Cost is bounded**: measured +6.6 MB static binary, 19 modules, no cgo (section 6).

Exception: `netcheck` is not a LocalAPI endpoint (section 4); if bnm ever needs it, run
`tailscale netcheck --format=json` rather than importing `net/netcheck`.

## 1. Two client packages, one is deprecated

- `tailscale.com/client/local` — "Package local contains a Go client for the Tailscale
  LocalAPI" ([src: local.go#L4][localdoc]). "Its API is not necessarily stable and
  subject to changes between releases. Some API calls have stricter compatibility
  guarantees ... See method docs" ([src: local.go#L54-58][localdoc]). pkg.go.dev marks
  `Status`, `GetPrefs`, `EditPrefs`, `CheckUpdate`, `CurrentDERPMap`, `BugReport`,
  `CertPair`, `DialTCP` as "stable API" ([pkg.go.dev client/local][pkglocal]).
- `tailscale.com/client/tailscale` — "This package is only intended for internal and
  transitional use. Deprecated" and requires `I_Acknowledge_This_API_Is_Unstable = true`
  ([src: tailscale.go#L6-25][clienttailscale]). It only re-exports `local.Client` as
  `LocalClient` ([pkg.go.dev client/tailscale][pkgtailscale]). Do not import it.

## 2. Transport: socket, permissions, identity

- Default socket on Linux: `/var/run/tailscale/tailscaled.sock`
  ([src: paths.go#L47][paths]); `local.Client.Socket` overrides it, and the zero
  `Client` is valid ([src: local.go#L60,L75-77,L99-102][localdoc]). On the dev machine
  `/run/tailscale/tailscaled.sock` is `srw-rw-rw- root root` (observed).
- The socket is deliberately mode 0666 on platforms that use peer credentials
  ([src: unixsocket.go#L83-91][sockperm]); authorisation is done in-daemon from
  `SO_PEERCRED`, not from file mode.
- Set `UseSocketOnly: true`: the default dialer first tries a macOS-GUI TCP fallback via
  `safesocket.LocalTCPPortAndToken()` before the socket ([src: local.go#L112-124][dial]).
- Every LocalAPI handler is gated by `PermitRead` / `PermitWrite`; "If PermitWrite is
  true, everything is allowed" ([src: localapi.go#L210-217][permit]).
- Observed as uid 1000 without operator: `GET /localapi/v0/prefs` -> 200,
  `PATCH /localapi/v0/prefs` -> 403 `prefs write access denied`;
  `tailscale set ...` -> `Access denied: checkprefs access denied` +
  `Use 'sudo tailscale set ...'`. Reads work unprivileged; writes do not.

## 3. Operator mode

- `Prefs.OperatorUser` is "the local machine user name who is allowed to operate
  tailscaled without being root or using sudo" ([src: prefs.go#L246-248][prefs]). It is
  one username string, persisted in tailscaled's prefs, resolved to a uid at check time
  ([src: local.go(ipnlocal)#L6797-6818][opuid]).
- Set with `sudo tailscale set --operator=USER` ("Unix username to allow to operate on
  tailscaled without sudo", [src: set.go#L111][setgo]; docs: "Provide a Unix username
  other than root to operate tailscaled" [KB 1080][kb1080]). Setting it is itself a
  prefs write, so the first run needs root. On NixOS:
  `services.tailscale.extraSetFlags = [ "--operator=USER" ]` runs `tailscale set` from a
  `tailscaled-set` unit ([nixpkgs tailscale.nix#L120-125,L202-210][nix]).
- Grant logic ([src: server.go#L218,L336-354][perms]; [src: ipnauth.go#L166-208][ro]):
  Unix-socket connections always get read; write is granted when the peer uid is `0`,
  equals tailscaled's own uid, equals the operator uid, or is in a platform admin group
  (`admin` on macOS, `administrators` on QNAP; on Linux there is no such group:
  "no system admin group found" -> read-only). Windows uses a different model.
- Consequences: exactly one operator user; the operator gets *all* writes (exit node,
  advertise routes, `WantRunning=false`, logout, changing the operator). A few edits are
  additionally policy-checked: disconnect (`WantRunning=false`) goes through
  `CheckProfileAccess(Disconnect)`, and exit-node edits are refused when an MDM/policy
  manages the exit node ([src: local.go(ipnlocal)#L4555-4580][editaccess]).
- Nothing in bnm can elevate for the user; the daemon must detect 403
  (`local.IsAccessDeniedError`, [src: local.go#L161][dial]) and tell the user to run
  `sudo tailscale set --operator=$USER` once.

## 4. Operations available

Read = works unprivileged; Write = needs root/operator. LocalAPI paths are
`/localapi/v0/<name>` ([src: localapi.go#L75-93,L96-140][handlers]).

| Need | Go (`local.Client`) | CLI equivalent | Perm |
|---|---|---|---|
| Status, self node, health, auth URL | `Status` / `StatusWithoutPeers` -> `ipnstate.Status{BackendState, Self, Peer, ExitNodeStatus, Health, AuthURL, CurrentTailnet, MagicDNSSuffix, ClientVersion}` ([src: ipnstate.go#L33-86][ipnstate]) | `tailscale status --json` (same struct) [status] | Read |
| Peers | `Status().Peer` map of `PeerStatus{DNSName, TailscaleIPs, Online, Active, ExitNode, ExitNodeOption, CurAddr, Relay, Location, Expired, PrimaryRoutes}` ([src: ipnstate.go#L229-330][ipnstate]); `PeerByID` | `status --json` | Read |
| Live events | `WatchIPNBus(ctx, mask)` -> `ipn.Notify{State, Prefs, NetMap, BrowseToURL, Health, ErrMessage, ...}`; `NotifyInitialState|Prefs|NetMap|HealthState|SuggestedExitNode` flags ([src: backend.go#L71-89,L104-178][notify]) | none (poll) | Read |
| Connect ("up") | `EditPrefs(&ipn.MaskedPrefs{Prefs: {WantRunning: true}, WantRunningSet: true})`; `tailscale up` itself is a composite of `GetPrefs`/`CheckPrefs`/`EditPrefs`/`Start`/`StartLoginInteractive`/`WatchIPNBus` and refuses to run against differing prefs without `--reset` ([src: up.go#L143,L969][up]) | `tailscale up [--json]` (`--json` also "subject to change", [src: up.go#L142][up]) | Write |
| Disconnect ("down") | `EditPrefs{WantRunning:false}` (exactly what the CLI does, [src: down.go#L57-61][down]) | `tailscale down` | Write |
| Login | `StartLoginInteractive`, then take the URL from `Notify.BrowseToURL` or `Status.AuthURL`; `tailscale login` = `SwitchToEmptyProfile` + up ([src: login.go#L25-28][login]) | `tailscale login` | Write |
| Logout | `Logout` ([src: logout.go#L43][logout]) | `tailscale logout` | Write |
| Exit node list | no endpoint: filter `Status().Peer` on `ExitNodeOption` ([src: exitnode.go#L103-116][exitnode]) | `tailscale exit-node list` (text only, no `--json`) | Read |
| Exit node suggest | `SuggestExitNode` -> `suggest-exit-node` ([src: local.go#L1456][suggest]) | `tailscale exit-node suggest` | Read |
| Select exit node | `EditPrefs` with `ExitNodeID`/`ExitNodeIP` via `Prefs.SetExitNodeIP`, or `AutoExitNode`; `ExitNodeAllowLANAccess` ([src: set.go#L79-80,L181-190][setgo]) | `tailscale set --exit-node=… [--exit-node-allow-lan-access]` | Write |
| Toggle exit node on/off | `SetUseExitNode(on)` -> `set-use-exit-node-enabled` ([src: exitnode.go#L78][exitnode]) | `tailscale exit-node connect|disconnect` | Write |
| Current exit node | `Status().ExitNodeStatus{ID, Online, TailscaleIPs}` ([src: ipnstate.go#L180-189][ipnstate]) | `status --json` | Read |
| DNS status | `Status` + `GetPrefs` + `DNSConfig` + `GetDNSOSConfig` ([src: dns-status.go#L100-161][dnsstatus]) | `tailscale dns status --json` | Read |
| DNS query | `QueryDNS(name, type)` ([src: dns-query.go#L71][dnsquery]) | `tailscale dns query` | Read |
| MagicDNS accept | `EditPrefs{CorpDNS}` (= `--accept-dns`, [src: set.go#L78][setgo]) | `tailscale set --accept-dns` | Write |
| Accounts / profiles | `ProfileStatus`, `SwitchProfile` | `tailscale switch --list --json` | Read/Write |
| Ping | `Ping(ip, tailcfg.PingDisco)` | `tailscale ping` | Read |
| netcheck | **not a LocalAPI endpoint**: the CLI runs `netcheck.Client` in-process and only uses `CurrentDERPMap` from the daemon ([src: netcheck.go#L90,L120][netcheck]) | `tailscale netcheck --format=json|json-line` ([src: netcheck.go#L52][netcheck]) | Read |

Endpoints are registered under `buildfeatures.HasX` guards (`watch-ipn-bus`,
`suggest-exit-node`, `dns-query`, ...) ([src: localapi.go#L96-140][handlers]); a
tailscaled built with `ts_omit_*` tags returns 404 for them ([src: localapi.go#L273][handlers]).
The nixpkgs daemon is a full build (observed: all of the above work).

## 5. Version skew

- Every request carries `Tailscale-Cap`; every response carries `Tailscale-Version` and
  `Tailscale-Cap` ([src: local.go#L139][dial]; [src: localapi.go#L251-252][handlers]).
  Observed from the daemon: `Tailscale-Version: 1.98.10`, `Tailscale-Cap: 138`.
- The client compares the daemon's version with `envknob.IPCVersion()` =
  `version.Long()` of the *importing binary's* `tailscale.com` module and calls the
  `SetVersionMismatchHandler` hook ([src: local.go#L158-159,L237-241][dial];
  [src: envknob.go#L681-686][envknob]). The CLI prints
  `Warning: client version %q != tailscaled server version %q` on stderr
  ([src: cli.go#L117-119][cligo]) — so a shell-out is exposed to skew too, and the
  version comes from the same module. bnm should install its own handler that logs once.
- Errors: 403 -> `ErrAccessDenied`, 412 -> `PreconditionsFailedError`, unknown path ->
  404 ([src: local.go#L161-167][dial]; [src: localapi.go#L273][handlers]). Newer client
  methods against an older daemon surface as 404; decode is tolerant of unknown JSON
  fields, so older-client-newer-daemon simply drops new fields.
- The module has moved on: pkg.go.dev lists v1.102.3 (2026-08-19) [pkglocal] while the
  dev daemon is 1.98.10. Pin `tailscale.com` to the oldest daemon we support and let MVS
  bump it; tailscale.com v1.98.10 declares `go 1.26.5` ([src: go.mod#L3][gomod]), so
  importing it forces bnm's `go` directive to >= 1.26.5 (the probe auto-switched the
  1.25.3 toolchain to 1.26.8).
- NixOS ships CLI and daemon from one package, so CLI == daemon there; the Go client
  version is whatever bnm pins. Either way the JSON types are the same generation of
  `ipnstate.Status`.

## 6. Dependency weight (measured)

Probe: a `main` that calls `local.Client.Status` (`CGO_ENABLED=0 go build -trimpath
-ldflags='-s -w'`, tailscale.com v1.98.10, linux/amd64):

| Metric | Value |
|---|---|
| Static binary | 8,065,186 B vs 1,495,224 B hello-world (+6.6 MB) |
| Modules in build graph | 19 (incl. main + tailscale.com) |
| `require` lines after `go mod tidy` | 22; `go.sum` 70 lines |
| Packages compiled | 315, of which 70 under `tailscale.com` |
| `GOMODCACHE/tailscale.com@v1.98.10` | 27 MB |
| cgo | none |

Transitive modules: `golang.org/x/{crypto,net,sys,exp,sync}`, `go4.org/{mem,netipx}`,
`github.com/fxamacker/cbor/v2`, `github.com/go-json-experiment/json`,
`github.com/mdlayher/{netlink,socket}`, `github.com/jsimonetti/rtnetlink`,
`github.com/coder/websocket`, `github.com/hdevalence/ed25519consensus`,
`filippo.io/edwards25519`, `github.com/mitchellh/go-ps`, `github.com/x448/float16`.
`tailscale.com`'s own go.mod has 247 requires ([src: go.mod][gomod]) but tidy prunes
to what `client/local` reaches; wireguard-go, gvisor, ebpf and friends are not linked.
Note tailscale.com pins `github.com/godbus/dbus/v5` at a 2023 pseudo-version; bnm's own
newer godbus requirement wins under MVS. The probe binary ran as uid 1000 and printed
`Running 1.98.10`.

Shell-out cost by comparison: zero Go deps, but bnm would still hand-write or vendor
the `ipnstate.Status` shape (importing `tailscale.com/ipn/ipnstate` alone drags in
`tailcfg`, `key`, etc.), must spawn a process per poll, parse text for `exit-node list`
and error messages, and gets no event stream.

## 7. Recommendation and what bnm gets

- Import `tailscale.com/client/local` only; `local.Client{UseSocketOnly: true}`; pin to
  the daemon's minor; install a `SetVersionMismatchHandler` that logs once.
- The bnm daemon (unprivileged) does: `WatchIPNBus(NotifyInitialState|Prefs|NetMap|
  HealthState)` for live state; `Status` for peers/exit nodes/health; `EditPrefs`
  for connect/disconnect/exit-node/accept-dns; `SetUseExitNode`, `SuggestExitNode`;
  `StartLoginInteractive`+`BrowseToURL`; `Logout`; `QueryDNS`/`DNSConfig`/`GetDNSOSConfig`.
- Writes require operator mode: detect `IsAccessDeniedError` and surface a
  "run `sudo tailscale set --operator=$USER` (NixOS: `services.tailscale.extraSetFlags`)"
  hint. Reads work with no setup.
- Skip `netcheck` unless a diagnostics screen needs it; then shell out to
  `tailscale netcheck --format=json`.

### For the VPN-model ticket

- Tailscale is not an NM VPN plugin: NM only sees `tailscale0` as an externally managed
  tun (dev machine). Its state must be modelled from `ipn.State`
  (`NoState, InUseOtherUser, NeedsLogin, NeedsMachineAuth, Stopped, Starting, Running`,
  [src: backend.go#L22-31][notify]) and `Status.BackendState`, not from NM.
- Connect/disconnect is `Prefs.WantRunning`; "exit node" is a Tailscale-only concept
  (`Prefs.ExitNodeID`, `ExitNodeAllowLANAccess`, `AutoExitNode`); login is a URL the UI
  must open (`BrowseToURL`).
- Authorisation is uid-based and static (one operator), unlike NM's polkit prompts; the
  model needs an explicit "read-only, needs operator" state.

[status]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/status.go#L51
[watch]: https://github.com/tailscale/tailscale/blob/v1.98.10/client/local/local.go#L1320
[ro]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/ipnauth/ipnauth.go#L166
[localdoc]: https://github.com/tailscale/tailscale/blob/v1.98.10/client/local/local.go#L54
[pkglocal]: https://pkg.go.dev/tailscale.com/client/local
[clienttailscale]: https://github.com/tailscale/tailscale/blob/v1.98.10/client/tailscale/tailscale.go#L6
[pkgtailscale]: https://pkg.go.dev/tailscale.com/client/tailscale
[paths]: https://github.com/tailscale/tailscale/blob/v1.98.10/paths/paths.go#L47
[sockperm]: https://github.com/tailscale/tailscale/blob/v1.98.10/safesocket/unixsocket.go#L83
[dial]: https://github.com/tailscale/tailscale/blob/v1.98.10/client/local/local.go#L112
[permit]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/localapi/localapi.go#L210
[prefs]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/prefs.go#L246
[opuid]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/ipnlocal/local.go#L6797
[setgo]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/set.go#L78
[kb1080]: https://tailscale.com/kb/1080/cli
[nix]: https://github.com/NixOS/nixpkgs/blob/nixos-25.05/nixos/modules/services/networking/tailscale.nix#L120
[perms]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/ipnserver/server.go#L336
[editaccess]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/ipnlocal/local.go#L4555
[handlers]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/localapi/localapi.go#L75
[ipnstate]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/ipnstate/ipnstate.go#L33
[notify]: https://github.com/tailscale/tailscale/blob/v1.98.10/ipn/backend.go#L71
[up]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/up.go#L142
[down]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/down.go#L57
[login]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/login.go#L25
[logout]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/logout.go#L43
[exitnode]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/exitnode.go#L78
[suggest]: https://github.com/tailscale/tailscale/blob/v1.98.10/client/local/local.go#L1456
[dnsstatus]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/dns-status.go#L100
[dnsquery]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/dns-query.go#L71
[netcheck]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/netcheck.go#L52
[envknob]: https://github.com/tailscale/tailscale/blob/v1.98.10/envknob/envknob.go#L681
[cligo]: https://github.com/tailscale/tailscale/blob/v1.98.10/cmd/tailscale/cli/cli.go#L117
[gomod]: https://github.com/tailscale/tailscale/blob/v1.98.10/go.mod#L3
