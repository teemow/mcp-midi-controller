//go:build cgo

package usbmidi

// Dependency anchor (CGO only). The USB/ALSA implementation (Phase E) drives
// hardware through the rtmidi driver, which requires CGO + ALSA headers. The
// cgo build constraint keeps CGO_ENABLED=0 builds pure-Go (the BLE path) while
// still recording rtmididrv in go.mod (cgo is enabled by default, so this file
// is considered by `go mod tidy`).
import _ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
