// Command tunneldir manages SSH tunnels defined in a YAML file: starting them
// in the background (autossh, falling back to ssh), showing a docker-ps-style
// status view, and optionally autostarting flagged tunnels via systemd.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tunneldir/internal/config"
	"tunneldir/internal/manager"
	"tunneldir/internal/paths"
	"tunneldir/internal/service"
	"tunneldir/internal/status"
	"tunneldir/internal/tunnel"
)

const usage = `tunneldir — manage SSH tunnels from a YAML file

Usage:
  tunneldir [--config PATH] <command> [names...]
  tunneldir <name> <command>            (convenience form)

Commands:
  up [names...] [--all] [--autostart] [--print-cmd]
                          start tunnels in the background
  down [names...] [--all] stop tunnels
  restart [names...] [--all]
                          restart tunnels
  status [names...]       show a docker-ps-style status table (default)
  logs <name> [-f]        show or follow a tunnel's log
  list                    list tunnels defined in the config
  validate                validate the config file
  install [--run]         install/enable the systemd autostart unit
  uninstall [--run]       disable/remove the systemd autostart unit

Config resolution: --config, then $TUNNELDIR_CONFIG, then ./tunnels.yaml,
then ~/.config/tunneldir/tunnels.yaml
`

func main() {
	os.Exit(run(os.Args[1:]))
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
	case "install":
		return toErr(service.Install(paths.ConfigFile(configPath), hasFlag(rest, "--run")))
	case "uninstall":
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
	if _, isTunnel := cfg.Tunnel(cmd); isTunnel && len(rest) >= 1 && isVerb(rest[0]) {
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
		status.Render(os.Stdout, status.Collect(cfg, names))
		return 0

	case "up":
		if hasFlag(rest, "--print-cmd") {
			return cmdPrintCmd(cfg, pickNames(cfg, stripFlags(rest), true))
		}
		names := pickNames(cfg, rest, false)
		if !requireSelection(names) {
			return 2
		}
		return toErr(manager.Up(cfg, names))

	case "down":
		names := pickNames(cfg, rest, false)
		if !requireSelection(names) {
			return 2
		}
		return toErr(manager.Down(cfg, names))

	case "restart":
		names := pickNames(cfg, rest, false)
		if !requireSelection(names) {
			return 2
		}
		return toErr(manager.Restart(cfg, names))

	case "logs":
		return cmdLogs(rest)

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

func cmdLogs(rest []string) int {
	follow := hasFlag(rest, "-f") || hasFlag(rest, "--follow")
	names := stripFlags(rest)
	if len(names) != 1 {
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

// requireSelection guards mutating commands against silently doing nothing when
// no tunnel was selected (e.g. a bare `up` with no names or selector).
func requireSelection(names []string) bool {
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "no tunnels selected; pass tunnel name(s), --all, or --autostart")
		return false
	}
	return true
}

func isVerb(s string) bool {
	switch s {
	case "up", "down", "restart", "status", "logs":
		return true
	}
	return false
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
