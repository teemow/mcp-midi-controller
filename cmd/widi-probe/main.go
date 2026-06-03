// Command widi-probe is a throwaway live spike (design build-order step 1) that
// verifies the reverse-engineered WIDI SysEx config protocol against real
// hardware over the project's own blemidi transport. It connects to a WIDI
// endpoint, reads every settings register (and a couple of status values) via
// CME SysEx, and prints the decoded replies. Not part of the shipped binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/transport"
	"github.com/teemow/mcp-midi-controller/internal/transport/blemidi"
)

// CME SysEx header (00 20 63 = CME manufacturer id, 0F = product group).
var cmeHeader = []byte{0x00, 0x20, 0x63, 0x0F}

const (
	cmdAction        = 0x00
	cmdReadStatus    = 0x01
	cmdReadSettings  = 0x02
	cmdWriteSettings = 0x03
)

type reg struct {
	id   byte
	leng byte
	name string
}

// Settings registers (v1.g in the WIDI app), with their lengths.
var registers = []reg{
	{0, 32, "BLE_NAME"},
	{1, 1, "TX_POWER"},
	{2, 1, "BLE_PHY_SWITCH"},
	{3, 1, "SCAN_DURATION"},
	{4, 1, "ADVERTISE_DURATION"},
	{5, 1, "POWER_SAVING"},
	{6, 1, "FORCE_BLE_ROLE"},
	{7, 6, "CONNECT_ADDRESS_1"},
	{8, 6, "CONNECT_ADDRESS_2"},
	{9, 6, "CONNECT_ADDRESS_3"},
	{10, 6, "CONNECT_ADDRESS_4"},
	{11, 1, "PREFER_LATENCY_JITTER"},
	{12, 1, "INTERNAL_CLOCK_TEMPO_BPM"},
	{13, 1, "INTERNAL_CLOCK_TEMPO_MS"},
	{14, 1, "MIDI_IN_THRU"},
}

var statuses = []struct {
	id   byte
	name string
}{
	{0, "SERIAL"},
	{1, "BLE_ROLE"},
	{2, "BLE_STATE"},
	{7, "BLE_PHY"},
}

// buildSysex frames a CME WIDI SysEx: F0 <hdr×4> devID cmd <data> checksum F7.
func buildSysex(devID, cmd byte, data []byte) []byte {
	out := []byte{0xF0}
	out = append(out, cmeHeader...)
	out = append(out, devID, cmd)
	out = append(out, data...)
	sum := int(cmd)
	for _, b := range data {
		sum += int(b)
	}
	out = append(out, byte(sum&0x7F), 0xF7)
	return out
}

// buildReadSetting builds a READ_SETTINGS request for one register.
func buildReadSetting(devID, regID, regLen byte) []byte {
	return buildSysex(devID, cmdReadSettings, []byte{regID, regLen})
}

// buildWriteByteSetting builds a WRITE_SETTINGS request for a single-byte
// register, matching the app's writeByteSettings: data = [regId, 1, low, high].
func buildWriteByteSetting(devID, regID, val byte) []byte {
	return buildSysex(devID, cmdWriteSettings, []byte{regID, 0x01, val & 0x0F, (val >> 4) & 0x0F})
}

// decodeNibbles reverses the app's low-nibble-first byte encoding.
func decodeNibbles(nib []byte) []byte {
	out := make([]byte, 0, len(nib)/2)
	for i := 0; i+1 < len(nib); i += 2 {
		out = append(out, (nib[i]&0x0F)|((nib[i+1]&0x0F)<<4))
	}
	return out
}

func isCMEReply(b []byte) bool {
	return len(b) >= 8 && b[0] == 0xF0 &&
		b[1] == cmeHeader[0] && b[2] == cmeHeader[1] &&
		b[3] == cmeHeader[2] && b[4] == cmeHeader[3] &&
		(b[6]&0x40) == 0x40
}

var sysexErrors = map[byte]string{
	127: "SYSX_UNKNOWN_PARAMETER",
	126: "SYSEX_WRONG_CHECKSUM",
	125: "SYSEX_WRONG_EOX",
	124: "SYSEX_PARAM_OUT_OF_RANGE",
	123: "SYSEX_BLE_NOT_CONNECTED",
	122: "SYSEX_PARAM_UNEXPECTED",
}

func regName(id byte) string {
	for _, r := range registers {
		if r.id == id {
			return r.name
		}
	}
	return "REG_" + fmt.Sprint(id)
}

func ascii(raw []byte) string {
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, string(raw))
}

