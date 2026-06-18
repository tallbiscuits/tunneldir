// Package service generates and installs the systemd unit that brings up
// autostart-flagged tunnels at boot. It supports two flavours:
//
//   - a per-user unit (no sudo), which only survives a reboot when the user has
//     systemd lingering enabled — otherwise it is session-scoped (up on login,
//     gone on logout/reboot);
//   - a system-wide unit (`--system`, needs sudo) running as the invoking user,
//     which starts at boot independently of any login session and needs no
//     linger. This is the robust choice for an unattended server.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tunneldir/internal/paths"
)

// systemUnitPath is the fixed location of the system-wide unit (root-owned).
const systemUnitPath = "/etc/systemd/system/tunneldir.service"

const unitTemplate = `[Unit]
Description=Tunnel Director (autostart tunnels)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
# Pin a known-good PATH so ssh/autossh are resolved from trusted locations
# rather than whatever the login environment happens to provide.
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=%s up --autostart
ExecStop=%s down --all
WorkingDirectory=%%h

[Install]
WantedBy=default.target
`

// systemUnitTemplate is the system-wide unit. Unlike the user unit it pins the
// user it runs as (so it works with no login session) and its HOME/working
// directory (so SSH keys and tunneldir's state dir resolve exactly as they do
// for a manual run). It is wanted by multi-user.target, i.e. ordinary boot.
const systemUnitTemplate = `[Unit]
Description=Tunnel Director (autostart tunnels)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
User=%s
# Pin HOME so SSH keys and tunneldir's state dir (pidfiles, logs) resolve to the
# invoking user's home, exactly as for a manual run.
Environment=%s
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=%s up --autostart
ExecStop=%s down --all
WorkingDirectory=%s

[Install]
WantedBy=multi-user.target
`

// UnitText renders the per-user unit. binPath is the absolute tunneldir binary
// and configPath is the resolved config the service should use (pinned with
// --config so the service finds the same file regardless of working directory).
// Both are systemd-quoted so paths containing spaces work and embedded quotes
// can't break out of the Exec line.
func UnitText(binPath, configPath string) string {
	invocation := invocation(binPath, configPath)
	return fmt.Sprintf(unitTemplate, invocation, invocation)
}

// SystemUnitText renders the system-wide unit for user (User=) with home pinned
// as HOME and the working directory. user is assumed newline-free (callers
// guard); paths are systemd-quoted.
func SystemUnitText(binPath, configPath, user, home string) string {
	inv := invocation(binPath, configPath)
	homeEnv := systemdQuote("HOME=" + home)
	return fmt.Sprintf(systemUnitTemplate, user, homeEnv, inv, inv, systemdQuote(home))
}

// invocation builds the quoted `<bin> [--config <cfg>]` prefix shared by both
// the ExecStart and ExecStop lines.
func invocation(binPath, configPath string) string {
	inv := systemdQuote(binPath)
	if configPath != "" {
		inv += " --config " + systemdQuote(configPath)
	}
	return inv
}

// systemdQuote wraps s in double quotes, escaping backslashes and quotes, as
// understood by systemd's Exec-line parser.
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// resolvePaths returns the absolute binary and config paths, refusing any that
// contain a newline (which would let it inject extra directives into the
// line-based unit file regardless of quoting).
func resolvePaths(configPath string) (binPath, cfgPath string, err error) {
	binPath, err = os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("locating own binary: %w", err)
	}
	binPath, _ = filepath.Abs(binPath)
	cfgPath, _ = filepath.Abs(configPath)
	if strings.ContainsAny(binPath, "\n\r") || strings.ContainsAny(cfgPath, "\n\r") {
		return "", "", fmt.Errorf("refusing to write unit: binary or config path contains a newline")
	}
	return binPath, cfgPath, nil
}

