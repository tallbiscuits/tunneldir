# Tunnel Director (`tunneldir`)

Manage a set of SSH tunnels from a single `tunnels.yaml` file. Each tunnel runs
in the **background** via [`autossh`](https://www.harding.motd.ca/autossh/)
(auto-reconnecting), falling back to plain `ssh` when autossh isn't installed.
A status view shows what's up, and any tunnel can be flagged to
**autostart** at boot via a systemd user service.

Single static Go binary — no runtime, no interpreter. Drop it on any machine.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/install.sh | sh
```

This downloads the right release binary for your OS/arch, verifies its SHA256,
and installs it to `~/.local/bin/tunneldir` (no sudo). Pin a version with
`VERSION=v0.1.0 sh` or change the target with `INSTALL_DIR=...`. Make sure
`~/.local/bin` is on your `PATH`.

On a fresh install it also writes a starter `~/.config/tunneldir/tunnels.yaml`
(via `tunneldir init`, which never overwrites an existing config). Edit that file
with your hosts, then `tunneldir validate`. If `autossh` is missing it offers to
install it via your package manager (apt/dnf/pacman/zypper/apk/yum/brew) — it's
recommended for auto-reconnect, but tunneldir falls back to plain `ssh` without it.

### Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/uninstall.sh | sh
```

Removes the systemd unit and the binary, and asks before deleting your config and
state. Add `--purge` (`... | sh -s -- --purge`) to remove config and state without
prompting; `INSTALL_DIR=...` if you installed it somewhere other than `~/.local/bin`.

```sh
tunneldir --version        # show the installed version
tunneldir update           # update in place to the latest release
tunneldir update --check    # report whether a newer release exists
```

`tunneldir` also checks GitHub for a newer release at most once a day and prints
a one-line notice when one is available. Disable it by setting
`TUNNELDIR_NO_UPDATE_CHECK=1`.

### Build from source

```sh
./build.sh                 # -> ./tunneldir   (host platform)
./build.sh all             # -> ./dist/...    (linux/darwin, amd64/arm64)
make install               # -> /usr/local/bin/tunneldir (needs sudo for that dir)
```

Requires Go to build. Runtime needs `ssh`, and ideally `autossh`
(`apt install autossh` / `brew install autossh`) for auto-reconnect.

## Configure

Run `tunneldir init` to write a starter `~/.config/tunneldir/tunnels.yaml`
(or copy `tunnels.example.yaml` yourself). The config is resolved from the first
of these that exists:

1. path given with `--config` / `$TUNNELDIR_CONFIG`
2. `./tunnels.yaml` in the current directory
3. `~/.config/tunneldir/tunnels.yaml`

```yaml
defaults:
  identity_file: ~/.ssh/id_ed25519
  ssh_options:                 # -o KEY=VALUE applied to every tunnel
    ServerAliveInterval: 30
    ServerAliveCountMax: 3
    ExitOnForwardFailure: "yes"

tunnels:
  - name: web-staging          # unique; used in CLI + pid/log file names
    host: staging.example.com
    user: deploy
    port: 22                   # optional ssh port (default 22)
    identity_file: ~/.ssh/id_staging   # optional per-tunnel key
    autostart: true            # start at boot via the systemd unit
    forwards:
      - local: 8080:localhost:80       # -L : reach the server's :80 on localhost:8080
      - local: 5432:db.internal:5432

  - name: socks-prod
    host: prod.example.com
    user: deploy
    forwards:
      - dynamic: 1080                  # -D : SOCKS proxy on localhost:1080

  - name: expose-local
    host: relay.example.com
    forwards:
      - remote: 9000:localhost:3000    # -R : expose local :3000 as relay's :9000
```

**Forward syntax** mirrors ssh (an optional bind address is allowed):

| key        | maps to | format                        |
|------------|---------|-------------------------------|
| `local`    | `-L`    | `[bind:]listen:host:hostport` |
| `remote`   | `-R`    | `[bind:]listen:host:hostport` |
| `dynamic`  | `-D`    | `[bind:]port`                 |

> **Trust your `tunnels.yaml`.** Options under `ssh_options` are passed straight
> to `ssh -o`, which includes directives like `ProxyCommand` and
> `LocalCommand` that run arbitrary programs. Treat the config as executable
> code: only run a `tunnels.yaml` you wrote or fully trust, just as you would a
> shell script or an `ssh_config`.

## Commands

```sh
tunneldir up [names...] [--all] [--autostart] [--print-cmd]
tunneldir down [names...] [--all]
tunneldir restart [names...] [--all]
tunneldir status [names...]      # default command; docker-ps-style table
tunneldir logs <name> [-f]
tunneldir list
tunneldir validate
tunneldir init                   # write a starter config (won't overwrite)
tunneldir install [--run] [--system]   # systemd autostart unit
tunneldir uninstall [--run] [--system]
tunneldir update [--check]       # self-update to the latest release
tunneldir version                # print the version
```

The convenience form `tunneldir <name> <command>` also works, e.g.
`tunneldir web-staging up`, `tunneldir web-staging logs -f`.

```
$ tunneldir status
NAME          TARGET                      FORWARDS    AUTOSTART   PID     UPTIME   STATUS
web-staging   deploy@staging.example.com  L:8080 L:5432  yes       40213   3h12m    UP
socks-prod    deploy@prod.example.com     D:1080      no          40517   12m      DEGRADED
expose-local  relay.example.com           remote      no          -       -        DOWN
```

- **UP** — process alive **and** every local (`-L`/`-D`) listen port is accepting
  connections.
- **DEGRADED** — process alive but a listen port isn't answering.
- **DOWN** — not running.
- `-R` forwards listen on the remote side and can't be probed locally, so they
  show as `remote` and count toward liveness by process only.

`tunneldir up <name> --print-cmd` prints the exact `autossh`/`ssh` command without
running it — handy for debugging.

## Autostart at boot

Tunnels with `autostart: true` are started by a generated systemd unit, pinned
to the config you installed it from. At boot the service runs
`tunneldir up --autostart`; on stop it runs `tunneldir down --all`. Manual-only
tunnels wait for an explicit `tunneldir up <name>`.

There are two flavours — pick based on whether you need the tunnels up **at
boot, before/without anyone logging in** (the usual case for a server):

### System service (`--system`) — survives reboot, recommended for servers

A system-wide unit that runs as you and starts at boot independently of any
login session. Needs `sudo`, but **no linger**.

```sh
tunneldir install --system           # preview the unit + sudo commands (dry-run)
tunneldir install --system --run     # write /etc/systemd/system/tunneldir.service,
                                     #   daemon-reload, enable --now (via sudo)
```

Remove it with `tunneldir uninstall --system --run`.

### User service (default) — no sudo, but session-scoped

A per-user unit. No sudo required, but a systemd **user** instance only runs at
boot if you have *lingering* enabled — otherwise the unit is **session-scoped**:
the tunnels come up when you log in and go down when you log out, so they do
**not** survive a reboot on their own. (Lingering is off by default for good
reason — it keeps user processes running with no active session.)

```sh
tunneldir install              # preview the unit + commands (dry-run)
tunneldir install --run        # write ~/.config/systemd/user/tunneldir.service,
                               #   daemon-reload, enable --now
loginctl enable-linger $USER   # OPTIONAL: make the user unit start at boot too
```

`install` and `status` will tell you when the user unit is session-scoped and
point you at the boot-persistent options.

Remove it with `tunneldir uninstall --run`.

**No systemd?** A user crontab line starts the tunnels at boot with no sudo and
no linger (cron runs it outside any login session):

```
@reboot /usr/local/bin/tunneldir --config /path/to/tunnels.yaml up --autostart
```

### Keys for unattended tunnels

A service that starts at boot has **no `ssh-agent`** and no human to type a
passphrase. A passphrase-protected key therefore can't authenticate and the
tunnel fails with `Permission denied (publickey)` — even though it works fine
when you run it interactively (your agent has the key unlocked).

So an `autostart` tunnel needs a **passphrase-less key**. The recommended setup
is a dedicated key, locked down on the server so it can do nothing but forward:

```sh
ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_tunneldir          # no passphrase
ssh-copy-id -i ~/.ssh/id_tunneldir.pub you@server           # or paste it manually
```

Point the tunnel (or `defaults`) at it with `identity_file: ~/.ssh/id_tunneldir`,
and on the server restrict the key in `~/.ssh/authorized_keys`:

```
restrict,permitopen="localhost:8000" ssh-ed25519 AAAA... tunneldir
```

`tunneldir install` and `tunneldir status` warn when an autostart tunnel's key is
passphrase-protected or missing, and `status` shows the reason a tunnel is
`DEGRADED` (auth, refused, DNS, timeout, …) so failures aren't silent.

## Notes

- **autossh vs ssh:** with `autossh` installed, a dropped connection
  reconnects automatically. Without it, the plain-`ssh` fallback is launched with
  ServerAlive options so it *exits* on a dead link — under the systemd service it
  is restarted; run standalone, it simply shows `DOWN` until the next
  `tunneldir up`. Install autossh for unattended use.
- **State:** pid and log files live under `$XDG_STATE_HOME/tunneldir/`
  (`~/.local/state/tunneldir/`): `pids/<name>.pid`, `logs/<name>.log`.
- **Host keys:** first connections may need the server's key accepted. Either
  connect once interactively, or set `StrictHostKeyChecking`/`UserKnownHostsFile`
  under `defaults.ssh_options`.

## Security

`tunneldir` is a **local, single-user** tool. It runs as you, never as root, and
exposes no network service of its own — the transport security is plain `ssh`'s.
The intended threat model is "a tool you run on your own machine with your own
config." Within that scope:

- **Your `tunnels.yaml` is trusted, executable input.** `ssh_options` are passed
  straight to `ssh -o`, which honours directives such as `ProxyCommand` and
  `LocalCommand` that run arbitrary programs. Only run a config you wrote or fully
  trust — the same care you'd give a shell script or an `ssh_config`.
- **Host-key verification is delegated to ssh.** `tunneldir` does not relax it;
  set `StrictHostKeyChecking`/`UserKnownHostsFile` yourself if you want it
  stricter than your ssh defaults.
- **State is kept private.** The state directory, logs and pidfiles under
  `~/.local/state/tunneldir/` are created `0700`/`0600`, since logs can reveal
  hostnames, usernames and connection detail.
- **Process tracking is defensive.** A tracked pid is only treated as a live
  tunnel if it still looks like our `ssh`/`autossh` process, so a recycled pid is
  never mistakenly killed.

Found a security issue? Please open an issue (or email the maintainer) rather
than disclosing it publicly until it's addressed.

## License

MIT — see [LICENSE](LICENSE).
