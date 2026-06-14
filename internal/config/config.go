// Package config loads and validates the tunnels.yaml configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level tunnels.yaml document.
type Config struct {
	Defaults Defaults `yaml:"defaults"`
	Tunnels  []Tunnel `yaml:"tunnels"`
}

// Defaults are values applied to every tunnel unless overridden per-tunnel.
type Defaults struct {
	IdentityFile string            `yaml:"identity_file"`
	SSHOptions   map[string]string `yaml:"ssh_options"`
}

// Tunnel is a single SSH connection carrying one or more port forwards.
type Tunnel struct {
	Name         string    `yaml:"name"`
	Host         string    `yaml:"host"`
	User         string    `yaml:"user"`
	Port         int       `yaml:"port"`
	IdentityFile string    `yaml:"identity_file"`
	Autostart    bool      `yaml:"autostart"`
	Forwards     []Forward `yaml:"forwards"`
}

// Forward is one port forward. Exactly one of the three fields is set, mirroring
// ssh's -L / -R / -D options.
type Forward struct {
	Local   string `yaml:"local"`
	Remote  string `yaml:"remote"`
	Dynamic string `yaml:"dynamic"`
}

// Target returns the user@host[:port] descriptor for display.
func (t Tunnel) Target() string {
	hp := t.Host
	if t.Port != 0 && t.Port != 22 {
		hp = fmt.Sprintf("%s:%d", t.Host, t.Port)
	}
	if t.User != "" {
		return t.User + "@" + hp
	}
	return hp
}

// SSHPort returns the configured ssh port, defaulting to 22.
func (t Tunnel) SSHPort() int {
	if t.Port == 0 {
		return 22
	}
	return t.Port
}

// Load reads, parses, validates and normalizes the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config not found: %s", path)
		}
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := cfg.normalizeAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Tunnel returns the named tunnel, or false if absent.
func (c *Config) Tunnel(name string) (Tunnel, bool) {
	for _, t := range c.Tunnels {
		if t.Name == name {
			return t, true
		}
	}
	return Tunnel{}, false
}

// Names returns all tunnel names in config order.
func (c *Config) Names() []string {
	names := make([]string, len(c.Tunnels))
	for i, t := range c.Tunnels {
		names[i] = t.Name
	}
	return names
}

// AutostartNames returns the names of tunnels flagged autostart, in order.
func (c *Config) AutostartNames() []string {
	var names []string
	for _, t := range c.Tunnels {
		if t.Autostart {
			names = append(names, t.Name)
		}
	}
	return names
}

func (c *Config) normalizeAndValidate() error {
	if len(c.Tunnels) == 0 {
		return fmt.Errorf("no tunnels defined")
	}
	c.Defaults.IdentityFile = expandHome(c.Defaults.IdentityFile)

	seen := make(map[string]bool)
	for i := range c.Tunnels {
		t := &c.Tunnels[i]
		if t.Name == "" {
			return fmt.Errorf("tunnel #%d: name is required", i+1)
		}
		if strings.ContainsAny(t.Name, " /\\\t") {
			return fmt.Errorf("tunnel %q: name must not contain spaces or slashes", t.Name)
		}
		if seen[t.Name] {
			return fmt.Errorf("duplicate tunnel name %q", t.Name)
		}
		seen[t.Name] = true

		if t.Host == "" {
			return fmt.Errorf("tunnel %q: host is required", t.Name)
		}
		if t.Port < 0 || t.Port > 65535 {
			return fmt.Errorf("tunnel %q: invalid port %d", t.Name, t.Port)
		}
		// Apply identity default, then expand ~.
		if t.IdentityFile == "" {
			t.IdentityFile = c.Defaults.IdentityFile
		} else {
			t.IdentityFile = expandHome(t.IdentityFile)
		}
		if len(t.Forwards) == 0 {
			return fmt.Errorf("tunnel %q: at least one forward is required", t.Name)
		}
		for j, f := range t.Forwards {
			if err := validateForward(f); err != nil {
				return fmt.Errorf("tunnel %q forward #%d: %w", t.Name, j+1, err)
			}
		}
	}
	return nil
}

func validateForward(f Forward) error {
	set := 0
	if f.Local != "" {
		set++
	}
	if f.Remote != "" {
		set++
	}
	if f.Dynamic != "" {
		set++
	}
	if set == 0 {
		return fmt.Errorf("must set one of local/remote/dynamic")
	}
	if set > 1 {
		return fmt.Errorf("set exactly one of local/remote/dynamic")
	}
	return nil
}

func expandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