// Install writes the per-user unit and enables it. When run is false it only
// prints what it would do. configPath is the resolved config path to pin into
// the unit. autostartCount is the number of autostart-flagged tunnels, used to
// explain the unit's reboot behaviour (which hinges on user-lingering).
func Install(configPath string, run bool, autostartCount int) error {
	binPath, configPath, err := resolvePaths(configPath)
	if err != nil {
		return err
	}
	unitPath := paths.SystemdUnitFile()
	text := UnitText(binPath, configPath)

	if !run {
		fmt.Printf("Would write %s:\n\n%s\n", unitPath, text)
		fmt.Println("Then run:")
		fmt.Println("  systemctl --user daemon-reload")
		fmt.Println("  systemctl --user enable --now tunneldir")
		fmt.Printf("  loginctl enable-linger %s   # so it survives reboot (otherwise session-scoped)\n", username())
		fmt.Println("\nRe-run with --run to apply, or `install --system --run` for a boot service (sudo).")
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", unitPath)

	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", "tunneldir"); err != nil {
		return err
	}
	fmt.Println("Enabled and started tunneldir.service (user).")
	fmt.Print(userInstallNote(binPath, configPath, autostartCount))
	return nil
}

// userInstallNote explains, after a user-unit install, whether the autostart
// tunnels will actually survive a reboot — and if not, how to make them. The
// per-user unit only runs at boot when lingering is on; otherwise it is
// session-scoped, so we degrade gracefully by naming the boot-persistent
// alternatives rather than implying the tunnels will come back after a reboot.
func userInstallNote(binPath, configPath string, autostartCount int) string {
	if autostartCount == 0 {
		return ""
	}
	enabled, known := LingerEnabled()
	if known && enabled {
		return "Autostart tunnels will start at boot (user-lingering is on).\n"
	}
	lead := "Note: user-lingering can't be confirmed"
	if known { // definitively off
		lead = "Note: user-lingering is OFF"
	}
	return fmt.Sprintf(`%s, so this is a SESSION-SCOPED service:
the %d autostart tunnel(s) start when you log in, but NOT at boot. For boot
persistence, pick one:
  • system service (recommended): tunneldir install --system --run   (uses sudo)
  • keep this user service:       loginctl enable-linger %s
  • cron (no sudo, no linger), add via `+"`crontab -e`"+`:
        @reboot %s --config %s up --autostart
`, lead, autostartCount, username(), binPath, configPath)
}

// InstallSystem writes the system-wide unit (running as the invoking user) and
// enables it via sudo. It needs no linger and starts at boot. When run is false
// it only prints the unit and the sudo commands it would run.
func InstallSystem(configPath string, run bool) error {
	binPath, configPath, err := resolvePaths(configPath)
	if err != nil {
		return err
	}
	user := username()
	if user == "" || user == "$USER" {
		return fmt.Errorf("cannot determine the current user for the system unit (set $USER)")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fmt.Errorf("cannot determine your home directory for the system unit: %w", err)
	}
	if strings.ContainsAny(user, "\n\r") || strings.ContainsAny(home, "\n\r") {
		return fmt.Errorf("refusing to write unit: user or home contains a newline")
	}
	text := SystemUnitText(binPath, configPath, user, home)

	if !run {
		fmt.Printf("Would write %s (via sudo), running as user %q:\n\n%s\n", systemUnitPath, user, text)
		fmt.Println("Then run:")
		fmt.Println("  sudo systemctl daemon-reload")
		fmt.Println("  sudo systemctl enable --now tunneldir")
		fmt.Println("\nRe-run with --run to apply (sudo will prompt for your password).")
		return nil
	}

	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("sudo not found; install the system unit manually:\n  write the above to %s, then `systemctl daemon-reload && systemctl enable --now tunneldir` as root", systemUnitPath)
	}

	// Stage the unit in a temp file we own, then place it root-owned with
	// `sudo install` — avoids piping text through a shell under sudo.
	tmp, err := os.CreateTemp("", "tunneldir-unit-*.service")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := sudo("install", "-m", "0644", tmpName, systemUnitPath); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", systemUnitPath)
	if err := sudo("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := sudo("systemctl", "enable", "--now", "tunneldir"); err != nil {
		return err
	}
	fmt.Printf("Enabled and started tunneldir.service (system), running as %s.\n", user)
	fmt.Println("Tunnels start at boot — no login session or linger required.")
	return nil
}

