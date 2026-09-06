# Build conventions for bnm agents

- Module `github.com/dopeCape/better-nm`, Go 1.25, `go.mod` already lists the main deps. Add a dep only with `go get` and only if truly needed; prefer stdlib.
- Domain types and port interfaces live in `internal/core`. Read `CONTEXT.md` and `internal/core/*.go` first. Do not change existing fields; adding a field or a helper is fine, report it.
- Every package: `gofmt`, `go vet` clean, table-driven tests, `go test -race ./internal/<pkg>/...` green. Tests must not need root or a real network unless behind a build tag: `//go:build live` for tests that touch this machine's real NM/tailscaled (read-only, never change network state), `//go:build integration` for dbusmock-backed tests.
- Errors: wrap with `%w` and context (`fmt.Errorf("nm: activate %s: %w", uuid, err)`). No panics on bad input from the outside world.
- Logging: `log/slog` with a package-scoped logger passed in or `slog.Default()`.
- No goroutine leaks: everything long-running takes a `context.Context` and stops when it ends. Watch channels are buffered and drop-on-full (never block a producer).
- Unprivileged always: never call sudo/pkexec; when a capability is missing, return a typed error with a human hint (what one-time step fixes it).
- Docs: a package comment at the top of one file saying what the package owns and how it is tested.
- Git: work on the branch named in your task, commit as `git -c user.name=dopeCape -c user.email=dopeCape@users.noreply.github.com commit`, end messages with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Push with plain `git push -u origin <branch>` (a repo-local credential helper handles auth). Never run `gh auth switch`; prefix `gh` with `GH_TOKEN=$(gh auth token --user dopeCape)`.
- Do not edit files outside the packages your task names, except `go.mod`/`go.sum` via `go get`.
