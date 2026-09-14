# Build conventions for bnm agents

- Module `github.com/dopeCape/better-nm`, Go 1.25, `go.mod` already lists the main deps. Add a dep only with `go get` and only if truly needed; prefer stdlib.
- Domain types and port interfaces live in `internal/core`. Read `CONTEXT.md` and `internal/core/*.go` first. Do not change existing fields; adding a field or a helper is fine, report it.
- Every package: `gofmt`, `go vet` clean, table-driven tests, `go test -race ./internal/<pkg>/...` green. Tests must not need root or a real network unless behind a build tag: `//go:build live` for tests that touch this machine's real NM/tailscaled (read-only, never change network state), `//go:build integration` for dbusmock-backed tests.
- Errors: wrap with `%w` and context (`fmt.Errorf("nm: activate %s: %w", uuid, err)`). No panics on bad input from the outside world.
- Logging: `log/slog` with a package-scoped logger passed in or `slog.Default()`.
- No goroutine leaks: everything long-running takes a `context.Context` and stops when it ends. Watch channels are buffered and drop-on-full (never block a producer).
- Unprivileged always: never call sudo/pkexec; when a capability is missing, return a typed error with a human hint (what one-time step fixes it).
- Docs: a package comment at the top of one file saying what the package owns and how it is tested.
- Git: work on the branch named in your task, commit as `git -c user.name=dopeCape -c user.email=dopeCape@users.noreply.github.com commit`, end messages with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Push with plain `git push -u origin <branch>` (a repo-local credential helper handles auth). Never run `gh auth switch`; prefix `gh` with `GH_TOKEN=$(gh auth token --user dopeCape)`.
- Do not edit files outside the packages your task names, except `go.mod`/`go.sum` via `go get`.

## Desktop app (`desktop/`)

- Tauri 2: Rust shell in `desktop/src-tauri` (crate `bnm-desktop`), React frontend in `desktop/src`; `desktop/CONTRACT.md` is the boundary. Work inside `nix develop` (rustc, cargo, node 24, pnpm 11, WebKitGTK 4.1 and friends), from `desktop/`.
- Frontend: `pnpm install --frozen-lockfile`, then `pnpm typecheck`, `pnpm lint`, `pnpm test` must pass. Never edit `pnpm-lock.yaml` by hand; `pnpm add` only when truly needed. Changing the lockfile invalidates `pnpmDeps.hash` in `flake.nix`: set it to `""`, run `nix build .#bnm-desktop`, paste the `got:` hash.
- Shell: `cargo fmt --check`, `cargo clippy --all-targets -- -D warnings`, `cargo test` (the live test builds `bnmd --fake` with `go`). `Cargo.lock` is committed and read by `flake.nix` (`cargoLock.lockFile`); there is no `cargoHash` to bump.
- Build: `make desktop` (binary, via `pnpm tauri build --no-bundle`) or `make desktop-bundle` (AppImage, deb, rpm). CI builds the binary on every PR; `release.yml` bundles per arch on tag push with the version taken from the tag (`--config '{"version":"X.Y.Z"}'`), so `version` in `tauri.conf.json` stays `0.1.0` in git.
- Bundle metadata lives in `desktop/src-tauri/tauri.conf.json` (`productName` and `mainBinaryName` are `bnm-desktop`, which names the deb/rpm/AppImage and the installed `.desktop` file). `packaging/desktop/` keeps the launcher entry and icon that `make install` uses.
