# Tunnel Director (`tunneldir`)

Manage a set of SSH tunnels from one `tunnels.yaml` file. Tunnels run in the
background through `autossh`, which reconnects them when the connection drops.
You can check their state with a single command and have selected tunnels start
at boot. It ships as a single static binary with no runtime dependencies.

## Quick start

**1. Install**

```sh
curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/install.sh | sh
```

This installs `tunneldir` to `~/.local/bin` (no sudo) and creates an empty
config at `~/.config/tunneldir/tunnels.yaml`. If `autossh` is missing, the
installer offers to install it.

**2. Add a tunnel**

```sh
tunneldir add
```

It asks for a name, the server, and which ports to forward, for example
`L 8080:localhost:80` to reach the server's port 80 on your port 8080. To change
a tunnel later, run `tunneldir edit`. To delete one, run `tunneldir remove`.

**3. Start it and check on it**

```sh
tunneldir up
tunneldir status
```

`up` without a name lists the stopped tunnels with numbers. Pick one or
more of them (`1 3`, `2-4`, or `a` for all). You can also give any unique part
of a name: `tunneldir up web`.

**4. (Optional) Start tunnels at boot.** Answer yes to "Start automatically at
boot?" when you add the tunnel, or later with `tunneldir edit`. Then install the
boot service once:

```sh
tunneldir install --system --run      # needs sudo
```

