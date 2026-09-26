// Command tunneldir manages SSH tunnels defined in a YAML file: starting them
// in the background (autossh, falling back to ssh), showing a docker-ps-style
// status view, and optionally autostarting flagged tunnels via systemd.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tunneldir/internal/config"
	"tunneldir/internal/manager"
	"tunneldir/internal/paths"
	"tunneldir/internal/service"
	"tunneldir/internal/status"
	"tunneldir/internal/tunnel"
	"tunneldir/internal/updater"
)

// version is the build's version string. It is "dev" for local builds and is
// overridden at release time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const usage = `tunneldir — manage SSH tunnels from a YAML file

tunneldir keeps a set of SSH tunnels defined in tunnels.yaml. It starts them in
the background with autossh (auto-reconnecting), falling back to plain ssh when
autossh isn't installed, and shows a docker-ps-style view of what's up. Tunnels
flagged "autostart: true" can be brought up at boot via a systemd unit (a
per-user unit, or a system-wide boot service with "install --system").

Usage:
  tunneldir [--config PATH] <command> [names...]
  tunneldir <name> <command>            (convenience form, e.g. "tunneldir web up")

Names may be abbreviated to any unique part ("web" for "web-staging"). Run up,
down, restart, logs, edit or remove without a name to pick tunnels from a numbered list.

Tunnel commands (act on named tunnels, or --all / --autostart):
  up [names...] [--all] [--autostart] [--print-cmd]
                              start tunnels in the background
                              (--print-cmd prints the command without running it)
  down [names...] [--all]     stop the selected tunnels
  restart [names...] [--all]  stop then start the selected tunnels
  status [names...]           status table: PID, uptime, UP/DEGRADED/DOWN (default)
  logs <name> [-f]            print a tunnel's log; -f follows it

Config commands:
  add [name]                  add a tunnel (asks for its settings)
  edit [name]                 change a tunnel's settings (Enter keeps a value)
  remove [name] [--yes]       remove a tunnel, stopping it first
  list                        list the tunnels defined in the config
  validate                    check that the config parses and is valid
  init                        write a starter config (never overwrites an existing one)

Autostart (systemd):
  install [--run] [--system]  enable the autostart unit (--run applies it).
                              user unit by default (no sudo; survives reboot only
                              with linger). --system installs a boot service that
                              runs as you and needs no linger (uses sudo).
  uninstall [--run] [--system]  disable/remove the autostart unit

Maintenance:
  update [--check]            update to the latest release (--check only reports)
  version                     print the version
  help                        show this help

Config resolution: --config, then $TUNNELDIR_CONFIG, then ./tunnels.yaml,
then ~/.config/tunneldir/tunnels.yaml

Uninstall tunneldir entirely:
  curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/uninstall.sh | sh
