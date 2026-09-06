# Speed test provider for `bnm speed`

Ticket: [dopeCape/better-nm#5](https://github.com/dopeCape/better-nm/issues/5). Researched 2026-09-07 against primary sources (official docs, terms pages, source repos, and live probes of the endpoints). Constraint: on-demand only, no API keys, must be legal for a public open-source client.

## Recommendation

- **Default: Cloudflare (`speed.cloudflare.com/__down` and `__up`), implemented natively in Go** (plain `net/http`, no third-party library). Cloudflare ships the reference engine as an MIT npm package whose defaults point at exactly these endpoints, and its blog says the client was open-sourced so others "can perform network quality tests" ([cf-readme], [cf-blog]). The protocol is two HTTP endpoints; the only Go wrapper is unmaintained (last commit 2023, 4 stars) and buggy, so reimplementing (~300 lines) is cheaper than adopting it.
- **Optional self-hosted mode: iperf3**, by shelling out to the distro-packaged `iperf3 -c <host> -J` binary and parsing its JSON. This is the only option that gives TCP retransmits and UDP jitter/loss against infrastructure the user controls.
- **Also support `--provider librespeed --server <url>`** for people who run a LibreSpeed backend (Go server available). Do not default to the public LibreSpeed list: its maintainers say "No infrastructure guaranteed" and half the entries were dead in August 2026 ([ls-issue837]).
- **Do not use Ookla.** The official CLI is a proprietary binary whose EULA forbids redistribution and limits use to personal, non-commercial, single-PC use; the unofficial protocol runs against servers covered by Terms of Use that prohibit "retrieval application[s]" and non-personal use ([ookla-eula], [ookla-terms]). At most, detect a user-installed official `speedtest` binary and offer it as an opt-in.

## Comparison

| | Cloudflare | LibreSpeed (public list) | Ookla official CLI | Ookla unofficial protocol | iperf3 (self-hosted) |
|---|---|---|---|---|---|
| Download / upload | yes / yes | yes / yes | yes / yes | yes / yes | yes / yes (`-R`, `--bidir`) |
| Latency, jitter | HTTP RTT (unloaded and loaded), jitter | HTTP RTT and ICMP, jitter | yes | HTTP, TCP:8080 or ICMP; jitter | TCP RTT in JSON; UDP jitter |
| Packet loss | only with your own TURN server | no | yes | UDP to server:8080 | UDP mode: loss, out-of-order |
| API key | none | none | none (must accept EULA) | none | none |
| Terms for an OSS client | permitted (MIT engine targets these URLs) | no AUP published; sponsor goodwill | EULA forbids redistribution, non-personal use | ToS prohibits retrieval apps | BSD-3; you own the server |
| Rate limit | none published; 429 handling was added to the engine in 2026 | none published | n/a | ToS-based | n/a |
| Go library | none worth adopting | `librespeed-cli` is LGPL-3.0 and CLI-shaped | none (binary only) | `showwin/speedtest-go` MIT, active | wrappers only; native ports immature |

## Cloudflare

**What is measured.** The engine reports download and upload bandwidth (bps), unloaded latency, loaded latency during download and during upload, jitter, packet loss (TURN only) and AIM quality scores ([cf-readme]). Jitter is "the mean absolute difference between consecutive latency samples" ([cf-calc]). Bandwidth is the 90th percentile of per-request samples, latency the median (`bandwidthPercentile: 0.9`, `latencyPercentile: 0.5`) ([cf-readme]).

**Protocol (verified by live probe on 2026-09-06 from colo BOM).**
- Download: `GET https://speed.cloudflare.com/__down?bytes=N` returns `Content-Length: N` of `application/octet-stream` with `Cache-Control: no-store, no-transform` ([probe]). The reference Go wrapper adds an optional `measId` query param ([bh-url]).
- Upload: `POST https://speed.cloudflare.com/__up` with the payload as the body; response is empty with `cf-meta-upload-bytes: <n>` echoing the received byte count ([probe]).
- Latency: `GET __down?bytes=0`, taking time-to-first-byte (`responseStart - requestStart`) ([cf-readme]).
- Server time: subtract the server's processing time from TTFB using the `server-timing` header. The engine first looks for `cfRequestDuration;dur=` and otherwise sums every `cfSpeed*;dur=` entry ([cf-engine]). The live headers were `Server-Timing: cfSpeedEdge;dur=4, cfSpeedWorker;dur=194` on a 10 MB download and `dur=2, dur=18` on a 0-byte request ([probe]).
- Bonus: every response also carries `server-timing: cfL4;desc="?proto=TCP&rtt=16451&min_rtt=15958&rtt_var=4108&...&retrans=0&delivery_rate=..."` (the edge's kernel TCP stats, microseconds), plus `cf-meta-ip`, `cf-meta-colo`, `cf-meta-asn`, `cf-meta-country`, `cf-meta-city`, latitude/longitude and timezone headers ([probe]). `GET /meta` returned `{}` to curl; `/cdn-cgi/trace` returns `ip=`, `colo=`, `loc=` lines ([probe]).
- Speed per request: download = `8 * transferSize / (ttfb - serverTime + payloadTime)`; upload = `8 * bytes / ttfb` ([cf-engine]). Requests shorter than `bandwidthMinRequestDuration` (10 ms) are discarded ([cf-readme]).
- Ramp-up: the default sequence is latency x1, download 100 KB x1 (bypass), latency x20, download 100 KB x9, 1 MB x8, upload 100 KB x8, packet loss, upload 1 MB x6, download 10 MB x6, upload 10 MB x4, download 25 MB x4, upload 25 MB x4, download 100 MB x3, upload 50 MB x3, download 250 MB x2. Larger sizes in a direction are skipped once a request lasts `bandwidthFinishRequestDuration` (1000 ms) ([cf-readme]). Worst case the full sequence moves roughly 970 MB down and 300 MB up, so `bnm speed` should expose a byte budget / `--quick` flag for metered links.
- Loaded latency: extra `bytes=0` GETs every `loadedLatencyThrottle` (400 ms) while transfers run ([cf-readme]).
- Packet loss: UDP through a WebRTC TURN server. Cloudflare's public TURN endpoint "is deprecated and will be discontinued soon" and "You must provide your own TURN server configuration" ([cf-readme]). Out of scope for bnm.
- Results logging: the engine defaults `logAimApiUrl` to `https://speed.cloudflare.com/__results` and `logMeasurementApiUrl` to `null` ([cf-config]); the README notes "measurement results are collected by Cloudflare on completion" via that POST ([cf-readme]). bnm should simply not call `__results` (the engine allows `null`), which also avoids the M-Lab data-sharing described in [cf-blog].

**Terms and limits.** `speed.cloudflare.com` links only to Cloudflare's generic Website Terms and privacy policy ([probe]). Those Terms (effective 2025-08-01) define "Websites" as "www.cloudflare.com, as well as the other websites that Cloudflare operates and that link to these Terms"; section 7 forbids use "that could damage, disable, overburden, disrupt or impair any Cloudflare servers or APIs" and exceeding "limitations on the Websites or Online Services, including on any API calls"; the automated-bot clause is scoped to AI/ML training ([cf-terms]). `robots.txt` disallows `/__down`, `/__up`, `/__log`, `/__results` for crawlers ([probe]), a crawler directive rather than a licence, but it is a reason to send a clear `User-Agent: bnm/<version> (+https://github.com/dopeCape/better-nm)`. No rate limit is documented; issue #43 asking about rate limits (2024-10-30) has no answer ([cf-issue43]), and PR #155 (2026-09-02) makes the engine retry on 408/429/5xx and stop on other HTTP errors ([cf-pr155]), so bnm should treat 429 as "back off and report". Package: `@cloudflare/speedtest` 1.13.1, MIT, released 2026-08-21, last commit 2026-08-25, 735 stars ([gh-meta], [cf-pkg]).

**Go libraries.** `github.com/bruceharrison1984/cloudflare-speed-test` (MIT) uses `__down?measId=%d&bytes=%d`, `__up?measId=%d` and `/meta` ([bh-url]) but has 4 stars, no releases, last commit 2023-08-07 ([gh-meta]), and its default config overwrites index `[2]` twice, dropping the 10 MB stage ([bh-default]). The other "Cloudflare speed test" Go repos found are CDN-IP pickers, not connection tests. Verdict: write it natively.

## LibreSpeed

**What is measured.** Download, upload, ping and jitter; ping is "NOT an ICMP ping" but the RTT of repeated HTTP GETs of an empty file ([ls-doc]). The Go CLI additionally does ICMP via `pro-bing` unless `--no-icmp` ([ls-readme], [ls-gomod]).

**Protocol.** Backends expose `garbage.php?ckSize=N` (incompressible data, N in MB, 4-1024), `empty.php` (HTTP 200 with minimal headers, used for upload POSTs and ping GETs) and `getIP.php` ([ls-doc]). Browser defaults: 6 parallel download streams of 100 MB chunks, 1.5 s grace, 15 s max; 3 upload streams of 20 MB blobs, 3 s grace, 15 s max; 10 pings; 1.06 overhead compensation factor ([ls-doc]). CLI defaults: `--concurrent 3`, `--duration 15`, `--chunks 100`, `--upload-size 1024` KiB, `--timeout 15` ([ls-readme]). The CLI's jitter is an EWMA of successive RTT deltas (weights 0.7/0.3 when jitter rises, 0.8/0.2 when it falls) ([ls-server-go]). Server list objects are `{id, name, server, dlURL, ulURL, pingURL, getIpURL, sponsorName, sponsorURL}` ([ls-servers], [ls-readme]).

**Public servers.** The CLI fetches `https://librespeed.org/backend-servers/servers.php` ([ls-cli-src]); on 2026-09-07 it returned 22 sponsor-donated entries ([ls-servers]). On 2026-08-11 it had 45 entries of which 22 failed (no DNS, missing TLS certs for `*.backend.librespeed.org`, non-LibreSpeed responses) ([ls-issue837], [ls-issue829]). A maintainer replied: "This is an open source project. No infrastructure guaranteed. Host your own servers and use them" and that librespeed.org's owner "is not maintaining anymore" ([ls-issue837]). librespeed.org publishes a privacy policy but no acceptable-use policy or rate limit ([ls-site]). Telemetry goes to librespeed.org only when `--share`/`--telemetry-*` options are used ([ls-readme]); keep it off.

**Go code.** `github.com/librespeed/speedtest-cli` (LGPL-3.0, v1.0.14 released 2026-08-17, 840 stars, 31 open issues, Go 1.25) ([gh-meta], [ls-gomod]) is a CLI, not a library: its `defs.Server` methods write progress to stdout and use `http.DefaultClient` ([ls-server-go]). Importing LGPL-3.0 code into a statically linked Go binary triggers the "Combined Works" relinking obligations of LGPL section 4 ([lgpl]). Use it as a protocol reference only. For self-hosting, `github.com/librespeed/speedtest-go` (LGPL-3.0, v1.1.6, 2026-04-30) is a single-binary server "Compatible with PHP frontend predefined endpoints" ([lsgo-readme], [gh-meta]).

## Ookla

**Official CLI.** A proprietary binary from packagecloud / install.speedtest.net; the page advertises download, upload, latency and packet loss, JSON/JSONL/CSV output, and invites users to "Use Speedtest in your programs by wrapping it" ([ookla-cli]). The EULA grants "a limited, non-exclusive and non-transferable license to use the Software through a command line interface for your personal, non-commercial use on a single personal computer", and forbids to "(c) reverse engineer ... (e) rent, lease, lend, sell, sublicense, assign, distribute, publish, transfer, or otherwise make available the Software ... to any third party ... or (f) install or use the Software on any router, modem, or other non-personal computer device" ([ookla-eula]). So bnm cannot bundle or download it, and users on business machines are outside the grant. Feasible only as: if `speedtest` is already on `PATH`, run `speedtest --format=json --accept-license --accept-gdpr` after the user opts in.

**Unofficial protocol** (as implemented by `github.com/showwin/speedtest-go`, MIT, v1.8.3 released 2026-09-01, 842 stars, active) ([gh-meta]). Server list: `https://www.speedtest.net/api/js/servers` with fallbacks `speedtest-servers-static.php` and `api/ios-config.php` ([sw-server]). HTTP mode: `GET <base>/random{N}x{N}.jpg` for N in 350..4000, `POST <base>/upload.php` with 100..4000 kB bodies, latency via `GET latency.txt` ([sw-request]). TCP mode on port 8080: `HI` -> `HELLO <version>`, `PING <unix-ns>` -> 19-byte echo, `DOWNLOAD <bytes>`, `UPLOAD <bytes> 0` then payload -> `OK <bytes>`, `QUIT`; some servers disable 8080 ([sw-tcp]). Packet loss: UDP datagrams `LOSS <nonce> <seq> <uuid>` to the server with `INITPLOSS`/`PLOSS` counters over TCP, sampled for 30 s ([sw-udp], [sw-loss]). The speedtest.net Terms of Use state users may not "use any robot, spider, site search and/or retrieval application, or other device to crawl, scrape, ... retrieve or index any portion of the Services", that "Users of the Services may use the Content only for their personal, noncommercial use", and that "Businesses, organizations or other legal entities ... are not permitted to use the Services for any purpose" ([ookla-terms]). The best-known unofficial client, `sivel/speedtest-cli`, is archived ([gh-meta]). Not acceptable as a default for a public open-source client.

## iperf3 (self-hosted)

**What is measured.** TCP throughput per interval and summary, with retransmits and congestion window and RTT in `-J` JSON; UDP mode reports `jitter_ms`, `lost_packets`, `lost_percent`, `out_of_order` ([ip-api-c]). `-R` reverses direction (server sends), `--bidir` runs both at once ([ip-invoking]). iperf3 "is not backwards compatible with the original iperf" ([ip-site]). Licence: three-clause BSD, LBNL ([ip-site], [ip-license]). Current release 3.21 (2026-04-09) ([ip-site], [gh-meta]).

**Protocol** (from source, enough to implement a client). Default port TCP 5201 ([ip-iperf-h]). The client opens a control connection, generates a 37-byte ASCII cookie (`COOKIE_SIZE 37 /* size of an ascii uuid */`) and writes it first ([ip-iperf-h], [ip-client]). The server then drives a state machine by sending single signed-byte state codes: `PARAM_EXCHANGE 9`, `CREATE_STREAMS 10`, `TEST_START 1`, `TEST_RUNNING 2`, `EXCHANGE_RESULTS 13`, `DISPLAY_RESULTS 14`, `IPERF_DONE 16`, `ACCESS_DENIED -1`, `SERVER_ERROR -2` ([ip-api-h], [ip-client]). Control messages are JSON prefixed by a 4-byte big-endian length ([ip-json]); parameters include `tcp`/`udp`, `omit`, `time`, `num`, `reverse`, `len` ([ip-params]). Optional authentication uses `--username` plus an RSA public key on the client and `--authorized-users-path` (SHA-256 of `{user}password`) on the server, only when built with OpenSSL ([ip-invoking]). ESnet's docs list no public servers; third-party lists exist but carry no terms, so bnm should only target a host the user names.

**Go code.** `github.com/BGrewell/go-iperf` (BSD-2) wraps the iperf3 binary and expects embedded binaries via `go-bindata`; 24 stars, last commit 2024-05-22 ([bg-readme], [gh-meta]). `ablekh/iperf3-go` (MIT) is a native client+server claiming wire compatibility, but its module path is the bare `iperf3-go`, its code lives under `internal/`, and it has 2 stars with one commit burst in 2025-06 ([ab-gomod], [ab-readme], [gh-meta]); `mfreeman451/iperf-go` is similar (8 stars, 2025-03) ([gh-meta]). Verdict: shell out to the system `iperf3` (packaged everywhere) with `-J`, and parse the JSON; keep a native Go client as a later option using the state machine above.

## Proposed design for `bnm speed`

- `Provider` interface: `Run(ctx, Options, progress func(Sample)) (Result, error)`; `Result{DownloadBps, UploadBps, LatencyMs, JitterMs, LoadedLatencyMs *float64, PacketLoss *float64, Server, Provider}`. Providers: `cloudflare` (default), `librespeed` (requires `--server`, or `--public-list` with a warning), `iperf3` (requires `--server host[:port]`, flags `--reverse`, `--udp`), `ookla-cli` (only if the binary is found and the user opts in).
- Cloudflare defaults: 20 latency samples (median, jitter = mean |delta|), ramp 100 KB -> 1 MB -> 10 MB -> 25 MB -> 100 MB per direction, stop a direction once a request exceeds 1 s, 90th percentile, subtract `cfSpeed*` server timing, 4 parallel streams at the larger sizes, total byte cap (default 300 MB, `--quick` 30 MB), never POST to `__results`, custom User-Agent, treat 429 as a soft failure with retry-after.
- Daemon never runs a provider; `bnm speed` is CLI/TUI-triggered only (ticket constraint).

## Sources

- [cf-readme] https://github.com/cloudflare/speedtest/blob/main/README.md
- [cf-engine] https://github.com/cloudflare/speedtest/blob/main/src/engines/BandwidthEngine/BandwidthEngine.ts
- [cf-calc] https://github.com/cloudflare/speedtest/blob/main/src/Results/MeasurementCalculations.ts
- [cf-config] https://github.com/cloudflare/speedtest/blob/main/src/config/defaultConfig.ts
- [cf-pkg] https://github.com/cloudflare/speedtest/blob/main/package.json
- [cf-blog] https://blog.cloudflare.com/aim-database-for-internet-quality/
- [cf-terms] https://www.cloudflare.com/website-terms/
- [cf-issue43] https://github.com/cloudflare/speedtest/issues/43
- [cf-pr155] https://github.com/cloudflare/speedtest/pull/155
- [probe] curl against `https://speed.cloudflare.com/__down?bytes=10000000`, `__down?bytes=0`, `POST __up`, `/meta`, `/cdn-cgi/trace`, `/robots.txt`, `/` on 2026-09-06 (UTC), colo BOM
- [bh-url] https://github.com/bruceharrison1984/cloudflare-speed-test/blob/main/providers/urlprovider.go
- [bh-default] https://github.com/bruceharrison1984/cloudflare-speed-test/blob/main/config/default.go
- [ls-doc] https://github.com/librespeed/speedtest/blob/master/doc.md
- [ls-readme] https://github.com/librespeed/speedtest-cli/blob/master/README.md
- [ls-cli-src] https://github.com/librespeed/speedtest-cli/blob/master/speedtest/speedtest.go
- [ls-server-go] https://github.com/librespeed/speedtest-cli/blob/master/defs/server.go
- [ls-gomod] https://github.com/librespeed/speedtest-cli/blob/master/go.mod
- [ls-servers] https://librespeed.org/backend-servers/servers.php (fetched 2026-09-07)
- [ls-site] https://librespeed.org/
- [ls-issue829] https://github.com/librespeed/speedtest/issues/829
- [ls-issue837] https://github.com/librespeed/speedtest/issues/837
- [lsgo-readme] https://github.com/librespeed/speedtest-go/blob/master/README.md
- [lgpl] https://www.gnu.org/licenses/lgpl-3.0.html (section 4, Combined Works)
- [ookla-cli] https://www.speedtest.net/apps/cli (via Wayback capture 2026-09-04; the live site returns 403 to non-browser clients)
- [ookla-eula] https://www.speedtest.net/about/eula (via Wayback capture 2026-08-26)
- [ookla-terms] https://www.speedtest.net/about/terms (via Wayback capture 2026-08-26)
- [sw-server] https://github.com/showwin/speedtest-go/blob/master/speedtest/server.go
- [sw-request] https://github.com/showwin/speedtest-go/blob/master/speedtest/request.go
- [sw-tcp] https://github.com/showwin/speedtest-go/blob/master/speedtest/transport/tcp.go
- [sw-udp] https://github.com/showwin/speedtest-go/blob/master/speedtest/transport/udp.go
- [sw-loss] https://github.com/showwin/speedtest-go/blob/master/speedtest/loss.go
- [ip-site] https://software.es.net/iperf/
- [ip-license] https://github.com/esnet/iperf/blob/master/LICENSE
- [ip-invoking] https://github.com/esnet/iperf/blob/master/docs/invoking.rst
- [ip-iperf-h] https://github.com/esnet/iperf/blob/master/src/iperf.h (`COOKIE_SIZE`, `PORT`)
- [ip-api-h] https://github.com/esnet/iperf/blob/master/src/iperf_api.h (state constants)
- [ip-api-c] https://github.com/esnet/iperf/blob/master/src/iperf_api.c (UDP summary fields)
- [ip-json] https://github.com/esnet/iperf/blob/master/src/iperf_api.c (`JSON_write`, `JSON_read`)
- [ip-params] https://github.com/esnet/iperf/blob/master/src/iperf_api.c (`send_parameters`, `get_parameters`)
- [ip-client] https://github.com/esnet/iperf/blob/master/src/iperf_client_api.c (`iperf_connect`, `iperf_handle_message_client`)
- [bg-readme] https://github.com/BGrewell/go-iperf/blob/master/README.md
- [ab-readme] https://github.com/ablekh/iperf3-go/blob/main/README.md
- [ab-gomod] https://github.com/ablekh/iperf3-go/blob/main/go.mod
- [gh-meta] GitHub REST API `repos/{owner}/{repo}`, `releases/latest`, `commits?per_page=1`, queried 2026-09-07
