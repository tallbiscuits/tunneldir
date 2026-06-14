// Package service generates and installs the systemd user unit that brings up
// autostart-flagged tunnels at boot.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tunneldir/internal/paths"
)

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

// UnitText renders the unit file. binPath is the absolute tunneldir binary and
// configPath is the resolved config the service should use (pinned with
// --config so the service finds the same file regardless of working directory).
// Both are systemd-quoted so paths containing spaces work and embedded quotes
// can't break out of the Exec line.
func UnitText(binPath, configPath string) string {
	invocation := systemdQuote(binPath)
	if configPath != "" {
		invocation += " --config " + systemdQuote(configPath)
	}
	return fmt.Sprintf(unitTemplate, invocation, invocation)
}

// systemdQuote wraps s in double quotes, escaping backslashes and quotes, as
// understood by systemd's Exec-line parser.
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Install writes the user unit and enables it. When run is false it only prints
// what it would do (the unit text and the systemctl commands). configPath is
// the resolved config path to pin into the unit.
func Install(configPath string, run bool) error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating own binary: %w", err)
	}
	binPath, _ = filepath.Abs(binPath)
	configPath, _ = filepath.Abs(configPath)
	// A newline in either path would let it inject extra directives into the
	// line-based unit file, regardless of quoting — refuse rather than emit it.
	if strings.ContainsAny(binPath, "\n\r") || strings.ContainsAny(configPath, "\n\r") {
		return fmt.Errorf("refusing to write unit: binary or config path contains a newline")
	}
	unitPath := paths.SystemdUnitFile()
	text := UnitText(binPath, configPath)

	if !run {
		fmt.Printf("Would write %s:\n\n%s\n", unitPath, text)
		fmt.Println("Then run:")
		fmt.Println("  systemctl --user daemon-reload")
		fmt.Println("  systemctl --user enable --now tunneldir")
		fmt.Printf("  loginctl enable-linger %s   # start at boot without login\n", username())
		fmt.Println("\nRe-run with --run to apply.")
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
	fmt.Printf("Tip: run `loginctl enable-linger %s` so tunnels start at boot without an active login.\n", username())
	return nil
}

// Uninstall disables the unit and removes the unit file.
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

func username() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "$USER"
}
