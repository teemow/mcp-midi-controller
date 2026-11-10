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

	// AUMAutoImport enables the automatic session-rig import: when the iPad
	// downloads a staged .aumproj (the surest signal it is about to be loaded
	// into AUM) and when the ProbeMidiBrain control channel connects, the
	// daemon re-runs import_aum_session for that session and pushes the
	// control-surface manifest to the brain. Default true; set false to keep
	// imports strictly tool-driven.
	AUMAutoImport bool `yaml:"aum_auto_import"`

	// IPadWDAURL is the WebDriverAgent endpoint for the iPad UI tools
	// (ipad_*, get_aum_ui, wait_for_aum_ui): the USB channel into the iPad's
	// screen that operates AUM by touch and verifies MIDI against AUM's own
	// display. auv3-probe's scripts/ipad forwards WDA to 127.0.0.1:8100 on the
	// host the iPad is plugged into; point this at that host when the daemon
	// runs elsewhere. Default "http://127.0.0.1:8100"; "" disables the tools.
	IPadWDAURL string `yaml:"ipad_wda_url"`

	// IPadGoIOS is the go-ios CLI the iPad tools use read-only, to diagnose a
	// missing tunnel / disk image and to resolve sideloaded apps' bundle IDs.
	// Default "ios" (on PATH); give an absolute path when the service's PATH
	// lacks it.
	IPadGoIOS string `yaml:"ipad_go_ios"`

	// RecordingsKeep is how many recordings (start_recording, play_song with
	// record) the daemon keeps in the state dir; older ones are deleted when a
	// new one is finished. Default 50; a negative number keeps all of them.
	RecordingsKeep int `yaml:"recordings_keep"`

	// GoogleDrive says where upload_recording puts recordings.
	GoogleDrive GoogleDrive `yaml:"google_drive"`
}

// GoogleDrive holds the upload settings. Nothing in it is secret: the OAuth
// client is a file of its own (GoogleDriveClientPath) and the sign-in lives in
// the state dir (GoogleDriveTokenPath).
type GoogleDrive struct {
	// Folder is the Drive folder recordings are uploaded into: a name, or
	// several separated by "/" for a folder inside others. The daemon creates
	// it on first use (it only sees folders it made itself). Empty: the
	// Drive's top level.
	Folder string `yaml:"folder"`

	// AutoUpload uploads every recording of a song by itself once it is
	// finished. Default false: a recording is uploaded when upload_recording
	// is called, or when the recording was started with upload set.
	AutoUpload bool `yaml:"auto_upload"`
}

// Default returns the default config.
func Default() Config {
	return Config{
		ListenAddr:       "127.0.0.1:7799",
		AUv3ReceiverAddr: ":7800",
		AUMAutoImport:    true,
		IPadWDAURL:       "http://127.0.0.1:8100",
		IPadGoIOS:        "ios",
		RecordingsKeep:   50,
	}
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

// SongsDir is where songs live: one YAML document per song (tracks, patterns,
// sections, arrangement). Like scenes they are rig-as-code under the config
// dir. A song names the plugins of one rig, so a user who considers that
// private keeps the directory out of a public repository.
func SongsDir() string { return filepath.Join(ConfigDir(), "songs") }

// SongSessionsPath records which staged AUM session each song was last
// authored into, and for which control layout — what play_song checks before
// sending a song at a session that no longer matches it. Volatile (state dir).
func SongSessionsPath() string { return filepath.Join(StateDir(), "song-sessions.json") }

// SongExportsDir is where export_song_midi writes Standard MIDI Files.
// Volatile (state dir): an export is derived from its song.
func SongExportsDir() string { return filepath.Join(StateDir(), "song-exports") }

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

// CurrentAUMSessionPath is the persisted "current session" marker: the staged
// session id the iPad last downloaded (and so presumably has loaded in AUM).
// The brain-connect auto-import re-runs against it, including across daemon
// restarts. Volatile (state dir), never committed.
func CurrentAUMSessionPath() string { return filepath.Join(StateDir(), "current-aum-session.json") }

// AUMSessionSwitchPath is the persisted PC-to-session registry: which staged
// session each Program Change on the session-switch channel loads. Programs
// are pinned — the user's hand-wired AUM global "Session Load" mappings depend
// on them — so the file lives in the state dir but survives re-registration
// (entries are never renumbered).
func AUMSessionSwitchPath() string { return filepath.Join(StateDir(), "aum-session-switch.json") }

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

// RecordingsDir is where recordings land: for each one its audio file and a
// small record of what it is (internal/recording). Volatile (state dir) and
// capped at Config.RecordingsKeep; like every captured audio it is a private
// rig signal and is never committed.
func RecordingsDir() string { return filepath.Join(StateDir(), "recordings") }

// GoogleDriveClientPath is the user's own OAuth client for Google Drive: the
// JSON the Google Cloud console offers for download. It holds the client
// secret, so it is a file of its own rather than a key in config.yaml: keep it
// readable by its owner only, and out of a repository the config dir is
// tracked in.
func GoogleDriveClientPath() string { return filepath.Join(ConfigDir(), "google-drive-client.json") }

// GoogleDriveTokenPath is the daemon's sign-in to Google Drive, written
// owner-only by internal/drive once the user has approved it. Never committed,
// never logged, never returned by a tool.
func GoogleDriveTokenPath() string { return filepath.Join(StateDir(), "google-drive-token.json") }

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
	if cfg.RecordingsKeep == 0 {
		cfg.RecordingsKeep = Default().RecordingsKeep
	}
	return cfg, nil
}
