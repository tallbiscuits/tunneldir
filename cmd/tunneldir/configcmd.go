package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"tunneldir/internal/config"
	"tunneldir/internal/manager"
	"tunneldir/internal/paths"
	"tunneldir/internal/service"
	"tunneldir/internal/tunnel"
)

// cmdAdd asks for a new tunnel's settings, appends it to the config and offers
// to start it. A missing config is created from the starter template first.
func cmdAdd(configPath string, rest []string) int {
	if !requireTerminal("add") {
		return 2
	}
	path := paths.ConfigFile(configPath)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writeStarter(path); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var draft config.Tunnel
	if names := stripFlags(rest); len(names) > 0 {
		draft.Name = names[0]
	}
	t, ok := tunnelForm(cfg, draft, "")
	if !ok {
		return 0
	}
	if err := config.AddTunnel(path, t); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("added %s to %s\n", t.Name, path)
	autostartHint(t)

	if confirm("Start it now?", true) {
		return reloadAnd(path, func(cfg *config.Config) error { return manager.Up(cfg, []string{t.Name}) })
	}
	return 0
}

// cmdEdit asks for a tunnel's settings pre-filled with the current values
// (Enter keeps each one) and offers to restart it if it is running.
func cmdEdit(cfg *config.Config, configPath string, rest []string) int {
	if !requireTerminal("edit") {
		return 2
	}
	name, ok := pickOne(cfg, rest, "edit")
	if !ok {
		return 2
	}
	path := paths.ConfigFile(configPath)
	cur, err := config.RawTunnel(path, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	t, ok := tunnelForm(cfg, cur, name)
	if !ok {
		return 0
	}
	if err := config.UpdateTunnel(path, name, t); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("saved %s in %s\n", t.Name, path)
	if t.Autostart && !cur.Autostart {
		autostartHint(t)
	}

	if _, running := manager.Status(name); running && confirm("It is running. Restart it to apply the changes?", true) {
		// Stop under the old config: a rename changes the pidfile name.
		if err := manager.Down(cfg, []string{name}); err != nil {
			return 1
		}
		return reloadAnd(path, func(cfg *config.Config) error { return manager.Up(cfg, []string{t.Name}) })
	}
	return 0
}

// cmdRemove deletes a tunnel from the config after confirmation, stopping it
// first so no untracked process is left behind.
func cmdRemove(cfg *config.Config, configPath string, rest []string) int {
	name, ok := pickOne(cfg, rest, "remove")
	if !ok {
		return 2
	}
	if !hasFlag(rest, "--yes") && !hasFlag(rest, "-y") {
		if !interactive() {
			fmt.Fprintln(os.Stderr, "remove needs confirmation; pass --yes when not running in a terminal")
			return 2
		}
		if !confirm(fmt.Sprintf("Remove %s from the config?", name), false) {
			return 0
		}
	}
	if _, running := manager.Status(name); running {
		if err := manager.Down(cfg, []string{name}); err != nil {
			return 1
		}
	}
	path := paths.ConfigFile(configPath)
	if err := config.RemoveTunnel(path, name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("removed %s from %s\n", name, path)
	return 0
}

// tunnelForm asks for every tunnel setting, offering cur's values as defaults.
// origName is the tunnel being edited ("" when adding), so it may keep its own
// name. It returns false if the user declines to save.
func tunnelForm(cfg *config.Config, cur config.Tunnel, origName string) (config.Tunnel, bool) {
	editing := origName != ""
	if editing {
		fmt.Println("Press Enter to keep a value; type - to clear an optional one.")
	}
	t := cur

	t.Name = askValid("Name (short, e.g. web-staging)", cur.Name, false, func(v string) error {
		if strings.ContainsAny(v, " /\\\t") {
			return fmt.Errorf("no spaces or slashes")
		}
		if _, taken := cfg.Tunnel(v); taken && v != origName {
			return fmt.Errorf("a tunnel called %q already exists", v)
		}
		return nil
	})

	t.Host = askValid("Server (host name or IP; user@host also works)", joinUserHost(cur), false, nil)
	if user, host, ok := strings.Cut(t.Host, "@"); ok {
		t.User, t.Host = user, host
	} else {
		t.User = askValid("SSH user (blank = your ssh default)", cur.User, true, nil)
	}

	port := askValid("SSH port", strconv.Itoa(cur.SSHPort()), false, func(v string) error {
		if p, err := strconv.Atoi(v); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("enter a port number between 1 and 65535")
		}
		return nil
	})
	t.Port, _ = strconv.Atoi(port)

	keyHint := "SSH key (blank = your ssh default)"
	if cfg.Defaults.IdentityFile != "" {
		keyHint = "SSH key (blank = " + cfg.Defaults.IdentityFile + ")"
	}
	t.IdentityFile = askValid(keyHint, cur.IdentityFile, true, nil)

	fmt.Println(`Forwards. Enter one, or several separated by commas:
  L 8080:localhost:80    your port 8080 reaches port 80 on the server
  R 9000:localhost:3000  the server's port 9000 reaches your port 3000
  D 1080                 a SOCKS proxy on your port 1080
  two at once:           L 8080:localhost:80, D 1080`)
	fwd := askValid("Forwards", formatForwards(cur.Forwards), false, func(v string) error {
		fs, err := parseForwardList(v)
		if err != nil {
			return err
		}
		_, err = tunnel.ParseForwards(config.Tunnel{Forwards: fs})
		return err
	})
	t.Forwards, _ = parseForwardList(fwd)

	t.Autostart = confirm("Start automatically at boot?", cur.Autostart)

	fmt.Printf("\n  %s  %s  %s", t.Name, t.Target(), formatForwards(t.Forwards))
	if t.Autostart {
		fmt.Print("  (autostart)")
	}
	fmt.Println()
	if !confirm("Save?", true) {
		fmt.Println("not saved")
		return t, false
	}
	return t, true
}

// askValid prompts until the answer passes check. The default is used on Enter;
// for an optional field, "-" clears it. A required field can't be left empty.
func askValid(label, def string, optional bool, check func(string) error) string {
	for {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		v := readLine()
		switch {
		case v == "":
			v = def
		case v == "-" && optional:
			v = ""
		}
		if v == "" && !optional {
			fmt.Println("  required")
			continue
		}
		if v != "" && check != nil {
			if err := check(v); err != nil {
				fmt.Printf("  %v\n", err)
				continue
			}
		}
		return v
	}
}

// confirm asks a yes/no question; Enter picks def.
func confirm(q string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Printf("%s %s ", q, hint)
		switch strings.ToLower(readLine()) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

// parseForwardList parses "L 8080:localhost:80, D 1080" into forwards. The
// kind may also be written as local/remote/dynamic, or joined with a colon
// ("L:8080:localhost:80").
func parseForwardList(s string) ([]config.Forward, error) {
	var out []config.Forward
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kind, spec, ok := strings.Cut(item, " ")
		if !ok {
			kind, spec, ok = strings.Cut(item, ":")
		}
		spec = strings.TrimSpace(spec)
		if !ok || spec == "" {
			return nil, fmt.Errorf("%q: start with L, R or D, e.g. L 8080:localhost:80", item)
		}
		switch strings.ToLower(kind) {
		case "l", "local":
			out = append(out, config.Forward{Local: spec})
		case "r", "remote":
			out = append(out, config.Forward{Remote: spec})
		case "d", "dynamic":
			out = append(out, config.Forward{Dynamic: spec})
		default:
			return nil, fmt.Errorf("%q: start with L, R or D, e.g. L 8080:localhost:80", item)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one forward is required")
	}
	return out, nil
}

// formatForwards is the inverse of parseForwardList.
func formatForwards(fs []config.Forward) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		switch {
		case f.Local != "":
			parts = append(parts, "L "+f.Local)
		case f.Remote != "":
			parts = append(parts, "R "+f.Remote)
		case f.Dynamic != "":
			parts = append(parts, "D "+f.Dynamic)
		}
	}
	return strings.Join(parts, ", ")
}

