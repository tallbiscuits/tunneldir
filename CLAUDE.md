# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`tunneldir` is a single static Go binary (module name `tunneldir`, dependencies `gopkg.in/yaml.v3` and `golang.org/x/term`) that manages background SSH tunnels defined in `tunnels.yaml`, running each via `autossh` (falling back to plain `ssh`), with a docker-ps-style status view and optional systemd autostart. README.md is the user-facing spec. Update it when behaviour, commands or flags change.

## Commands

```sh
./build.sh                 # build ./tunneldir for the host (CGO_ENABLED=0, version from git describe)
./build.sh all             # cross-compile linux/darwin × amd64/arm64 into ./dist
make build / make test     # same build; `go test ./...`
go test ./internal/tunnel -run TestParseForwards   # single test
go vet ./...
./tunneldir --config ./tunnels.yaml up <name> --print-cmd   # show the ssh/autossh command without running it
```

The version is injected with `-ldflags -X main.version=...`. Releases come from pushing a `v*` tag: `.github/workflows/release.yml` runs `build.sh all`, writes `SHA256SUMS` and publishes a GitHub release. `install.sh` and `tunneldir update` both depend on those asset names (`tunneldir-<os>-<arch>`) and on `SHA256SUMS`, so keep all three in sync.

`tunnels.yaml` in the repo root is the developer's personal config and is gitignored, as are `index.html`, `/tunneldir` and `/dist`. Use `tunnels.example.yaml` as the template.

## Architecture

- `cmd/tunneldir/main.go` has hand-rolled argv parsing (no flag library). `extractConfigFlag` pulls `--config` out first. `init`, `install`, `uninstall`, `update` and `version` run before the config is loaded, and every other command requires a valid config. It also rewrites the `tunneldir <name> <verb>` form into `<verb> <name>`. Running with no arguments means `status`. `pick.go` resolves tunnel names: an exact name, or any unique case-insensitive substring. It also provides the numbered picker, which `up`/`down`/`restart`/`logs` show when given no name and stdin is a TTY. Scripts (no TTY) get an error instead of a prompt. After every command, `maybeNotifyUpdate` does an update check that is cached for one day.
- `internal/config` loads and validates YAML. `edit.go` backs `add`/`edit`/`remove` (prompts in `cmd/tunneldir/configcmd.go`). It edits the yaml.v3 node tree instead of re-marshalling `Config`, so user comments survive. `spaceOut` restores the blank lines the encoder drops. Every save goes to a temp file and must pass `Load` before it replaces the original. A config with zero tunnels is valid, and the starter config is `tunnels: []`. `normalizeAndValidate` merges `defaults` and expands `~`.
- `internal/tunnel` turns a `config.Tunnel` into the concrete `ssh`/`autossh` argv (`Command`) and parses forwards (`-L`/`-R`/`-D`) into `ProbeAddr`s that status uses.
- `internal/manager` handles process lifecycle. `launch` starts the command with `Setsid` (the pid is also the process-group id), sends output to the log file, sets `AUTOSSH_GATETIME=0`, writes a pidfile and calls `Release()`. `stop` sends SIGTERM to the whole group (negative pid), then SIGKILL. `Status` counts a pid as alive only if `/proc/<pid>/comm` is `ssh` or `autossh`, which guards against a recycled pid. Don't weaken this check.
- `internal/status` builds the table. The states are UP (process alive and every local listen port accepts TCP), DEGRADED (a port isn't answering; `classifyLogLine` finds the reason in the log tail) and DOWN. `KeyWarning` flags autostart tunnels whose key is missing or passphrase-protected.
- `internal/sshkey` parses private key files directly (it never shells out to `ssh-keygen`) to detect encryption.
- `internal/service` renders and installs the systemd units. There are two variants. The user unit (`~/.config/systemd/user`) only persists across reboot with linger enabled; `LingerWarning` covers that case. The system unit (`--system`) goes to `/etc/systemd/system`, uses sudo and pins `User=`, `HOME` and `WorkingDirectory`. Both run `tunneldir --config <pinned> up --autostart` and `down --all`. `install` without `--run` is a dry run that only prints. Watch out for quoting: Exec and Environment values are systemd-quoted, but `WorkingDirectory=` must stay unquoted (`service_test.go` checks this).
- `internal/paths` resolves XDG paths. State lives in `~/.local/state/tunneldir/{pids,logs}` with 0700/0600 permissions. Tunnel names are validated before they are used in file paths.
- `internal/updater` does the self-update using only the standard library. It must fail soft: a network error must never break a normal command. It verifies SHA256 against `SHA256SUMS`.

The target platform is Linux with systemd. On macOS it builds, and the `/proc`-dependent checks (comm, uptime) degrade gracefully instead of failing.
