// Package manager starts, stops and tracks tunnel processes in the background.
package manager

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tunneldir/internal/config"
	"tunneldir/internal/paths"
	"tunneldir/internal/tunnel"
)

// Up starts the named tunnels in the background. Tunnels already running are
// left untouched. It returns the first error encountered but attempts all.
func Up(cfg *config.Config, names []string) error {
	var firstErr error
	for _, name := range names {
		t, ok := cfg.Tunnel(name)
		if !ok {
			err := fmt.Errorf("unknown tunnel %q", name)
			fmt.Fprintln(os.Stderr, err)
			firstErr = orFirst(firstErr, err)
			continue
		}
		if pid, alive := Status(name); alive {
			fmt.Printf("%-20s already running (pid %d)\n", name, pid)
			continue
		}
		pid, err := launch(t, cfg.Defaults)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%-20s failed: %v\n", name, err)
			firstErr = orFirst(firstErr, err)
			continue
		}
		fmt.Printf("%-20s started (pid %d)\n", name, pid)
	}
	return firstErr
}

// Down stops the named tunnels, killing each process group and removing its
// pidfile. Tunnels that aren't running are reported but not treated as errors.
func Down(cfg *config.Config, names []string) error {
	var firstErr error
	for _, name := range names {
		if _, ok := cfg.Tunnel(name); !ok {
			fmt.Fprintf(os.Stderr, "%-20s unknown tunnel\n", name)
			firstErr = orFirst(firstErr, fmt.Errorf("unknown tunnel %q", name))
			continue
		}
		pid, alive := Status(name)
		if !alive {
			fmt.Printf("%-20s not running\n", name)
			clearPid(name)
			continue
		}
		if err := stop(pid); err != nil {
			fmt.Fprintf(os.Stderr, "%-20s stop failed: %v\n", name, err)
			firstErr = orFirst(firstErr, err)
			continue
		}
		clearPid(name)
		fmt.Printf("%-20s stopped\n", name)
	}
	return firstErr
}

// Restart stops then starts the named tunnels.
func Restart(cfg *config.Config, names []string) error {
	err := Down(cfg, names)
	if upErr := Up(cfg, names); upErr != nil {
		err = orFirst(err, upErr)
	}
	return err
}

// Status returns the tracked pid for a tunnel and whether that process is alive.
// "Alive" requires not just that *some* process holds the pid, but that it still
// looks like one of our ssh/autossh processes — otherwise a recycled pid (the
// tunnel died and the OS handed its pid to something unrelated) would be
// mistaken for a running tunnel and, worse, killed by `down`.
func Status(name string) (pid int, alive bool) {
	pid, err := readPid(name)
	if err != nil {
		return 0, false
	}
	return pid, isAlive(pid) && looksLikeTunnel(pid)
}

// looksLikeTunnel reports whether pid is one of our ssh/autossh processes, by
// reading /proc/<pid>/comm. When the command name can't be determined (e.g. a
// non-Linux host with no /proc) it returns true, so behaviour matches platforms
// where this guard isn't available.
func looksLikeTunnel(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return true
	}
	switch strings.TrimSpace(string(data)) {
	case "ssh", "autossh":
		return true
	default:
		return false
	}
}

// launch starts a tunnel detached in its own session, with output to its log
// file, and records the pid. The returned pid is the session/group leader.
func launch(t config.Tunnel, defaults config.Defaults) (int, error) {
	bin, args, _, err := tunnel.Command(t, defaults)
	if err != nil {
		return 0, err
	}

	logPath, err := paths.LogFile(t.Name)
	if err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()

	fmt.Fprintf(logFile, "\n=== %s: starting %s %s ===\n",
		time.Now().Format(time.RFC3339), bin, strings.Join(args, " "))

	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach into a new session so the process survives our exit and so the
	// whole group (autossh + its ssh children) can be signalled together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// autossh needs AUTOSSH_GATETIME=0 so it keeps retrying even if the very
	// first connection fails (e.g. server not yet reachable at boot).
	cmd.Env = append(os.Environ(), "AUTOSSH_GATETIME=0")

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := writePid(t.Name, pid); err != nil {
		// Best effort: don't leave an untracked process running.
		_ = stop(pid)
		return 0, err
	}
	// Release so the Go runtime doesn't reap/track the detached child.
	_ = cmd.Process.Release()
	return pid, nil
}

// stop terminates a process group gracefully (SIGTERM), escalating to SIGKILL
// if it is still alive after a short grace period.
func stop(pid int) error {
	// Negative pid signals the whole process group (Setsid => pgid == pid).
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		if !isAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	if isAlive(pid) {
		return fmt.Errorf("process %d would not die", pid)
	}
	return nil
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 performs error checking without sending a signal.
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM // exists but owned by another user
}

func readPid(name string) (int, error) {
	path, err := paths.PidFile(name)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("corrupt pidfile %s", path)
	}
	return pid, nil
}

func writePid(name string, pid int) error {
	path, err := paths.PidFile(name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func clearPid(name string) {
	if path, err := paths.PidFile(name); err == nil {
		_ = os.Remove(path)
	}
}

func orFirst(first, next error) error {
	if first == nil {
		return next
	}
	return first
}
