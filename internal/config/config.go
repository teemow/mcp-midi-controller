// Package config resolves XDG paths and loads the daemon config. The config dir
// is designed to be a single git-trackable "rig as code" directory; volatile
// state (desired-state cache, logs) lives separately under the state dir.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const appName = "mcp-midi-controller"

// Config holds daemon settings (config.yaml).
type Config struct {
	// ListenAddr is the streamable-HTTP bind address. MUST stay on loopback.
	ListenAddr string `yaml:"listen_addr"`
}

// Default returns the default config.
func Default() Config {
	return Config{ListenAddr: "127.0.0.1:7799"}
}

// ConfigDir returns $XDG_CONFIG_HOME/mcp-midi-controller (rig-as-code).
func ConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appName)
}

// StateDir returns $XDG_STATE_HOME/mcp-midi-controller (volatile).
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, appName)
}

// DevicesDir is where user device definitions live (override bundled by name).
func DevicesDir() string { return filepath.Join(ConfigDir(), "devices") }

// ScenesDir is where saved scenes live.
func ScenesDir() string { return filepath.Join(ConfigDir(), "scenes") }

// BindingsPath is the persisted bindings file.
func BindingsPath() string { return filepath.Join(ConfigDir(), "bindings.yaml") }

// DesiredStatePath is the persisted desired-state cache (volatile).
func DesiredStatePath() string { return filepath.Join(StateDir(), "desired-state.json") }

// Load reads config.yaml from the config dir, falling back to defaults.
func Load() (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = Default().ListenAddr
	}
	return cfg, nil
}
