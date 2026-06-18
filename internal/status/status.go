// Package status reports the live state of configured tunnels and renders it as
// a docker-ps-style table.
package status

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"tunneldir/internal/config"
	"tunneldir/internal/manager"
	"tunneldir/internal/paths"
	"tunneldir/internal/sshkey"
	"tunneldir/internal/tunnel"
)

// State is the resolved status of a single tunnel.
type State struct {
	Name      string
	Target    string
	Forwards  string
	Type      string // autossh | ssh — what is running (or would run, if down)
	Autostart bool
	PID       int
	Uptime    time.Duration
	Status    string // UP | DEGRADED | DOWN
	Reason    string // for DEGRADED: short cause pulled from the log tail
}

const (
	StatusUp       = "UP"
	StatusDegraded = "DEGRADED"
	StatusDown     = "DOWN"
)

// Collect computes the state of every named tunnel.
func Collect(cfg *config.Config, names []string) []State {
	states := make([]State, 0, len(names))
	for _, name := range names {
		t, ok := cfg.Tunnel(name)
		if !ok {
			continue
		}
		states = append(states, collectOne(t))
	}
	return states
}

func collectOne(t config.Tunnel) State {
	s := State{
		Name:      t.Name,
		Target:    t.Target(),
		Forwards:  tunnel.Summary(t),
		Autostart: t.Autostart,
		Status:    StatusDown,
	}
	pid, alive := manager.Status(t.Name)
	if !alive {
		// Not running: show what it would launch as.
		s.Type = prospectiveType()
		return s
	}
	s.PID = pid
	s.Uptime = uptime(pid)
	s.Type = processType(pid)

	// Process is alive; probe local listen ports to distinguish UP/DEGRADED.
	s.Status = StatusUp
	fwds, err := tunnel.ParseForwards(t)
	if err != nil {
		return s
	}
	for _, f := range fwds {
		addr, probeable := f.ProbeAddr()
		if !probeable {
			continue
		}
		if !portListening(addr) {
			s.Status = StatusDegraded
			break
		}
	}
	if s.Status == StatusDegraded {
		// Process is up but a forward isn't working — surface why from the log
		// so the user doesn't have to go digging (auth, refused, DNS, ...).
		s.Reason = degradedReason(t.Name)
	}
	return s
}

// degradedReason scans the tail of a tunnel's log for a recognisable failure
// and returns a short human cause, or "" if none is found.
func degradedReason(name string) string {
	path, err := paths.LogFile(name)
	if err != nil {
		return ""
	}
	lines := strings.Split(tailString(path, 8192), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if r := classifyLogLine(lines[i]); r != "" {
			return r
		}
	}
	return ""
}

// classifyLogLine maps a known ssh/autossh error line to a short cause.
func classifyLogLine(line string) string {
	switch {
	case strings.Contains(line, "Permission denied"),
		strings.Contains(line, "Too many authentication failures"):
		return "SSH auth rejected (Permission denied) — check the key; a passphrase-protected key can't be used unattended"
	case strings.Contains(line, "Host key verification failed"),
		strings.Contains(line, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		return "host key verification failed — update known_hosts on the server-side account running the tunnel"
	case strings.Contains(line, "Connection refused"):
		return "connection refused by the server"
	case strings.Contains(line, "Could not resolve hostname"),
		strings.Contains(line, "Name or service not known"),
		strings.Contains(line, "nodename nor servname"):
		return "host name could not be resolved (DNS)"
	case strings.Contains(line, "Connection timed out"),
		strings.Contains(line, "not responding"),
		strings.Contains(line, "Operation timed out"):
		return "connection timed out — host unreachable or firewalled"
	case strings.Contains(line, "Connection reset by"),
		strings.Contains(line, "kex_exchange_identification"):
		return "connection reset by the server during handshake"
	case strings.Contains(line, "remote port forwarding failed"),
		strings.Contains(line, "bind: Address already in use"):
		return "a forwarded port is already in use"
	default:
		return ""
	}
}

// tailString returns up to the last n bytes of the file at path (empty on error).
func tailString(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	off := info.Size() - n
	if off < 0 {
		off = 0
	}
	buf := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) > 0 {
		// ReadAt returns io.EOF when it fills the buffer exactly; tolerate short
		// reads by trusting whatever was read into buf.
	}
	return string(buf)
}