A tunnel that starts at boot needs an SSH key without a passphrase. See
[Autostart at boot](#autostart-at-boot).

To undo it:

- **Stop one tunnel from starting at boot:** run `tunneldir edit <name>` and
  answer no to "Start automatically at boot?". The service keeps starting any
  other autostart tunnels.
- **Remove the boot service entirely:** run
  `tunneldir uninstall --system --run`. Stopping the service runs
  `tunneldir down --all`, so this stops all running tunnels, including ones you
  started by hand.

## Commands

| Command | What it does |
|---|---|
| `tunneldir status [names...]` | Show a status table. This is the default when you give no command. |
| `tunneldir up [names...] [--all] [--autostart]` | Start the named tunnels, all of them, or only those with `autostart: true`. |
| `tunneldir down [names...] [--all]` | Stop tunnels. |
| `tunneldir restart [names...] [--all]` | Restart tunnels. |
| `tunneldir logs <name> [-f]` | Show a tunnel's log (`-f` follows it). |
| `tunneldir add [name]` | Add a tunnel. It asks for each setting. |
| `tunneldir edit [name]` | Change a tunnel's settings. Press Enter to keep a value, or type `-` to clear an optional one. If the tunnel is running, it offers to restart it. |
| `tunneldir remove [name] [--yes]` | Remove a tunnel from the config, stopping it first. It asks for confirmation unless you pass `--yes`. |
| `tunneldir list` | List the configured tunnels. |
| `tunneldir up <name> --print-cmd` | Print the exact `ssh`/`autossh` command without running it. |
| `tunneldir init` | Write a starter config. An existing config is never overwritten. |
| `tunneldir install` / `uninstall [--system] [--run]` | Install or remove the autostart unit. Without `--run` it only previews what it would do. |
| `tunneldir update [--check]` | Update to the latest release, or with `--check`, only report whether one exists. |
| `tunneldir version` | Print the version. |

Tunnel names can be shortened to any unique part (`web` for `web-staging`).
Run `up`, `down`, `restart`, `logs`, `edit` or `remove` without a name to pick from a numbered
list. You can also put the name first: `tunneldir web up`,
`tunneldir web logs -f`.

```
$ tunneldir status
NAME          TARGET                      FORWARDS       AUTOSTART  PID    UPTIME  STATUS
web-staging   deploy@staging.example.com  L:8080 L:5432  yes        40213  3h12m   UP
socks-prod    deploy@prod.example.com     D:1080         no         40517  12m     DEGRADED
expose-local  relay.example.com           remote         no         -      -       DOWN
```

| Status | Meaning |
|---|---|
| **UP** | The process is running and every local (`-L`/`-D`) port accepts connections. |
| **DEGRADED** | The process is running but a local port isn't answering. `status` shows the likely reason (authentication, connection refused, DNS, timeout, …). |
| **DOWN** | The tunnel is not running. |

A `-R` forward listens on the remote server, so it can't be checked from this
machine. It shows as `remote`, and only the process being alive counts toward
its status.

## Configuration

`tunneldir` uses the first config file it finds:

1. the path given with `--config` or `$TUNNELDIR_CONFIG`
2. `./tunnels.yaml` in the current directory
3. `~/.config/tunneldir/tunnels.yaml`

`tunneldir add` and `edit` cover each tunnel's own settings. For `defaults`
and `ssh_options`, edit the file directly. A full example (also in
`tunnels.example.yaml`):

```yaml
defaults:
  identity_file: ~/.ssh/id_ed25519
  ssh_options:                 # passed as -o KEY=VALUE to every tunnel
    ServerAliveInterval: 30
    ServerAliveCountMax: 3
    ExitOnForwardFailure: "yes"

tunnels:
  - name: web-staging          # unique; used in commands and file names
    host: staging.example.com
    user: deploy
    port: 22                   # optional, default 22
    identity_file: ~/.ssh/id_staging   # optional key for this tunnel only
    autostart: true            # start at boot
    forwards:
      - local: 8080:localhost:80
      - local: 5432:db.internal:5432

  - name: socks-prod
    host: prod.example.com
    user: deploy
    forwards:
      - dynamic: 1080                  # SOCKS proxy on localhost:1080

  - name: expose-local
    host: relay.example.com
    forwards:
      - remote: 9000:localhost:3000    # your local :3000 becomes the relay's :9000
```

Each forward uses the same syntax as the matching `ssh` flag. A bind address in
front is optional.

| Key | ssh flag | Format |
|---|---|---|
| `local` | `-L` | `[bind:]listen:host:hostport` |
| `remote` | `-R` | `[bind:]listen:host:hostport` |
| `dynamic` | `-D` | `[bind:]port` |

> **Only use a config you trust.** `ssh_options` go straight to `ssh -o`, and
> options such as `ProxyCommand` and `LocalCommand` run arbitrary programs.
> Treat `tunnels.yaml` like a shell script.

## Autostart at boot

Tunnels with `autostart: true` are started by a systemd unit that
`tunneldir install` generates. The unit is tied to the config file you installed
it from. At boot it runs `tunneldir up --autostart`, and when stopped it runs
`tunneldir down --all`. Any command without `--run` only previews the changes.

| | System service (`--system`) | User service (default) |
|---|---|---|
| Survives reboot | Yes. It starts at boot, with or without a login. | Only with lingering enabled. Otherwise tunnels start when you log in and stop when you log out. |
| Needs sudo | Yes | No |
| Install | `tunneldir install --system --run` | `tunneldir install --run` (plus `loginctl enable-linger $USER` to make it start at boot) |
| Remove | `tunneldir uninstall --system --run` | `tunneldir uninstall --run` |

The system service is recommended for servers. `install` and `status` warn you
when a user service won't survive a reboot.

**Without systemd,** add a user crontab entry. It needs neither sudo nor
lingering:

```
@reboot /usr/local/bin/tunneldir --config /path/to/tunnels.yaml up --autostart
```

### Keys for unattended tunnels

A service started at boot has no `ssh-agent` and nobody to type a passphrase,
so a passphrase-protected key fails with `Permission denied (publickey)`. It
fails even though the same key works when you run the tunnel by hand. Use a
dedicated key without a passphrase:

```sh
ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_tunneldir
ssh-copy-id -i ~/.ssh/id_tunneldir.pub you@server
```

Set `identity_file: ~/.ssh/id_tunneldir` on the tunnel (or under `defaults`).
Then, on the server, limit that key to port forwarding in
`~/.ssh/authorized_keys`:

```
restrict,permitopen="localhost:8000" ssh-ed25519 AAAA... tunneldir
```

`install` and `status` warn when an autostart tunnel's key is missing or has a
passphrase.

## Updating and uninstalling

```sh
tunneldir update                 # update in place to the latest release
curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/uninstall.sh | sh
```

`tunneldir` checks for a new release at most once a day and prints a one-line
notice when there is one. Set `TUNNELDIR_NO_UPDATE_CHECK=1` to turn this off.

The uninstaller removes the systemd unit and the binary. It asks before deleting
your config and state. To remove those without being asked, use
`... | sh -s -- --purge`. If you installed somewhere other than `~/.local/bin`,
set `INSTALL_DIR=...`.

---

## Technical details

### Install options

- `VERSION=v0.1.0 sh` pins a specific version, and `INSTALL_DIR=...` changes
  where the binary goes. Both work with the install and uninstall scripts.
- The installer picks the release binary for your OS and architecture and
  verifies its SHA256 checksum.
- Runtime requirements: `ssh`, and ideally `autossh` (`apt install autossh` /
  `brew install autossh`).

### Checking the config

`tunneldir validate` parses the config and reports any errors without starting
anything. Every other command reports the same errors, so this is mainly useful
in scripts or after a large edit.

### Building from source

Building requires Go.

```sh
./build.sh          # -> ./tunneldir for this machine
./build.sh all      # -> ./dist/  (linux/darwin, amd64/arm64)
make install        # -> /usr/local/bin/tunneldir (sudo needed for that directory)
make test
```

### autossh vs. plain ssh

With `autossh`, a dropped connection reconnects automatically. Without it,
`tunneldir` falls back to plain `ssh` with ServerAlive options, so a dead
connection makes `ssh` exit. Under the systemd service the tunnel is then
restarted. Run by hand, it shows `DOWN` until the next `tunneldir up`. Install
autossh for unattended use.

### State and host keys

- Pid and log files live in `$XDG_STATE_HOME/tunneldir/` (by default
  `~/.local/state/tunneldir/`), as `pids/<name>.pid` and `logs/<name>.log`.
- The first connection to a server may require accepting its host key. Either
  connect once by hand, or set `StrictHostKeyChecking` and `UserKnownHostsFile`
  under `defaults.ssh_options`.

### Security

`tunneldir` is a local, single-user tool. It runs as you, never as root, and
opens no network service of its own. Connection security is whatever `ssh`
provides.

- **The config is trusted input.** It can run programs through `ssh_options`
  (see above).
- **Host-key checking is left to ssh.** `tunneldir` never relaxes it.
- **State is kept private.** The state directory is created `0700` and logs and
  pidfiles `0600`, because logs can reveal hostnames, usernames and connection
  details.
- **Stopping tunnels is safe.** A saved pid only counts as a tunnel if the
  process is still `ssh` or `autossh`. A pid the OS has reused for another
  program is never killed.

Found a security issue? Please open an issue (or email the maintainer) rather
than disclosing it publicly until it's fixed.

## License

MIT. See [LICENSE](LICENSE).