// Uninstall disables the per-user unit and removes the unit file.
func Uninstall(run bool) error {
	unitPath := paths.SystemdUnitFile()
	if !run {
		fmt.Println("Would run:")
		fmt.Println("  systemctl --user disable --now tunneldir")
		fmt.Printf("  rm %s\n", unitPath)
		fmt.Println("  systemctl --user daemon-reload")
		fmt.Println("\nRe-run with --run to apply.")
		return nil
	}
	// Best effort: ignore disable errors (unit may already be gone).
	_ = systemctl("disable", "--now", "tunneldir")
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = systemctl("daemon-reload")
	fmt.Printf("Removed %s and disabled tunneldir.service.\n", unitPath)
	return nil
}

// UninstallSystem disables and removes the system-wide unit via sudo.
func UninstallSystem(run bool) error {
	if !run {
		fmt.Println("Would run:")
		fmt.Println("  sudo systemctl disable --now tunneldir")
		fmt.Printf("  sudo rm %s\n", systemUnitPath)
		fmt.Println("  sudo systemctl daemon-reload")
		fmt.Println("\nRe-run with --run to apply (sudo will prompt for your password).")
		return nil
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("sudo not found; remove %s and `systemctl disable --now tunneldir` as root", systemUnitPath)
	}
	// Best effort: ignore disable errors (unit may already be gone).
	_ = sudo("systemctl", "disable", "--now", "tunneldir")
	if err := sudo("rm", "-f", systemUnitPath); err != nil {
		return err
	}
	_ = sudo("systemctl", "daemon-reload")
	fmt.Printf("Removed %s and disabled tunneldir.service (system).\n", systemUnitPath)
	return nil
}

// UnitInstalled reports whether the per-user unit file has been written. It
// gates the linger warning in `status` so it only fires for users who actually
// rely on the user unit, not anyone who merely flagged a tunnel autostart (and
// not users on the system unit, which needs no linger).
func UnitInstalled() bool {
	_, err := os.Stat(paths.SystemdUnitFile())
	return err == nil
}

// LingerEnabled reports whether systemd user-lingering is enabled for the
// current user — i.e. whether the user's systemd instance (and thus our user
// unit) is started at boot rather than only while a login session is active.
// The second return value is false when the state can't be determined (loginctl
// missing, user unknown, unexpected output); callers should stay silent in that
// case rather than warn on a guess.
func LingerEnabled() (enabled, known bool) {
	u := username()
	if u == "" || u == "$USER" {
		return false, false
	}
	out, err := exec.Command("loginctl", "show-user", u, "-p", "Linger").Output()
	if err != nil {
		return false, false
	}
	val := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Linger="))
	switch val {
	case "yes":
		return true, true
	case "no":
		return false, true
	default:
		return false, false
	}
}

// LingerWarning returns a warning (with the fix) when autostart tunnels are
// configured but user-lingering is definitively off — the case where the user
// unit is enabled yet tunnels still vanish after a reboot because the user's
// systemd instance never starts without a login. It returns "" when there are
// no autostart tunnels, linger is on, or its state can't be determined, so
// callers can print the result unconditionally.
func LingerWarning(autostartCount int) string {
	if autostartCount == 0 {
		return ""
	}
	if enabled, known := LingerEnabled(); !known || enabled {
		return ""
	}
	return fmt.Sprintf(`warning: %d autostart tunnel(s) configured, but user-lingering is OFF — they
will not start at boot without an active login session. Either make it a boot
service with `+"`tunneldir install --system`"+`, or enable linger:
    loginctl enable-linger %s
`, autostartCount, username())
}

func systemctl(args ...string) error {
	full := append([]string{"--user"}, args...)
	cmd := exec.Command("systemctl", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	return nil
}

// sudo runs a command under sudo with the user's terminal attached so sudo can
// prompt for a password.
func sudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func username() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "$USER"
}