// KeyWarning returns a warning naming any autostart tunnels whose SSH key an
// unattended boot service can't use — passphrase-protected (no agent at boot to
// unlock it) or missing. It returns "" when every autostart key is usable, so
// callers can print it unconditionally. This is the auth analogue of the linger
// check: it turns a silent "Permission denied" at boot into an up-front warning.
func KeyWarning(cfg *config.Config) string {
	var encrypted, missing []string
	for _, name := range cfg.AutostartNames() {
		t, ok := cfg.Tunnel(name)
		if !ok || t.IdentityFile == "" {
			continue // no explicit key: left to ssh defaults/agent, can't judge
		}
		switch sshkey.Inspect(t.IdentityFile) {
		case sshkey.Encrypted:
			encrypted = append(encrypted, fmt.Sprintf("%s (%s)", name, t.IdentityFile))
		case sshkey.Missing:
			missing = append(missing, fmt.Sprintf("%s (%s)", name, t.IdentityFile))
		}
	}
	if len(encrypted) == 0 && len(missing) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("warning: autostart tunnel(s) have an SSH key an unattended boot service can't use\n")
	b.WriteString("(there is no ssh-agent at boot to unlock a passphrase):\n")
	for _, e := range encrypted {
		fmt.Fprintf(&b, "    passphrase-protected: %s\n", e)
	}
	for _, m := range missing {
		fmt.Fprintf(&b, "    key not found:        %s\n", m)
	}
	b.WriteString("Use a dedicated passphrase-less key for these, e.g.:\n")
	b.WriteString("    ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_tunneldir\n")
	b.WriteString("then set the tunnel's identity_file to it and authorise the public key on the server.\n")
	return b.String()
}

// processType reports the actual program backing a running tunnel by reading
// /proc/<pid>/comm (e.g. "autossh" or "ssh"), falling back to the prospective
// type if that can't be read.
func processType(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return prospectiveType()
	}
	if name := strings.TrimSpace(string(data)); name != "" {
		return name
	}
	return prospectiveType()
}

// prospectiveType reports which program a tunnel would launch as right now.
func prospectiveType() string {
	if tunnel.HasAutossh() {
		return "autossh"
	}
	return "ssh"
}

// portListening reports whether something accepts a TCP connection at addr.
func portListening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Render writes the status table to w.
func Render(w *os.File, states []State) {
	color := useColor(w)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTARGET\tFORWARDS\tTYPE\tAUTOSTART\tPID\tUPTIME\tSTATUS")
	for _, s := range states {
		pid := "-"
		up := "-"
		if s.PID > 0 {
			pid = strconv.Itoa(s.PID)
		}
		if s.Uptime > 0 {
			up = humanDuration(s.Uptime)
		}
		auto := "no"
		if s.Autostart {
			auto = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, s.Target, s.Forwards, s.Type, auto, pid, up, colorize(s.Status, color))
	}
	_ = tw.Flush()

	// Explain degraded tunnels beneath the table so the cause is visible without
	// opening the log.
	for _, s := range states {
		if s.Status == StatusDegraded && s.Reason != "" {
			fmt.Fprintf(w, "  ! %s: %s\n", s.Name, s.Reason)
		}
	}
}

func colorize(status string, enabled bool) string {
	if !enabled {
		return status
	}
	switch status {
	case StatusUp:
		return "\033[32m" + status + "\033[0m" // green
	case StatusDegraded:
		return "\033[33m" + status + "\033[0m" // yellow
	case StatusDown:
		return "\033[31m" + status + "\033[0m" // red
	}
	return status
}

func useColor(w *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := w.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// uptime derives a process's age from /proc/<pid>/stat (Linux), falling back to
// the pidfile mtime if that is unavailable.
func uptime(pid int) time.Duration {
	if d, ok := procUptime(pid); ok {
		return d
	}
	return 0
}

// userHZ is the kernel clock-tick rate. It is 100 on effectively all modern
// Linux kernels; we avoid cgo (sysconf) and assume that.
const userHZ = 100

func procUptime(pid int) (time.Duration, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	line := string(data)
	// comm (field 2) is parenthesised and may contain spaces; fields after the
	// final ')' are space-separated, with state as the first.
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[rparen+2:])
	// starttime is field 22 overall => index 19 of the post-comm fields.
	if len(fields) < 20 {
		return 0, false
	}
	startTicks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	bootTime, ok := bootTimeUnix()
	if !ok {
		return 0, false
	}
	startUnix := bootTime + startTicks/userHZ
	age := time.Now().Unix() - startUnix
	if age < 0 {
		age = 0
	}
	return time.Duration(age) * time.Second, true
}

func bootTimeUnix() (int64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseInt(strings.TrimSpace(line[len("btime "):]), 10, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}
