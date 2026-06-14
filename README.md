# Tunnel Director (`tunneldir`)

Manage a set of SSH tunnels from a single `tunnels.yaml` file. Each tunnel runs
in the **background** via [`autossh`](https://www.harding.motd.ca/autossh/)
(auto-reconnecting), falling back to plain `ssh` when autossh isn't installed.
A `docker ps`-style status view shows what's up, and any tunnel can be flagged to
**autostart** at boot via a systemd user service.

Single static Go binary — no runtime, no interpreter. Drop it on any machine.

## Build & install

```sh
./build.sh                 # -> ./tunneldir   (host platform)
./build.sh all             # -> ./dist/...    (linux/darwin, amd64/arm64)
sudo make install          # -> /usr/local/bin/tunneldir
```

Requires Go to build. Runtime needs `ssh`, and ideally `autossh`
(`apt install autossh` / `brew install autossh`) for auto-reconnect.

## Configure

Copy `tunnels.example.yaml` to one of these (first match wins):

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
tunneldir install [--run]        # systemd autostart unit
tunneldir uninstall [--run]
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

Tunnels with `autostart: true` are started by a generated **systemd user**
service. The service is pinned to the config you installed it from.

```sh
tunneldir install              # preview the unit + commands (dry-run)
tunneldir install --run        # write ~/.config/systemd/user/tunneldir.service,
                               #   daemon-reload, enable --now
loginctl enable-linger $USER   # so it starts at boot without an active login
```

At boot the service runs `tunneldir up --autostart`; on stop it runs
`tunneldir down --all`. Manual-only tunnels wait for an explicit
`tunneldir up <name>`.

Remove it with `tunneldir uninstall --run`.

**No systemd?** Add a crontab line instead:

```
@reboot /usr/local/bin/tunneldir --config /path/to/tunnels.yaml up --autostart
```

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