func decodeReply(b []byte) string {
	// 10-byte form is an error reply: F0 hdr×4 devID cmd errId param F7.
	if len(b) == 10 {
		if name, ok := sysexErrors[b[7]]; ok {
			return fmt.Sprintf("ERROR dev=0x%02X %s param=0x%02X", b[5], name, b[8])
		}
	}
	devID := b[5]
	cmd := b[6] & 0x0F
	body := b[7 : len(b)-1] // strip F7; keep trailing checksum for visibility
	switch cmd {
	case cmdReadSettings, cmdWriteSettings:
		label := "SETTINGS "
		if cmd == cmdWriteSettings {
			label = "WRITE-ACK"
		}
		if len(body) < 2 {
			return fmt.Sprintf("%s dev=0x%02X (short) body=% X", label, devID, body)
		}
		regID := body[0]
		n := int(body[1]) // logical byte count; wire carries 2*n nibbles
		nib := body[2:]
		if 2*n <= len(nib) {
			nib = nib[:2*n]
		}
		raw := decodeNibbles(nib)
		return fmt.Sprintf("%s dev=0x%02X %-24s n=%2d bytes=% X ascii=%q", label, devID, regName(regID), n, raw, ascii(raw))
	case cmdReadStatus:
		if len(body) < 1 {
			return fmt.Sprintf("STATUS   dev=0x%02X (short) body=% X", devID, body)
		}
		return fmt.Sprintf("STATUS   dev=0x%02X id=%d body=% X", devID, body[0], body[1:])
	default:
		return fmt.Sprintf("cmd=0x%02X dev=0x%02X body=% X", cmd, devID, body)
	}
}

func main() {
	addr := flag.String("addr", "", "WIDI BLE endpoint address (required, e.g. AA:BB:CC:DD:EE:FF)")
	devID := flag.Int("dev", 0x12, "WIDI product devID (0x09 Master, 0x0B Jack, 0x0E Bud Pro, 0x12 Thru6 BT)")
	writeTest := flag.Bool("write-test", false, "reversible write test: toggle MIDI_IN_THRU and restore")
	writeReg := flag.Int("write-reg", -1, "single-byte register id to write (e.g. 6 = FORCE_BLE_ROLE); -1 disables")
	writeVal := flag.Int("write-val", 0, "value to write into --write-reg (read-before / write / read-after)")
	flag.Parse()

	if *addr == "" {
		log.Fatal("widi-probe: --addr is required (a WIDI BLE endpoint address)")
	}

	t, err := blemidi.New()
	if err != nil {
		log.Fatalf("new transport: %v", err)
	}
	defer t.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("connecting to %s ...", *addr)
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	defer ccancel()
	if err := t.Connect(cctx, *addr); err != nil {
		log.Fatalf("connect: %v", err)
	}
	log.Printf("connected")

	in, err := t.Listen(ctx, *addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	// Collect inbound SysEx replies in the background.
	go func() {
		for ev := range in {
			if ev.Kind != transport.MIDIEvent || len(ev.Data) == 0 || ev.Data[0] != 0xF0 {
				continue
			}
			if isCMEReply(ev.Data) {
				log.Printf("RX  %s   [raw % X]", decodeReply(ev.Data), ev.Data)
			} else {
				log.Printf("RX  (non-CME sysex) % X", ev.Data)
			}
		}
	}()

	send := func(label string, sx []byte) {
		log.Printf("TX  %s  % X", label, sx)
		if err := t.Send(ctx, *addr, transport.Event{Kind: transport.MIDIEvent, Data: sx}); err != nil {
			log.Printf("  send error: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	d := byte(*devID)

	if *writeReg >= 0 {
		// Generic single-byte register write: read current value, write the new
		// one, read back to confirm. Persistent flash config (e.g. role); not
		// auto-restored.
		reg := byte(*writeReg)
		val := byte(*writeVal)
		log.Printf("--- WRITE reg %d (%s) = %d on dev 0x%02X ---", reg, regName(reg), val, d)
		send("READ  (before)", buildReadSetting(d, reg, 1))
		send(fmt.Sprintf("WRITE %s = %d", regName(reg), val), buildWriteByteSetting(d, reg, val))
		send("READ  (verify)", buildReadSetting(d, reg, 1))
		log.Printf("waiting for trailing replies ...")
		time.Sleep(2 * time.Second)
		log.Printf("done")
		return
	}

	if *writeTest {
		// Reversible write test on MIDI_IN_THRU (reg 14): read current, write the
		// opposite, read back, then restore the original and read back again.
		const reg = byte(14)
		log.Printf("--- WRITE TEST: MIDI_IN_THRU (reg 14) on dev 0x%02X ---", d)
		send("READ  MIDI_IN_THRU (before)", buildReadSetting(d, reg, 1))
		send("WRITE MIDI_IN_THRU = 1 (on)", buildWriteByteSetting(d, reg, 1))
		send("READ  MIDI_IN_THRU (verify)", buildReadSetting(d, reg, 1))
		send("WRITE MIDI_IN_THRU = 0 (restore)", buildWriteByteSetting(d, reg, 0))
		send("READ  MIDI_IN_THRU (after)", buildReadSetting(d, reg, 1))
		log.Printf("waiting for trailing replies ...")
		time.Sleep(2 * time.Second)
		log.Printf("done")
		return
	}

	for _, r := range registers {
		send("READ_SETTINGS "+r.name, buildReadSetting(d, r.id, r.leng))
	}
	for _, s := range statuses {
		send("READ_STATUS "+s.name, buildSysex(d, cmdReadStatus, []byte{s.id}))
	}

	log.Printf("waiting for trailing replies ...")
	time.Sleep(2 * time.Second)
	log.Printf("done")
}
