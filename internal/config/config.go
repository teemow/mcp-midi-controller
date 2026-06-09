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

	// USBAllowWrites is the master gate for USB writes (set_param, write_pattern,
	// recall_pattern, select_preset, and real — non-dry-run — usb_write). It
	// defaults to false: a fresh install is read-only over USB. Even with it on,
	// a device's USB connection must additionally opt in with writable: true
	// before its write tools are exposed (see engine.Connection.Writable and
	// docs/usb-tools.md).
	USBAllowWrites bool `yaml:"usb_allow_writes"`

	// AUv3ReceiverAddr is the LAN bind address for the iPad receiver — the
	// off-MCP listener that ingests parameter-tree dumps from the auv3-probe
	// iPad app (github.com/teemow/auv3-probe) AND ferries AUM session files
	// (.aumproj/.aum_midimap) in and out. Unlike ListenAddr this is meant to be
	// LAN-reachable (the iPad cannot reach loopback). It never touches hardware.
	// Default ":7800"; set to "" to disable the in-daemon receiver entirely.
	AUv3ReceiverAddr string `yaml:"auv3_receiver_addr"`
}

// Default returns the default config.
func Default() Config {
	return Config{ListenAddr: "127.0.0.1:7799", AUv3ReceiverAddr: ":7800"}
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

// DeviceTypesDir is where user device types live (override bundled by id).
func DeviceTypesDir() string { return filepath.Join(ConfigDir(), "device-types") }

// ScenesDir is where saved scenes live.
func ScenesDir() string { return filepath.Join(ConfigDir(), "scenes") }

// AUv3DefaultStatesDir is where per-audio-unit user-defined default states live:
// one YAML file per audio unit, applied automatically by the AUM author to any
// node of that unit. Unlike AUv3ProbesDir / AUMSessionsDir (volatile staging
// under the state dir) this is rig-as-code under the config dir — it survives
// probe re-syncs and is git-trackable. Captured third-party plugin state may be
// vendor/installation-specific, so a user should gitignore any default-state
// file they consider private (see the public-vs-private rule).
func AUv3DefaultStatesDir() string { return filepath.Join(ConfigDir(), "auv3-default-states") }

// DevicesPath is the persisted rig file: the set of controllable Device
// instances (devices.yaml).
func DevicesPath() string { return filepath.Join(ConfigDir(), "devices.yaml") }

// DesiredStatePath is the persisted desired-state cache (volatile).
func DesiredStatePath() string { return filepath.Join(StateDir(), "desired-state.json") }

// AUv3ProbesDir is the staging dir for AUv3 parameter-tree dumps shipped by the
// off-daemon cmd/auv3-probe receiver and ingested via import_auv3_probe. It is
// volatile (under the state dir), not rig-as-code: the dumps are a throwaway
// authoring input, not committed config.
func AUv3ProbesDir() string { return filepath.Join(StateDir(), "auv3-probes") }

// AudioClipsDir is where per-probe audio segments (stereo float32 WAVs written
// by probe_sound) land for an agent to fetch by path. Like AUv3ProbesDir it is
// volatile (under the state dir), NOT rig-as-code: captured audio is a private
// rig signal and is never committed (see the public-vs-private rule). It is
// retention-capped (oldest pruned) so it never grows without bound.
func AudioClipsDir() string { return filepath.Join(StateDir(), "audio-clips") }

// AUMSessionsDir is the staging dir for AUM session (.aumproj) and standalone
// MIDI-map (.aum_midimap) files: the ones uploaded from the iPad via the aum
// receiver and the ones authored/edited by the aum MCP tools (then downloaded
// back to the iPad). Like AUv3ProbesDir it is volatile (under the state dir),
// NOT rig-as-code — sessions are private rig snapshots (channel/plugin names,
// the controller map) and are never committed (see the public-vs-private rule).
func AUMSessionsDir() string { return filepath.Join(StateDir(), "aum-sessions") }

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
