// Package paths resolves config and runtime-state locations following the XDG
// Base Directory spec, with sensible fallbacks.
package paths

import (
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

// StateDir returns the runtime state directory ($XDG_STATE_HOME/tunneldir),
// creating it if necessary.
func StateDir() (string, error) {
	dir := filepath.Join(stateHome(), appName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// PidFile returns the pidfile path for a named tunnel, ensuring its dir exists.
func PidFile(name string) (string, error) {
	dir, err := subdir("pids")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".pid"), nil
}

// LogFile returns the log path for a named tunnel, ensuring its dir exists.
func LogFile(name string) (string, error) {
	dir, err := subdir("logs")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".log"), nil
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