func joinUserHost(t config.Tunnel) string {
	if t.User != "" && t.Host != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}

// pickOne resolves the single tunnel edit/remove act on, from a (possibly
// partial) name or, with no name in a terminal, the picker.
func pickOne(cfg *config.Config, rest []string, verb string) (string, bool) {
	names := stripFlags(rest)
	switch {
	case len(names) == 0 && interactive():
		names = pickInteractively(cfg, verb, true)
	case len(names) == 1:
		names, _ = resolveNames(cfg, names, verb)
	default:
		fmt.Fprintf(os.Stderr, "%s takes one tunnel name\n", verb)
		return "", false
	}
	if len(names) != 1 {
		return "", false
	}
	return names[0], true
}

func requireTerminal(verb string) bool {
	if interactive() {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s asks questions, so it needs a terminal; edit the config file directly instead\n", verb)
	return false
}

// reloadAnd reloads the just-saved config and runs fn with it.
func reloadAnd(path string, fn func(*config.Config) error) int {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return toErr(fn(cfg))
}

// autostartHint points at `install` when a tunnel is flagged autostart but no
// boot unit exists yet to start it.
func autostartHint(t config.Tunnel) {
	if t.Autostart && !service.UnitInstalled() && !service.SystemUnitInstalled() {
		fmt.Println("to start it at boot, install the service once: tunneldir install --system --run")
	}
}
