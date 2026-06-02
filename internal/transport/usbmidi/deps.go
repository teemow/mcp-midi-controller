package usbmidi

// Dependency anchor. The USB/ALSA implementation (Phase E) builds and parses
// MIDI messages with gomidi's pure-Go core. This blank import keeps the module
// recorded in go.mod (surviving `go mod tidy`) until the real code lands.
import _ "gitlab.com/gomidi/midi/v2"