This removes the autostart unit and the binary, and asks before deleting your
config and state. Add "| sh -s -- --purge" to remove config and state too.
`

func main() {
	code := run(os.Args[1:])
	maybeNotifyUpdate(os.Args[1:])
	os.Exit(code)
}

func run(argv []string) int {
	configPath, argv := extractConfigFlag(argv)

	if len(argv) == 0 {
		argv = []string{"status"}
	}
	cmd, rest := argv[0], argv[1:]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "-v", "--version", "version":
		fmt.Printf("tunneldir %s\n", version)
		return 0
	case "update":
		return cmdUpdate(rest)
	case "init":
		return cmdInit(configPath)
	case "add":
		return cmdAdd(configPath, rest)
	case "install":
		run := hasFlag(rest, "--run")
		resolved := paths.ConfigFile(configPath)
		// Load the config (best effort) for both the autostart count and the
		// unattended-key preflight printed after install.
		cfg, _ := config.Load(resolved)
		var installErr error
		if hasFlag(rest, "--system") {
			// System-wide unit: starts at boot as the invoking user, no linger.
			installErr = service.InstallSystem(resolved, run)
		} else {
			autostart := 0
			if cfg != nil {
				autostart = len(cfg.AutostartNames())
			}
			installErr = service.Install(resolved, run, autostart)
		}
		// A boot service has no ssh-agent: warn if an autostart key needs one.
		if installErr == nil && run && cfg != nil {
			if w := status.KeyWarning(cfg); w != "" {
				fmt.Fprint(os.Stderr, "\n"+w)
			}
		}
		return toErr(installErr)
	case "uninstall":
		if hasFlag(rest, "--system") {
			return toErr(service.UninstallSystem(hasFlag(rest, "--run")))
		}
		return toErr(service.Uninstall(hasFlag(rest, "--run")))
	}

	// Everything else needs the config.
	cfg, err := config.Load(paths.ConfigFile(configPath))
	if err != nil {
		// validate should still report the error cleanly.
		if cmd == "validate" {
			fmt.Fprintf(os.Stderr, "invalid: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Convenience form: `tunneldir <name> <command>`.
	if len(rest) >= 1 && isVerb(rest[0]) && !isCommand(cmd) {
		name := cmd
		cmd = rest[0]
		rest = append([]string{name}, rest[1:]...)
	}

	switch cmd {
	case "validate":
		fmt.Printf("OK: %d tunnel(s) defined\n", len(cfg.Tunnels))
		return 0

	case "list":
		return cmdList(cfg)

	case "status":
		names := pickNames(cfg, rest, true)
		if len(stripFlags(rest)) > 0 {
			names = expandNames(cfg, stripFlags(rest))
		}
		if len(cfg.Tunnels) == 0 {
			fmt.Println("no tunnels yet; add one with: tunneldir add")
			return 0
		}
		status.Render(os.Stdout, status.Collect(cfg, names))
		// If the autostart unit is installed but user-lingering is off, the
		// autostart tunnels won't survive a reboot — flag that here.
		if service.UnitInstalled() {
			if w := service.LingerWarning(len(cfg.AutostartNames())); w != "" {
				fmt.Fprint(os.Stderr, "\n"+w)
			}
		}
		// Flag autostart tunnels whose key can't be used unattended.
		if w := status.KeyWarning(cfg); w != "" {
			fmt.Fprint(os.Stderr, "\n"+w)
		}
		return 0

	case "up":
		if hasFlag(rest, "--print-cmd") {
			names := pickNames(cfg, stripFlags(rest), true)
			if len(stripFlags(rest)) > 0 {
				names = expandNames(cfg, stripFlags(rest))
			}
			return cmdPrintCmd(cfg, names)
		}
		names, ok := selectNames(cfg, rest, "up")
		if !ok {
			return 2
		}
		return toErr(manager.Up(cfg, names))

	case "down":
		names, ok := selectNames(cfg, rest, "down")
		if !ok {
			return 2
		}
		return toErr(manager.Down(cfg, names))

	case "restart":
		names, ok := selectNames(cfg, rest, "restart")
		if !ok {
			return 2
		}
		return toErr(manager.Restart(cfg, names))

	case "logs":
		return cmdLogs(cfg, rest)

	case "edit":
		return cmdEdit(cfg, configPath, rest)

	case "remove", "rm":
		return cmdRemove(cfg, configPath, rest)

	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdList(cfg *config.Config) int {
	for _, t := range cfg.Tunnels {
		auto := ""
		if t.Autostart {
			auto = "  [autostart]"
		}
		fmt.Printf("%-20s %-25s %s%s\n", t.Name, t.Target(), tunnel.Summary(t), auto)
	}
	return 0
}

func cmdPrintCmd(cfg *config.Config, names []string) int {
	for _, name := range names {
		t, ok := cfg.Tunnel(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown tunnel %q\n", name)
			continue
		}
		bin, args, autossh, err := tunnel.Command(t, cfg.Defaults)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			continue
		}
		kind := "ssh (fallback)"
		if autossh {
			kind = "autossh"
		}
		fmt.Printf("# %s (%s)\n%s %s\n\n", name, kind, bin, strings.Join(args, " "))
	}
	return 0
}

func cmdLogs(cfg *config.Config, rest []string) int {
	follow := hasFlag(rest, "-f") || hasFlag(rest, "--follow")
	names := stripFlags(rest)
	switch {
	case len(names) == 0 && interactive():
		names = pickInteractively(cfg, "view logs of", true)
		if len(names) == 0 {
			return 2
		}
	case len(names) == 1:
		var ok bool
		if names, ok = resolveNames(cfg, names, "view logs of"); !ok || len(names) != 1 {
			return 2
		}
	default:
		fmt.Fprintln(os.Stderr, "logs requires exactly one tunnel name")
		return 2
	}
	path, err := paths.LogFile(names[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no log for %q (%v)\n", names[0], err)
		return 1
	}
	defer f.Close()
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return 1
	}
	if !follow {
		return 0
	}
	for {
		time.Sleep(300 * time.Millisecond)
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return 1
		}
	}
}

// starterConfig is the commented template written by `tunneldir init`. It has
// no tunnels, so `tunneldir add` starts from a clean list; the commented example
// mirrors tunnels.example.yaml.
const starterConfig = `# Tunnel Director configuration.
# Add tunnels with:  tunneldir add   (or edit this file by hand)

defaults:
  # SSH key used for every tunnel unless overridden per-tunnel. ~ is expanded.
  identity_file: ~/.ssh/id_ed25519
  # Passed as -o KEY=VALUE to every tunnel. These keep the link healthy and make
  # even the plain-ssh fallback exit on a dead connection.
  ssh_options:
    ServerAliveInterval: 30
    ServerAliveCountMax: 3
    ExitOnForwardFailure: "yes"

