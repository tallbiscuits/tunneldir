// Package paths resolves config and runtime-state locations following the XDG
// Base Directory spec, with sensible fallbacks.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "tunneldir"

// ConfigFile returns the path to the tunnels.yaml config.
//
// Resolution order:
//  1. an explicit override (the --config flag or TUNNELDIR_CONFIG), if non-empty
//  2. ./tunnels.yaml in the current directory, if it exists
//  3. $XDG_CONFIG_HOME/tunneldir/tunnels.yaml (~/.config/tunneldir/tunnels.yaml)
func ConfigFile(override string) string {
	if override != "" {
		return override
	}
	if env := os.Getenv("TUNNELDIR_CONFIG"); env != "" {
		return env
	}
	if local := "tunnels.yaml"; fileExists(local) {
		abs, err := filepath.Abs(local)
		if err == nil {
			return abs
		}
		return local
	}
	return filepath.Join(configHome(), appName, "tunnels.yaml")
}

// UserConfigFile returns the default per-user config path
// ($XDG_CONFIG_HOME/tunneldir/tunnels.yaml), ignoring a local ./tunnels.yaml.
// It is where `tunneldir init` writes a starter config.
func UserConfigFile() string {
	return filepath.Join(configHome(), appName, "tunnels.yaml")
}

// StateDir returns the runtime state directory ($XDG_STATE_HOME/tunneldir),
// creating it if necessary. It is private to the user (0700) since the logs and
// pidfiles beneath it can leak hostnames, usernames and connection detail.
func StateDir() (string, error) {
	dir := filepath.Join(stateHome(), appName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// PidFile returns the pidfile path for a named tunnel, ensuring its dir exists.
func PidFile(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	dir, err := subdir("pids")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".pid"), nil
}

// LogFile returns the log path for a named tunnel, ensuring its dir exists.
func LogFile(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	dir, err := subdir("logs")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".log"), nil
}

// validName rejects tunnel names that could escape the state directory when used
// to build a pid/log filename. Config-loaded names are already validated, but
// the `logs` command takes a name straight from argv, so guard here too.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return fmt.Errorf("invalid tunnel name %q", name)
	}
	return nil
}

// SystemdUnitFile returns the path of the user systemd unit we generate.
func SystemdUnitFile() string {
	return filepath.Join(configHome(), "systemd", "user", appName+".service")
}

func subdir(name string) (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(state, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".config")
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".local", "state")
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
