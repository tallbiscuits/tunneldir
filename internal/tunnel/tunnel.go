// Package tunnel turns a config.Tunnel into a concrete ssh/autossh command and
// exposes the listen endpoints needed for status probing.
package tunnel

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"tunneldir/internal/config"
)

// Kind is the forward direction.
type Kind int

const (
	KindLocal   Kind = iota // -L
	KindRemote              // -R
	KindDynamic             // -D
)

// Forward is a parsed port forward.
type Forward struct {
	Kind Kind
	Arg  string // the ssh argument, e.g. "8080:localhost:80"
	Bind string // listen bind address (default "" => localhost)
	Port int    // local listen port (for -L and -D); 0 for -R
}

// ProbeAddr returns the 127.0.0.1:port address to probe for liveness, and true
// if this forward exposes a locally-probeable listen port (-L and -D do; -R
// does not).
func (f Forward) ProbeAddr() (string, bool) {
	if f.Kind == KindRemote || f.Port == 0 {
		return "", false
	}
	host := f.Bind
	if host == "" || host == "*" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, f.Port), true
}

func (f Forward) flag() string {
	switch f.Kind {
	case KindLocal:
		return "-L"
	case KindRemote:
		return "-R"
	default:
		return "-D"
	}
}

// Summary is a compact one-line description of a tunnel's forwards, e.g.
// "L:8080 L:5432 D:1080 remote".
func Summary(t config.Tunnel) string {
	fwds, err := ParseForwards(t)
	if err != nil {
		return "?"
	}
	parts := make([]string, 0, len(fwds))
	for _, f := range fwds {
		switch f.Kind {
		case KindLocal:
			parts = append(parts, "L:"+strconv.Itoa(f.Port))
		case KindDynamic:
			parts = append(parts, "D:"+strconv.Itoa(f.Port))
		case KindRemote:
			parts = append(parts, "remote")
		}
	}
	return strings.Join(parts, " ")
}

// ParseForwards parses all of a tunnel's forward specs.
func ParseForwards(t config.Tunnel) ([]Forward, error) {
	out := make([]Forward, 0, len(t.Forwards))
	for _, raw := range t.Forwards {
		f, err := parseForward(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func parseForward(raw config.Forward) (Forward, error) {
	switch {
	case raw.Local != "":
		return parseHostForward(KindLocal, raw.Local)
	case raw.Remote != "":
		return parseHostForward(KindRemote, raw.Remote)
	case raw.Dynamic != "":
		return parseDynamic(raw.Dynamic)
	default:
		return Forward{}, fmt.Errorf("empty forward")
	}
}

// parseHostForward handles -L / -R specs: "[bind:]listen:host:hostport".
func parseHostForward(kind Kind, spec string) (Forward, error) {
	parts := strings.Split(spec, ":")
	var bind, listenPort string
	switch len(parts) {
	case 3: // listen:host:hostport
		listenPort = parts[0]
	case 4: // bind:listen:host:hostport
		bind = parts[0]
		listenPort = parts[1]
	default:
		return Forward{}, fmt.Errorf("invalid forward %q: expected [bind:]listen:host:port", spec)
	}
	port := 0
	// For -R the "listen" port lives on the remote side and is not locally
	// probeable, so we leave Port=0; for -L it's our local listen port.
	if kind == KindLocal {
		p, err := strconv.Atoi(listenPort)
		if err != nil || p < 1 || p > 65535 {
			return Forward{}, fmt.Errorf("invalid listen port in %q", spec)
		}
		port = p
	}
	return Forward{Kind: kind, Arg: spec, Bind: bind, Port: port}, nil
}

// parseDynamic handles -D specs: "[bind:]port".
func parseDynamic(spec string) (Forward, error) {
	parts := strings.Split(spec, ":")
	var bind, portStr string
	switch len(parts) {
	case 1:
		portStr = parts[0]
	case 2:
		bind, portStr = parts[0], parts[1]
	default:
		return Forward{}, fmt.Errorf("invalid dynamic forward %q: expected [bind:]port", spec)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return Forward{}, fmt.Errorf("invalid dynamic port in %q", spec)
	}
	return Forward{Kind: KindDynamic, Arg: spec, Bind: bind, Port: p}, nil
}

// HasAutossh reports whether the autossh binary is on PATH.
func HasAutossh() bool {
	_, err := exec.LookPath("autossh")
	return err == nil
}

// lookPathOr resolves name to its absolute path via PATH, so the binary we
// actually exec is fixed at launch time (a later PATH change can't swap it) and
// is shown explicitly by --print-cmd. If the lookup fails (binary not installed
// here, e.g. when only printing the command) it falls back to the bare name.
func lookPathOr(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// Command builds the full command for a tunnel: the binary plus its arguments,
// and whether autossh (vs. plain ssh fallback) is being used.
func Command(t config.Tunnel, defaults config.Defaults) (bin string, args []string, autossh bool, err error) {
	fwds, err := ParseForwards(t)
	if err != nil {
		return "", nil, false, err
	}

	autossh = HasAutossh()
	if autossh {
		bin = lookPathOr("autossh")
		// -M 0 disables autossh's own monitoring port; rely on ServerAlive.
		args = append(args, "-M", "0")
	} else {
		bin = lookPathOr("ssh")
	}

	// -N: no remote command, -T: no pty. Background is handled by us, not -f,
	// so we keep the child in the foreground of its own session for clean
	// pid tracking and log capture.
	args = append(args, "-N", "-T")

	// SSH options: merge defaults, ensuring ServerAlive-based liveness exists so
	// the plain-ssh fallback also exits on a dead link.
	opts := map[string]string{
		"ServerAliveInterval":  "30",
		"ServerAliveCountMax":  "3",
		"ExitOnForwardFailure": "yes",
	}
	for k, v := range defaults.SSHOptions {
		opts[k] = v
	}
	for _, k := range sortedKeys(opts) {
		args = append(args, "-o", fmt.Sprintf("%s=%s", k, opts[k]))
	}

	if t.IdentityFile != "" {
		args = append(args, "-i", t.IdentityFile)
	}
	if p := t.SSHPort(); p != 22 {
		args = append(args, "-p", strconv.Itoa(p))
	}
	for _, f := range fwds {
		args = append(args, f.flag(), f.Arg)
	}
	args = append(args, destination(t))
	return bin, args, autossh, nil
}

// destination is the ssh connection target (user@host); the port is passed
// separately via -p, so it is not appended here.
func destination(t config.Tunnel) string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