# Tunnels. Add them with "tunneldir add", or by hand following this example:
#
#   - name: web-staging              # short name used on the command line
#     host: staging.example.com
#     user: deploy                   # optional; default is your ssh user
#     port: 22                       # optional ssh port (default 22)
#     identity_file: ~/.ssh/id_staging  # optional per-tunnel key
#     autostart: true                # start at boot via "tunneldir install"
#     forwards:
#       - local: 8080:localhost:80   # -L : localhost:8080 -> server's :80
#       - dynamic: 1080              # -D : SOCKS proxy on localhost:1080
#       - remote: 9000:localhost:3000  # -R : server's :9000 -> your :3000
tunnels: []
`

// cmdInit writes a starter config to the --config path (if given) or the default
// per-user location, creating parent directories. It never overwrites an
// existing config, so it is safe to run from the installer on every install.
func cmdInit(configPath string) int {
	target := configPath
	if target == "" {
		target = paths.UserConfigFile()
	}
	if _, err := os.Stat(target); err == nil {
		fmt.Printf("config already exists at %s (left untouched)\n", target)
		return 0
	}
	if err := writeStarter(target); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("wrote starter config to %s\n", target)
	fmt.Println("add your first tunnel with: tunneldir add")
	return 0
}

// writeStarter writes the starter config to path, creating its directory.
func writeStarter(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(starterConfig), 0o600)
}

// cmdUpdate handles `tunneldir update [--check]`: --check only reports whether a
// newer release exists, while a bare update downloads and replaces the binary.
func cmdUpdate(rest []string) int {
	if hasFlag(rest, "--check") {
		latest, newer, err := updater.Check(version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update check failed: %v\n", err)
			return 1
		}
		if newer {
			fmt.Printf("a newer version (%s) is available — run: tunneldir update\n", latest)
		} else {
			fmt.Printf("up to date (%s)\n", version)
		}
		return 0
	}
	newVersion, err := updater.SelfUpdate(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	if newVersion == "" {
		fmt.Printf("already on the latest version (%s)\n", version)
	} else {
		fmt.Printf("updated to %s\n", newVersion)
	}
	return 0
}

// maybeNotifyUpdate prints a one-line notice to stderr when a newer release is
// available. It is best-effort and cached (at most one network check per day);
// any error is swallowed so it never disrupts the command that just ran.
func maybeNotifyUpdate(argv []string) {
	if version == "dev" {
		return
	}
	_, rest := extractConfigFlag(argv)
	if len(rest) > 0 {
		switch rest[0] {
		case "-h", "--help", "help", "-v", "--version", "version", "update":
			return
		}
	}
	if latest, ok := updater.CheckCached(version); ok {
		fmt.Fprintf(os.Stderr, "\na newer version (%s) is available — run: tunneldir update\n", latest)
	}
}

// pickNames resolves the set of tunnel names a command should act on, from
// positional names and the --all / --autostart flags. When no selector is given
// and emptyMeansAll is true (status), all tunnels are returned.
func pickNames(cfg *config.Config, rest []string, emptyMeansAll bool) []string {
	if hasFlag(rest, "--all") {
		return cfg.Names()
	}
	if hasFlag(rest, "--autostart") {
		return cfg.AutostartNames()
	}
	names := stripFlags(rest)
	if len(names) == 0 && emptyMeansAll {
		return cfg.Names()
	}
	return names
}

// selectNames resolves the tunnels a mutating command (up/down/restart) acts
// on. --all/--autostart win; typed names may be partial; with no names at all
// an interactive terminal gets a picker, while a script gets an error rather
// than silently doing nothing.
func selectNames(cfg *config.Config, rest []string, verb string) ([]string, bool) {
	if hasFlag(rest, "--all") || hasFlag(rest, "--autostart") {
		return pickNames(cfg, rest, false), true
	}
	if names := stripFlags(rest); len(names) > 0 {
		return resolveNames(cfg, names, verb)
	}
	if interactive() {
		// Nothing picked (cancelled, or nothing to act on) is not an error.
		return pickInteractively(cfg, verb, false), true
	}
	fmt.Fprintln(os.Stderr, "no tunnels selected; pass tunnel name(s), --all, or --autostart")
	return nil, false
}

func isVerb(s string) bool {
	switch s {
	case "up", "down", "restart", "status", "logs", "edit", "remove", "rm":
		return true
	}
	return false
}

// isCommand reports whether s is a top-level command, so the convenience form
// `tunneldir <name> <verb>` never mistakes a command for a tunnel name.
func isCommand(s string) bool {
	switch s {
	case "validate", "list", "init", "add", "install", "uninstall", "update", "version", "help":
		return true
	}
	return isVerb(s)
}

// extractConfigFlag pulls a leading/standalone --config value out of argv.
func extractConfigFlag(argv []string) (string, []string) {
	out := make([]string, 0, len(argv))
	configPath := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--config" && i+1 < len(argv):
			configPath = argv[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
		default:
			out = append(out, a)
		}
	}
	return configPath, out
}

// hasFlag reports whether args contains flag. A long flag ("--all") is also
// matched in its single-dash form ("-all"), since that is an easy mistake to
// make and silently doing nothing would be worse.
func hasFlag(args []string, flag string) bool {
	alt := ""
	if strings.HasPrefix(flag, "--") {
		alt = flag[1:]
	}
	for _, a := range args {
		if a == flag || (alt != "" && a == alt) {
			return true
		}
	}
	return false
}

func stripFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func toErr(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
