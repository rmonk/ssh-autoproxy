# AGENTS.md

Guide for coding agents (and humans) working on ssh-autoproxy. Read
`README.md` first for what the tool does from a user's point of view; this
file covers how the code is put together and the things that are easy to
break.

## What it is

A single Go binary for Linux that makes SSH `ProxyJump` (and a browser
SOCKS5/PAC proxy) conditional on the current network. One long-running
`daemon` (a systemd `--user` service) keeps an `ssh -M -N [-D]` master
connection open to each configured jump host while you're "away"; small
CLI subcommands report status, install the service, and self-update.

## Build, test, run

```sh
go build ./...                 # CI: build
go vet ./...                   # CI: vet
go test ./...                  # CI: test (all three must pass)
gofmt -l .                     # must print nothing
go build -o ssh-autoproxy ./cmd/ssh-autoproxy   # local binary (gitignored)
```

- Go version comes from `go.mod`; CI and releases use `go-version-file: go.mod`.
- Dependencies are deliberately minimal (cobra, yaml.v3). Prefer the
  standard library over adding new modules.
- Release builds stamp the version with
  `-ldflags "-X main.version=vX.Y.Z"`; local builds report `dev`.
- Try a build in the foreground with
  `./ssh-autoproxy daemon -v --config <path>`.

## Layout

| Path | Responsibility |
|---|---|
| `cmd/ssh-autoproxy/` | Cobra CLI: `daemon`, `status`, `check-ssh`, `install`, `update`. Thin wrappers over `internal/`. |
| `internal/config` | YAML config: defaults, per-route SOCKS defaults (incl. random ports), validation. |
| `internal/netstate` | Detect current network (`ip`, `nmcli`) and match it against `direct_profiles` (`Evaluate`). |
| `internal/daemon` | Main loop: config hot-reload, one supervisor per route, PAC server, network watching, notifications. |
| `internal/tunnel` | `Supervisor` owns one `ssh` subprocess per route with backoff; classifies ssh stderr into error categories. |
| `internal/pac` | Generates and serves the PAC file. |
| `internal/proxy` | The daemon's front-door proxy listener (SOCKS5 + HTTP CONNECT on one port): matches each connection's host to a route and relays it through that route's ssh `-D` forward or directly. |
| `internal/notify` | `notify-send` desktop notifications (silently skipped without a D-Bus session). |
| `internal/state` | JSON state file (`$XDG_STATE_HOME/ssh-autoproxy/state.json`): notification cooldowns and the daemon's actual SOCKS addresses. |
| `internal/update` | Self-update from GitHub releases with SHA-256 verification. |
| `.github/workflows/` | `ci.yml` (build/vet/test on PRs and main), `release.yml` (tag-triggered builds + `SHA256SUMS`). |

## Invariants — don't break these

1. **Never edit the user's `~/.ssh/config`, browser, or OS proxy
   settings.** `install` only *prints* the snippets to paste. This is a
   stated promise in the README.
2. **Listeners are loopback-only.** `config.Validate` rejects any
   non-loopback SOCKS or PAC bind. This is a personal proxy, never an
   open relay.
3. **Random SOCKS ports are per process.** `socks_proxy: { active: true }`
   makes `config.Load` pick a fresh free port *every time it runs, in
   every process*. Only the daemon's ports are real. It records them in
   `state.SocksAddrs`, and any other command that needs them (e.g.
   `status`) must read them from the state file and never recompute them
   from config.
4. **The daemon's ssh master runs with `ControlPersist=no`, explicitly.**
   Otherwise ssh backgrounds itself, escapes supervision, and leaves an
   orphaned control socket. The user's jump-host `ssh_config` block sets
   `ControlPersist`, which is why the `-o` override is required. See the
   long comment in `tunnel/supervisor.go` `sshArgs`.
5. **The service only sees the environment it started with.** A systemd
   user service copies its environment at start time, which at boot is
   before the desktop session exports `SSH_AUTH_SOCK`. So `install` writes
   `Environment=SSH_AUTH_SOCK=...` into the unit (`renderUnit`, with
   `%t` for `$XDG_RUNTIME_DIR`). Anything else the daemon or its ssh
   children need from the session environment has the same problem.
   Changes to `unitTemplate` only take effect after users re-run
   `ssh-autoproxy install`, so call that out in the PR and release notes.
6. **`check-ssh` must stay fast, self-contained, and fail safe.** OpenSSH
   runs it on every matching connection via `Match exec`. It must not
   depend on the daemon, and any error or unknown route must `exit 0`
   ("proxy needed"): an unneeded hop is better than a broken direct
   connection.
7. **PAC defaults to `DIRECT`.** Only hosts matching a route's
   `host_patterns`/`host_subnets` may ever go through a tunnel. General
   browsing must never be proxied. The same rule holds in
   `internal/proxy`: anything the routing table doesn't match is dialed
   directly, and a matched route whose tunnel is down fails the connection.
   It never falls back to direct, and it never resolves hostnames locally
   to match `host_subnets`.
8. **A bad config edit never takes the daemon down.** On reload, a config
   that fails to parse or validate is logged and ignored, and the last
   good config keeps running. A successful reload tears down and restarts
   every supervisor (random ports are re-picked and `SocksAddrs`
   rewritten).
9. **Self-update never installs unverified bytes.** `update` refuses a
   release without `SHA256SUMS` or without a matching entry, and leaves
   the existing binary untouched on any failure (temp file + rename).

## Cross-file couplings

- **Release asset names**: `release.yml` builds
  `ssh-autoproxy-<tag>-<goos>-<goarch>.tar.gz`, containing a binary of the
  same name without `.tar.gz`, plus `SHA256SUMS`. `update.ArchiveName` /
  `binaryName` / `ChecksumsAsset` must match. Change both together.
- **Repo slug**: `update.Repo` is hardcoded to `rmonk/ssh-autoproxy`.
- **Tag format**: `release.yml` only fires on `v[0-9]+.[0-9]+.[0-9]+`, and
  `update.IsNewer` only understands `vMAJOR.MINOR.PATCH`. No prerelease
  suffixes without changing both.
- **ssh stderr classification**: notifications come from substring matches
  in `tunnel/stderr.go` (`stderrSignatures`). New failure types go there,
  plus a case in `stderr_test.go` and a message in
  `daemon.describeCategory`. Cooldown is tracked per `route:category`.
- **SOCKS request logging** needs ssh `-vv`, not `-v`. Debug2 is where
  OpenSSH logs `dynamic request: socks...`, which `parseSocksRequest` reads.
- **Host matching exists twice**: the PAC file (`pac.Generate`,
  `shExpMatch`/`isInNet` in the browser) and the proxy listener
  (`proxy.Table.match`, `path.Match` on the lowercased host) must agree on
  which hosts belong to which route, in config order, first match wins.
  Change both together.
- **Front proxy and PAC**: with `proxy.enabled`, `pac.Generate` gets the
  listener's address as `frontAddr` and advertises it in place of each
  route's own `socks_proxy`. The PAC is still dynamic: a route that's
  currently direct gets `DIRECT`.
- **Config schema**: when adding a config field, update `config.Default()`,
  `config.example.yaml`, and the README if it's user-facing.

## Code and test conventions

- Comments explain *why*, often at length (see `supervisor.go`,
  `install.go`). Match that: document the reasoning behind non-obvious
  choices, not what the code literally does.
- Keep logic that shells out (`ip`, `nmcli`, `ssh`, `systemctl`,
  `notify-send`) thin, and put decisions in pure functions that can be
  tested: `netstate.Evaluate`, `classifyStderrLine`, `renderUnit`,
  `pac.Generate`, `update.IsNewer`, and so on. External commands aren't
  mocked; test the pure parts.
- Tests are table-driven where it fits. Network code is tested with
  `httptest` (see `internal/update/update_test.go`). File-system tests use
  `t.TempDir()`.
- `internal/daemon` has no tests. Logic added there is easier to test if
  it goes in small pure helpers.
- Logging is `log/slog`. Expected transient noise (connection refused
  during backoff, no D-Bus) is debug level or silent, never a
  notification.

## Platform notes

- Linux-first: network detection needs `ip` and NetworkManager's
  `nmcli`, and the service is systemd `--user`. darwin binaries are built
  and `update` works there (it skips the systemctl restart), but
  `netstate.Query` will fail on macOS, which `check-ssh` treats as "proxy
  needed".

## Debugging a live install

The developer's own machine runs the released binary from
`~/.local/bin/ssh-autoproxy` as `ssh-autoproxy.service`. Useful read-only
checks:

```sh
systemctl --user status ssh-autoproxy
journalctl --user -u ssh-autoproxy -n 50 --no-pager
ssh-autoproxy status            # or: status --json
cat ~/.config/systemd/user/ssh-autoproxy.service
cat ~/.local/state/ssh-autoproxy/state.json
# environment the running daemon actually has (e.g. SSH_AUTH_SOCK):
tr '\0' '\n' < /proc/$(systemctl --user show -p MainPID --value ssh-autoproxy)/environ
```

- An `exit status 255` loop in the journal is ssh failing to connect or
  authenticate. Check the daemon's environment before assuming the keys
  are wrong.
- Don't restart or reinstall the user's service, or replace
  `~/.local/bin/ssh-autoproxy`, unless asked. To try something, build to
  a scratch path and run it there.
- To try `update` safely, build a throwaway binary with an older version
  stamped (`-ldflags "-X main.version=v0.1.0"`) in a scratch dir and run
  `update --check` / `update --no-restart` on it. `--no-restart` matters
  because it would otherwise restart the real service.

## Workflow

- Work on a branch and open a PR against `main`. The maintainer merges.
  CI (`ci.yml`) must pass.
- Commit messages: short imperative subject, and a body explaining the
  problem and why the change fixes it.
- Releases: after merging, tag the merge commit on `main` with an
  annotated `vX.Y.Z` tag and push the tag. `release.yml` builds
  linux/darwin × amd64/arm64, generates `SHA256SUMS`, and publishes the
  GitHub release with generated notes. Then confirm `SHA256SUMS` is
  attached; `ssh-autoproxy update --check` from an older build should see
  the new version.
