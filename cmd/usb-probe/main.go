// Command usb-probe is a throwaway live spike for the USB readback-protocol
// research (see docs/research/usb.md). It reads real device state off a
// USB-connected pedal, so a later phase can verify what BLE-MIDI writes actually
// landed. It is the USB counterpart of cmd/widi-probe (the BLE-MIDI/WIDI side)
// and is not part of the shipped daemon. Read-only by design: it only ever
// issues read/identify/request commands, never a write/save.
//
// Two device tracks, selected with --device (default ml10x):
//
// ml10x — Morningstar ML10X editor protocol, SysEx over the device's USB-MIDI
// interface (ALSA rawmidi, e.g. hw:4,0,0) — NOT over the CDC-ACM serial port and
// NOT the MC-series external SysEx API (op1 0x70). The framing was
// reverse-engineered by capturing the web editor's "read from device" with
// usbmon (docs/research/ml10x.md):
//
//	F0 00 21 24 07 00 <op1> <op2> <payload…> <cksum> F7
//	         └ Morningstar mfg ┘  │   └ ML10X model id 0x07
//	                              └ op1: 0x00 request / 0x01 status / 0x06 data block
//	cksum = XOR(all bytes before cksum) & 0x7F
//
// sl-2 — Boss SL-2, Roland address-based SysEx over USB-MIDI (ALSA rawmidi, e.g.
// hw:6,0,0). The full editor protocol was decompiled from "BOSS TONE STUDIO for
// SL-2" (CEF/JS app; protocol in config/address_map.js + common/midi_controller.js,
// extracted reference in docs/private/sl2-bts/). Confirmed: model id is the 5-byte
// 00 00 00 00 1D, device id 0x10, RQ1/DT1 with a 4-byte address + 4-byte size and
// the standard Roland checksum. By default this track sends the Universal Identity
// Request then reads a set of named registers (editor-comm level/rev, system tempo
// + MIDI channel, temp-patch name) and decodes the DT1 replies. Read-only: it only
// issues RQ1 (never a DT1 write). See docs/research/sl-2.md for the address map and
// the (write) pattern-create flow.
//
// h90 — Eventide H90, the documented Eventide system SysEx over USB-MIDI (ALSA
// rawmidi, e.g. hw:3,0,0). It sends the Universal Identity Request and the
// Eventide Factor/H9-family proprietary "WANT" reads (F0 1C 70 <dev> <cmd> F7,
// mfg 0x1C / model 0x70), then reports any reply. The outcome here is itself the
// finding: the H90 answers NONE of them (read-only probe). Its USB-MIDI only
// carries PC/CC/Clock and its mass-storage LUN is a Recovery-Mode firmware
// volume, not a preset filesystem — so there is no non-destructive USB readback
// channel. See docs/research/h90.md for the transcribed protocol + the live
// negative result. The --h90-trpc/--h90-ping/--h90-raw/--h90-handshake flags
// drive the modern H90 Control "TRPC" protocol (request/response RPC over MIDI
// SysEx, FlatBuffers payloads) recovered by static analysis of H90 Control.exe.
// The SysEx prefix F0 1C 77 is recovered AND live-verified: --h90-ping sends a
// safe empty/opcode-0 Dot9 frame and the pedal replies with the error response
// (status 0x02) over its own USB-MIDI. The per-operation Dot9MessageType opcodes
// still need a live capture, so a working getSystemParameters readback is not
// wired yet — but the envelope is confirmed.
//
// eq2 — Source Audio EQ2, the Neuro editor channel over the device's vendor HID
// interface (/dev/hidrawN, VID:PID 29A4:0400) — NOT a USB-MIDI SysEx protocol
// (the EQ2's USB-MIDI interface only carries standard PC/CC; Source Audio has no
// published readback SysEx). The framing was reverse-engineered for the sibling
// C4 Synth (same 29A4 vendor / Neuro HID framework, see docs/research/eq-2.md):
//
//	write  0x36 <a2> <a1> <a0>   -> dump 32 bytes from 24-bit address a2a1a0
//	write  0x77 <preset 0-127>   -> select (program change) a preset  [WRITE]
//
// The reply is a 38-byte HID input report: byte[0] header, byte[1:33] the 32
// dumped bytes, the rest padding. Read-only by design: only the 0x36 dump is ever
// sent; 0x77 changes device state and is intentionally never issued here.
//
// opus — Two Notes Opus, the Torpedo Remote channel over the device's vendor HID
// interface (/dev/hidrawN, VID:PID 0483:A334). Like the EQ2 this is NOT USB-MIDI:
// the Opus exposes ONLY a HID interface (no USB-MIDI/CDC/audio), a raw 64-byte
// bidirectional vendor pipe (usage page 0xFF00, no report IDs, no FEATURE reports
// — interrupt EP 0x01 OUT / 0x81 IN; see docs/research/usb.md + opus.md). The
// Torpedo Remote command layout INSIDE that pipe is proprietary and undocumented,
// and the editor is Mac/Win/iOS only, so the semantic protocol is blocked on a
// non-Linux capture. This track therefore does NOT guess command bytes: by
// default it LISTENS read-only (drains input reports the device emits, e.g. on a
// front-panel change) and prints them; --opus-raw replays one operator-supplied
// captured frame. No request bytes are ever synthesised here (a wrong write could
// change device state), mirroring the read-only-first methodology.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv" // ALSA-backed rawmidi driver
	"golang.org/x/sys/unix"
)

// Morningstar manufacturer SysEx id + the ML10X model id (discovered live; the
// MC-series ids 0x03..0x08 are documented, the ML10X's 0x07 is not).
var morningstarMfg = []byte{0x00, 0x21, 0x24}

const ml10xModel = 0x07

// op1 (message class) values seen on the wire.
const (
	classRequest = 0x00 // host -> device
	classStatus  = 0x01 // device -> host: ack / small value / streaming index
	classData    = 0x06 // device -> host: TLV data block
)

// Read opcodes (op2) the web editor issues for a full "read from device". Their
// individual meanings are partly inferred (see docs/research/ml10x.md); they are
// all read-only and together stream back the whole bank/preset config.
var readOps = []byte{0x00, 0x01, 0x12, 0x13, 0x15, 0x16, 0x17, 0x18}

func checksum(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	c := b[0]
	for _, x := range b[1:] {
		c ^= x
	}
	return c & 0x7F
}

// buildRequest frames an ML10X read request: class 0x00, the given op2, then an
// 8-byte payload (zeros, as the editor sends, unless overridden), checksum + F7.
func buildRequest(op2 byte, payload ...byte) []byte {
	p := make([]byte, 8)
	copy(p, payload)
	body := []byte{0xF0}
	body = append(body, morningstarMfg...)
	body = append(body, ml10xModel, 0x00, classRequest, op2)
	body = append(body, p...)
	return append(body, checksum(body), 0xF7)
}

// parsePayload turns "01 00 ff" / "01,00,ff" into request payload bytes.
func parsePayload(s string) ([]byte, error) {
	s = strings.NewReplacer(",", " ").Replace(s)
	var out []byte
	for _, tok := range strings.Fields(s) {
		var v int
		if _, err := fmt.Sscanf(tok, "%x", &v); err != nil {
			return nil, fmt.Errorf("bad payload byte %q: %w", tok, err)
		}
		out = append(out, byte(v))
	}
	return out, nil
}

func hexs(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, " ")
}

func ascii(b []byte) string {
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, string(b))
}

func isML10X(f []byte) bool {
	return len(f) >= 9 && f[0] == 0xF0 &&
		f[1] == morningstarMfg[0] && f[2] == morningstarMfg[1] && f[3] == morningstarMfg[2] &&
		f[4] == ml10xModel
}

// tlvRecord is one 7F <id> <len> <value> entry inside a data block.
type tlvRecord struct {
	id  byte
	val []byte
}

// parseTLV walks a data-block payload of 7F-delimited records.
func parseTLV(p []byte) []tlvRecord {
	var recs []tlvRecord
	for i := 0; i+2 < len(p); {
		if p[i] != 0x7F {
			i++
			continue
		}
		id, n := p[i+1], int(p[i+2])
		end := i + 3 + n
		if end > len(p) {
			break
		}
		recs = append(recs, tlvRecord{id: id, val: p[i+3 : end]})
		i = end
	}
	return recs
}

// decode renders one reply frame. Data blocks are summarised as TLV records;
// the actual values are rig-specific (preset/loop names) so this prints them for
// the operator but the research note keeps them in docs/private/.
func decode(f []byte) string {
	if !isML10X(f) {
		return fmt.Sprintf("(non-ML10X) %s", hexs(f))
	}
	op1, op2 := f[6], f[7]
	payload := f[8 : len(f)-2]
	ck := f[len(f)-2]
	ckNote := ""
	if ck != checksum(f[:len(f)-2]) {
		ckNote = fmt.Sprintf(" !cksum(%02X)", ck)
	}
	switch op1 {
	case classData:
		recs := parseTLV(payload)
		var b strings.Builder
		fmt.Fprintf(&b, "DATA   op2=0x%02X len=%d records=%d%s", op2, len(payload), len(recs), ckNote)
		for _, r := range recs {
			fmt.Fprintf(&b, "\n         7F id=0x%02X len=%2d  %-32s %q", r.id, len(r.val), hexs(r.val), ascii(r.val))
		}
		return b.String()
	case classStatus:
		return fmt.Sprintf("STATUS op2=0x%02X payload=%s%s", op2, hexs(payload), ckNote)
	default:
		return fmt.Sprintf("op1=0x%02X op2=0x%02X payload=%s%s", op1, op2, hexs(payload), ckNote)
	}
}

// session bundles an opened pair of ALSA rawmidi ports (one device) with a
// background SysEx collector, so each device track just sends and drains.
type session struct {
	out   drivers.Out
	wait  time.Duration
	mu    sync.Mutex
	inbox [][]byte
}

// send transmits one raw message, then drains everything that arrives within the
// configured wait window. It clears stragglers from the previous send first so
// the returned slice is just this request's replies.
func (s *session) send(b []byte) [][]byte {
	s.drain()
	if err := s.out.Send(b); err != nil {
		log.Printf("  send error: %v", err)
		return nil
	}
	time.Sleep(s.wait)
	return s.drain()
}

func (s *session) drain() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := s.inbox
	s.inbox = nil
	return got
}

func main() {
	device := flag.String("device", "ml10x", "device track: ml10x | sl-2 | eq2 | h90 | opus")
	portMatch := flag.String("port", "", "ALSA rawmidi port name substring (default: per-device)")
	wait := flag.Duration("wait", 1500*time.Millisecond, "time to collect replies after each request")
	// ML10X track flags.
	full := flag.Bool("full", false, "ml10x: send the editor's full read-opcode sweep (default: identify + names only)")
	op := flag.Int("op", -1, "ml10x: single-opcode mode: send just this op2 (hex/dec) and decode every reply")
	payloadStr := flag.String("payload", "", "ml10x: request payload bytes for --op mode, e.g. \"01 00\" (default 8 zeros)")
	// H90 track flags. Default is the legacy TJ WANT reads (the negative
	// result); the TRPC flags scaffold the H90 Control app protocol path for a
	// later live capture (see docs/research/h90.md "H90 Control protocol").
	h90TRPC := flag.Bool("h90-trpc", false, "h90: use the TRPC/SysEx7 (H90 Control app) path instead of the legacy TJ WANT reads")
	h90Raw := flag.String("h90-raw", "", "h90: TRPC path — send one arbitrary raw SysEx (hex bytes incl. F0…F7, e.g. captured frame) and decode the reply")
	h90Ping := flag.Bool("h90-ping", false, "h90: TRPC path — send the safe Dot9 ping (F0 1C 77 00 00 00 00 00 F7) and decode the reply; verifies the F0 1C 77 framing (expect error status 0x02)")
	h90Handshake := flag.Bool("h90-handshake", false, "h90: TRPC path — send the connection-handshake probe (Identity Request to --h90-dev) and listen")
	h90Dev := flag.Int("h90-dev", 0x7F, "h90: Eventide SysEx device id (0x7F = broadcast)")
	// SL-2 track flags (manual single-RQ1 probe; default reads the named registers).
	rq1Model := flag.String("rq1-model", "", "sl-2: hex model-id bytes for a single RQ1 probe, e.g. \"00 00 00 00 1D\" (default: read the named registers with the confirmed model id)")
	rq1Addr := flag.String("rq1-addr", "20 00 00 00", "sl-2: hex RQ1 address bytes for --rq1-model")
	rq1Size := flag.String("rq1-size", "00 00 00 10", "sl-2: hex RQ1 size bytes for --rq1-model")
	// EQ2 + Opus track flags (Neuro / Torpedo Remote HID; hidraw, not ALSA MIDI).
	hidraw := flag.String("hidraw", "", "eq2/opus: /dev/hidrawN path (default: auto-detect by the track's VID:PID)")
	eq2Addr := flag.String("eq2-addr", "", "eq2: single 24-bit hex address to dump 32 bytes from, e.g. 0800A0 (default: preset-name sweep)")
	eq2Scan := flag.String("eq2-scan", "", "eq2: contiguously dump a hex address range \"from:to\" in 32-byte windows, printing those with readable ASCII (memory-map discovery)")
	eq2Base := flag.Uint("eq2-base", 0x80000, "eq2: preset-block base address for the name sweep (EQ2-confirmed)")
	eq2Stride := flag.Uint("eq2-stride", 0x1000, "eq2: bytes per preset block (EQ2-confirmed)")
	eq2NameOff := flag.Uint("eq2-name-off", 0x97, "eq2: name offset within a preset block (EQ2-confirmed)")
	eq2Count := flag.Int("eq2-count", 128, "eq2: number of preset slots to sweep")
	// Opus track flags (Torpedo Remote HID; hidraw, raw 64-byte vendor pipe).
	opusRaw := flag.String("opus-raw", "", "opus: replay ONE captured Torpedo Remote request frame (hex bytes, padded/truncated to 64) and dump the reply; default is listen-only (sends nothing)")
	flag.Parse()
	defer midi.CloseDriver()

	// EQ2 and Opus are the odd ones out: they speak a vendor protocol over a HID
	// interface, not ALSA rawmidi, so they run before any MIDI port is opened.
	if *device == "eq2" {
		runEQ2(*hidraw, *eq2Addr, *eq2Scan, *eq2Base, *eq2Stride, *eq2NameOff, *eq2Count, *wait)
		return
	}
	if *device == "opus" {
		runOpus(*hidraw, *opusRaw, *wait)
		return
	}

	pm := *portMatch
	if pm == "" {
		pm = map[string]string{"ml10x": "ML10X", "sl-2": "SL-2", "h90": "H90"}[*device]
	}
	if pm == "" {
		log.Fatalf("usb-probe: unknown --device %q (want ml10x|sl-2|eq2|h90|opus) and no --port given", *device)
	}

	out, err := midi.FindOutPort(pm)
	if err != nil {
		log.Fatalf("usb-probe: no MIDI out port matching %q (amidi -l to list): %v", pm, err)
	}
	if err := out.Open(); err != nil {
		log.Fatalf("usb-probe: open out: %v", err)
	}
	in, err := midi.FindInPort(pm)
	if err != nil {
		log.Fatalf("usb-probe: no MIDI in port matching %q: %v", pm, err)
	}

	s := &session{out: out, wait: *wait}
	stop, err := midi.ListenTo(in, func(msg midi.Message, _ int32) {
		b := msg.Bytes()
		if len(b) > 0 && b[0] == 0xF0 {
			s.mu.Lock()
			s.inbox = append(s.inbox, append([]byte(nil), b...))
			s.mu.Unlock()
		}
	}, midi.UseSysEx())
	if err != nil {
		log.Fatalf("usb-probe: listen: %v", err)
	}
	defer stop()

	log.Printf("%s probe on %q (read-only)", *device, pm)
	switch *device {
	case "ml10x":
		runML10X(s, *full, *op, *payloadStr)
	case "sl-2":
		runSL2(s, *rq1Model, *rq1Addr, *rq1Size)
	case "h90":
		if *h90TRPC || *h90Raw != "" || *h90Handshake || *h90Ping {
			runH90TRPC(s, *h90Raw, *h90Handshake, *h90Ping, byte(*h90Dev))
		} else {
			runH90(s)
		}
	default:
		log.Fatalf("usb-probe: unknown --device %q (want ml10x|sl-2|eq2|h90|opus)", *device)
	}
	log.Printf("done")
}

// runML10X issues the Morningstar editor's read opcodes and decodes the streamed
// TLV blocks (see the package doc + docs/research/ml10x.md).
func runML10X(s *session, full bool, op int, payloadStr string) {
	// Single-opcode mode: one op2 with an optional custom payload, isolated so
	// the device can't pipeline (used to map op2 -> reply block and to hunt for
	// the bank/preset selector byte).
	if op >= 0 {
		pl, err := parsePayload(payloadStr)
		if err != nil {
			log.Fatalf("usb-probe: %v", err)
		}
		req := buildRequest(byte(op), pl...)
		log.Printf("TX  op2=0x%02X payload=[%s]  %s", byte(op), hexs(pl), hexs(req))
		replies := s.send(req)
		if len(replies) == 0 {
			log.Printf("RX  (no reply)")
		}
		for _, r := range replies {
			log.Printf("RX  %s", decode(r))
		}
		return
	}

	ops := []byte{0x00, 0x01}
	if full {
		ops = readOps
	}
	for _, op2 := range ops {
		req := buildRequest(op2)
		log.Printf("TX  read op2=0x%02X  %s", op2, hexs(req))
		replies := s.send(req)
		if len(replies) == 0 {
			log.Printf("RX  (no reply) — is the web editor still holding the port?")
		}
		for _, r := range replies {
			log.Printf("RX  %s\n      [raw %d bytes]", decode(r), len(r))
		}
	}
	if !full {
		log.Printf("(pass --full to sweep every editor read opcode: %s)", hexOps(readOps))
	}
}

func hexOps(b []byte) string {
	s := make([]string, len(b))
	for i, x := range b {
		s[i] = fmt.Sprintf("0x%02X", x)
	}
	sort.Strings(s)
	return strings.Join(s, " ")
}

// --- SL-2 track: Roland address-based SysEx (Identity Request + RQ1) ----------

const rolandMfg = 0x41

// sl2Model is the SL-2's address-map model id (5 bytes), and sl2Dev its default
// device id — both confirmed from BOSS Tone Studio for SL-2 (product_setting.js:
// modelId '000000001D', deviceId '10') and verified live. The address/size fields
// are 4 bytes each.
var sl2Model = []byte{0x00, 0x00, 0x00, 0x00, 0x1D}

const sl2Dev = 0x10

// sl2Reg is one named SL-2 register to RQ1 (address is the literal wire address;
// addresses are 7-bit-safe and used verbatim — see docs/research/sl-2.md).
type sl2Reg struct {
	name string
	addr []byte // 4 bytes
	size []byte // 4 bytes
}

// sl2Regs is the default read-only readout: the editor-comm handshake registers,
// two SYSTEM values, and the temporary patch name. All are RQ1 reads.
var sl2Regs = []sl2Reg{
	{"EDITOR_COMM_LEVEL", []byte{0x7F, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x01}},
	{"EDITOR_COMM_REVISION", []byte{0x7F, 0x00, 0x00, 0x03}, []byte{0x00, 0x00, 0x00, 0x01}},
	{"SYSTEM_TEMPO", []byte{0x10, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x04}},
	{"SYSTEM_MIDI_CH", []byte{0x10, 0x00, 0x00, 0x08}, []byte{0x00, 0x00, 0x00, 0x01}},
	{"TEMP_PATCH_NAME", []byte{0x20, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x10}},
}

// identityRequest is the Universal Non-Realtime Identity Request, broadcast to
// all device IDs (7F). Any MIDI device should answer with an Identity Reply.
var identityRequest = []byte{0xF0, 0x7E, 0x7F, 0x06, 0x01, 0xF7}

// rolandChecksum is Roland's address+size(+data) checksum: the value that makes
// the 7-bit sum of all preceding address/size/data bytes come out to 0.
func rolandChecksum(b []byte) byte {
	sum := 0
	for _, x := range b {
		sum += int(x)
	}
	return byte((0x80 - (sum & 0x7F)) & 0x7F)
}

// buildRQ1 frames a Roland Data Request 1: F0 41 dev <model…> 11 <addr…> <size…>
// <checksum> F7. The checksum covers the address + size bytes.
func buildRQ1(dev byte, model, addr, size []byte) []byte {
	out := []byte{0xF0, rolandMfg, dev}
	out = append(out, model...)
	out = append(out, 0x11)
	out = append(out, addr...)
	out = append(out, size...)
	body := append(append([]byte{}, addr...), size...)
	return append(out, rolandChecksum(body), 0xF7)
}

// identityReply holds the fields of a decoded Roland Universal Identity Reply.
type identityReply struct {
	devID      byte
	mfg        byte
	familyCode []byte // 2 bytes, as transmitted (LSB, MSB)
	familyNum  []byte // 2 bytes
	swRev      []byte // 4 bytes
}

// decodeIdentityReply parses F0 7E dev 06 02 <mfg> … F7. Returns nil if the frame
// is not a (single-byte-manufacturer) Identity Reply.
func decodeIdentityReply(f []byte) *identityReply {
	if len(f) < 15 || f[0] != 0xF0 || f[1] != 0x7E || f[3] != 0x06 || f[4] != 0x02 {
		return nil
	}
	// Single-byte manufacturer id (Roland = 0x41); the 3-byte extended form
	// (mfg 0x00) is not expected from these pedals.
	return &identityReply{
		devID:      f[2],
		mfg:        f[5],
		familyCode: append([]byte{}, f[6:8]...),
		familyNum:  append([]byte{}, f[8:10]...),
		swRev:      append([]byte{}, f[10:14]...),
	}
}

// decodeSL2DT1 parses a DT1 data set against the known SL-2 header
// (F0 41 dev <5-byte model> 12 <4-byte addr> <data…> <ck> F7) and returns the
// address bytes and data bytes. Returns nil if the frame is not an SL-2 DT1.
func decodeSL2DT1(f []byte) (addr, data []byte) {
	hdr := 3 + len(sl2Model) + 1 // F0 41 dev | model | 12
	if len(f) < hdr+4+1+1 || f[0] != 0xF0 || f[1] != rolandMfg {
		return nil, nil
	}
	for i, m := range sl2Model {
		if f[3+i] != m {
			return nil, nil
		}
	}
	if f[3+len(sl2Model)] != 0x12 { // DT1 command
		return nil, nil
	}
	addr = f[hdr : hdr+4]
	data = f[hdr+4 : len(f)-2] // strip checksum + F7
	return addr, data
}

// decodeSL2 renders an SL-2 reply: an Identity Reply, a decoded DT1 data set, or
// any other Roland SysEx printed generically.
func decodeSL2(f []byte) string {
	if id := decodeIdentityReply(f); id != nil {
		mfg := fmt.Sprintf("0x%02X", id.mfg)
		if id.mfg == rolandMfg {
			mfg = "Roland(0x41)"
		}
		return fmt.Sprintf("IDENTITY dev=0x%02X mfg=%s familyCode=%s familyNum=%s swRev=%s  (RQ1/DT1 model id: %s)",
			id.devID, mfg, hexs(id.familyCode), hexs(id.familyNum), hexs(id.swRev), hexs(sl2Model))
	}
	if addr, data := decodeSL2DT1(f); addr != nil {
		return fmt.Sprintf("DT1 addr=%s data=%s ascii=%q", hexs(addr), hexs(data), ascii(data))
	}
	if len(f) >= 4 && f[0] == 0xF0 && f[1] == rolandMfg {
		body := f[3 : len(f)-1]
		return fmt.Sprintf("ROLAND dev=0x%02X body=%s ascii=%q", f[2], hexs(body), ascii(body))
	}
	return fmt.Sprintf("(non-Roland sysex) %s", hexs(f))
}

// runSL2 reads the SL-2's identity, then issues Roland RQ1 reads with the
// confirmed model id (00 00 00 00 1D). By default it reads the named registers in
// sl2Regs (editor-comm level/rev, system tempo + MIDI channel, temp-patch name)
// and decodes the DT1 replies. With --rq1-model/--rq1-addr/--rq1-size it does a
// single arbitrary RQ1 (use --rq1-model "00 00 00 00 1D" to read any address with
// the real model id). Read-only throughout — only RQ1, never a DT1 write.
func runSL2(s *session, rq1Model, rq1Addr, rq1Size string) {
	// 1. Identity Request.
	log.Printf("TX  Identity Request  %s", hexs(identityRequest))
	replies := s.send(identityRequest)
	if len(replies) == 0 {
		log.Printf("RX  (no Identity Reply) — is the SL-2 powered and is the port free?")
	}
	dev := byte(sl2Dev)
	for _, r := range replies {
		log.Printf("RX  %s", decodeSL2(r))
		if id := decodeIdentityReply(r); id != nil {
			dev = id.devID
		}
	}

	// 2a. Manual single RQ1 (explicit model id) for ad-hoc address probing.
	if rq1Model != "" {
		model, err := parsePayload(rq1Model)
		if err != nil {
			log.Fatalf("usb-probe: --rq1-model: %v", err)
		}
		addr, err := parsePayload(rq1Addr)
		if err != nil {
			log.Fatalf("usb-probe: --rq1-addr: %v", err)
		}
		size, err := parsePayload(rq1Size)
		if err != nil {
			log.Fatalf("usb-probe: --rq1-size: %v", err)
		}
		req := buildRQ1(dev, model, addr, size)
		log.Printf("TX  RQ1 model=%s addr=%s size=%s  %s", hexs(model), hexs(addr), hexs(size), hexs(req))
		got := s.send(req)
		if len(got) == 0 {
			log.Printf("RX  (no reply) — wrong model id or out-of-range address")
		}
		for _, r := range got {
			log.Printf("RX  %s", decodeSL2(r))
		}
		return
	}

	// 2b. Default: read the named registers with the confirmed model id.
	for _, reg := range sl2Regs {
		req := buildRQ1(dev, sl2Model, reg.addr, reg.size)
		log.Printf("TX  RQ1 %-20s addr=%s size=%s  %s", reg.name, hexs(reg.addr), hexs(reg.size), hexs(req))
		got := s.send(req)
		if len(got) == 0 {
			log.Printf("RX  (no reply)")
			continue
		}
		for _, r := range got {
			log.Printf("RX  %s%s", decodeSL2(r), sl2Annotate(reg, r))
		}
	}
}

// sl2Annotate adds a human-readable gloss for the registers whose encoding we
// know (MIDI channel is 0-indexed with 10=All; tempo is INTEGER4x4 ×0.1 BPM).
func sl2Annotate(reg sl2Reg, frame []byte) string {
	_, data := decodeSL2DT1(frame)
	if data == nil {
		return ""
	}
	switch reg.name {
	case "SYSTEM_MIDI_CH":
		if len(data) == 1 {
			if data[0] == 10 {
				return "  -> channel All/Omni"
			}
			return fmt.Sprintf("  -> channel %d", data[0]+1)
		}
	case "SYSTEM_TEMPO":
		if len(data) == 4 { // 4 nibbles, big-endian
			v := int(data[0])<<12 | int(data[1])<<8 | int(data[2])<<4 | int(data[3])
			return fmt.Sprintf("  -> %.1f BPM", float64(v)/10)
		}
	}
	return ""
}

// --- H90 track: Eventide documented system SysEx (Identity + TJ WANT) ----------
//
// Eventide publishes a system SysEx for the Factor/Space/H9 family (the "TJ"
// protocol: WANT requests -> DUMP replies, framed F0 1C 70 <dev> <cmd> ... F7,
// manufacturer 0x1C, model 0x70 — see docs/research/h90.md). This track sends the
// read-only "WANT" commands plus the Universal Identity Request and prints any
// reply. On the H90 the result is a clean negative: it answers none of them, which
// is the finding — the H90 does not honor the Factor-family SysEx over USB-MIDI.
// Strictly read-only: only Identity + WANT (never a VALUE_PUT or any write).

const (
	eventideMfg = 0x1C // Eventide MIDI manufacturer id
	h90Model    = 0x70 // Factor/H9-family model id (the H90 does NOT honor it; see note)
)

// h90Wants are the documented read-only "WANT" commands (each should provoke the
// matching DUMP). VALUE_WANT (0x3B) takes an ASCII-hex key; "0000" is the version
// key (tj_version_key). All are non-destructive.
var h90Wants = []struct {
	name string
	cmd  byte
	data []byte
}{
	{"TJ_SYSVARS_WANT", 0x4C, nil},
	{"TJ_PROGRAM_WANT", 0x4E, nil},
	{"TJ_PRESETS_WANT", 0x48, nil},
	{"VALUE_WANT(version key 0000)", 0x3B, []byte("0000")},
}

// buildEventideWant frames an Eventide proprietary message: F0 1C 70 <dev> <cmd>
// <data…> F7. dev 0x00 addresses all units.
func buildEventideWant(dev, cmd byte, data ...byte) []byte {
	out := []byte{0xF0, eventideMfg, h90Model, dev, cmd}
	out = append(out, data...)
	return append(out, 0xF7)
}

// decodeH90 renders an H90 reply: an Identity Reply (Eventide mfg 0x1C) or any
// Eventide proprietary frame (F0 1C 70 <dev> <cmd> …).
func decodeH90(f []byte) string {
	if id := decodeIdentityReply(f); id != nil {
		mfg := fmt.Sprintf("0x%02X", id.mfg)
		if id.mfg == eventideMfg {
			mfg = "Eventide(0x1C)"
		}
		return fmt.Sprintf("IDENTITY dev=0x%02X mfg=%s familyCode=%s familyMember=%s swRev=%s",
			id.devID, mfg, hexs(id.familyCode), hexs(id.familyNum), hexs(id.swRev))
	}
	if len(f) >= 6 && f[0] == 0xF0 && f[1] == eventideMfg {
		model, dev, cmd := f[2], f[3], f[4]
		body := f[5 : len(f)-1] // strip F7
		return fmt.Sprintf("EVENTIDE model=0x%02X dev=0x%02X cmd=0x%02X body=%s ascii=%q",
			model, dev, cmd, hexs(body), ascii(body))
	}
	return fmt.Sprintf("(non-Eventide sysex) %s", hexs(f))
}

// runH90 sends the Universal Identity Request and the documented Eventide TJ WANT
// reads, then reports replies. Read-only throughout.
func runH90(s *session) {
	any := false

	log.Printf("TX  Identity Request  %s", hexs(identityRequest))
	for _, r := range s.send(identityRequest) {
		any = true
		log.Printf("RX  %s", decodeH90(r))
	}

	for _, w := range h90Wants {
		req := buildEventideWant(0x00, w.cmd, w.data...)
		log.Printf("TX  %-28s %s", w.name, hexs(req))
		got := s.send(req)
		if len(got) == 0 {
			log.Printf("RX  (no reply)")
		}
		for _, r := range got {
			any = true
			log.Printf("RX  %s", decodeH90(r))
		}
	}

	if !any {
		log.Printf("No reply to the Identity Request or any Eventide TJ WANT command.")
		log.Printf("Confirmed: the H90 does NOT implement the documented Factor/H9-family")
		log.Printf("system SysEx over USB-MIDI — its USB-MIDI carries only PC/CC/Clock. The")
		log.Printf("mass-storage interface is a Recovery-Mode firmware volume (.os/.pak/.bam),")
		log.Printf("not a preset filesystem, so there is no non-destructive USB readback")
		log.Printf("channel. Preset/program files (.pgm90/.lst90) live host-side in H90")
		log.Printf("Control, not on the pedal. See docs/research/h90.md.")
	}
}

// --- H90 TRPC track: the H90 Control app protocol (scaffold) ------------------
//
// This is the modern Eventide "Tide RPC" (TRPC) protocol that H90 Control speaks
// to the pedal, recovered by static analysis of H90 Control.exe (see
// docs/research/h90.md "H90 Control protocol (static analysis)" and the catalog
// in docs/research/h90-control.yaml). In short: request/response RPC carried as
// MIDI SysEx (JUCE UMP SysEx7), FlatBuffers payloads, ids 7-bit-split, large
// payloads zlib-compressed (>100 bytes) and segmented.
//
// The SysEx envelope IS known: F0 1C 77 00 <4-byte 7-bit header> <FlatBuffers> F7
// (manufacturer 0x1C, model 0x77; status 0x02 = device error). Recovered from
// the binary and live-verified — see decodeDot9 / dot9Ping and h90.md. What is
// still NOT known statically: the per-operation Dot9MessageType opcode VALUES,
// so this track cannot synthesise a real getSystemParameters request yet.
//
//	--h90-ping            send the safe Dot9 ping (F0 1C 77 00 …) and decode the reply
//	--h90-raw "F0 .. F7"  send one captured/handcrafted SysEx and decode the reply
//	--h90-handshake       send the Universal Identity Request to --h90-dev, listen
//	--h90-dev <id>        Eventide SysEx device id (0x7F = broadcast)
//
// Strictly read-only intent: only send what the operator explicitly passes (a
// raw frame) or the standard Identity Request. Never a changePresetParameter or
// any TRPC write — those mutate the pedal (see the catalog's writes list).

// buildIdentityRequest is the Universal Non-Realtime Identity Request addressed
// to a specific device id (0x7F = broadcast to all units).
func buildIdentityRequest(dev byte) []byte {
	return []byte{0xF0, 0x7E, dev & 0x7F, 0x06, 0x01, 0xF7}
}

// dot9Model is the H90's Dot9/TRPC SysEx model id. The full prefix is
// F0 1C 77 — manufacturer 0x1C (Eventide), model 0x77 — recovered by static
// analysis of H90 Control.exe (the inbound parser gates on these three bytes;
// the build/parse helpers seed the header struct with the literal 0x771cf0) and
// CONFIRMED live: the pedal replies to F0 1C 77 … frames over its own USB-MIDI.
// See docs/research/h90.md "SysEx header recovered" and the live verification.
const dot9Model = 0x77

// dot9Ping is the safest possible Dot9 probe: the recovered prefix + a zeroed
// 8-byte header + EMPTY body. Opcode/type 0 with no payload cannot form any
// valid TRPC write (writes such as changePresetParameter need a payload), so the
// pedal rejects it with the error response (status 0x02) — which is exactly what
// verifies that the F0 1C 77 framing is honoured, non-destructively.
var dot9Ping = []byte{0xF0, eventideMfg, dot9Model, 0x00, 0x00, 0x00, 0x00, 0x00, 0xF7}

// decodeDot9 parses an H90 Dot9/TRPC reply (F0 1C 77 00 <h4 h5 h6 h7> <fb…> F7).
// The 4-byte header carries a 7-bit-split message id and a status byte (0x02 =
// device error); the body is a FlatBuffers buffer (first u32 = root uoffset).
// Returns "" if the frame is not a Dot9 frame.
func decodeDot9(f []byte) string {
	if len(f) < 9 || f[0] != 0xF0 || f[1] != eventideMfg || f[2] != dot9Model || f[len(f)-1] != 0xF7 {
		return ""
	}
	hdr := f[3:8] // 00 + four 7-bit header bytes
	body := f[8 : len(f)-1]
	status := hdr[4]
	note := ""
	switch status {
	case 0x02:
		note = "  (ERROR response, code 0x02)"
	}
	var root string
	if len(body) >= 4 {
		r := uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
		root = fmt.Sprintf(" fb_root_uoffset=%d", r)
	}
	return fmt.Sprintf("DOT9 hdr=%s status=0x%02X%s body(%dB)=%s%s",
		hexs(hdr), status, note, len(body), hexs(body), root)
}

// decodeH90TRPC renders a reply on the TRPC path: an Identity Reply, a decoded
// Dot9/TRPC frame (F0 1C 77 …), or a generic SysEx dump noting 7-bit cleanliness.
func decodeH90TRPC(f []byte) string {
	if id := decodeIdentityReply(f); id != nil {
		mfg := fmt.Sprintf("0x%02X", id.mfg)
		if id.mfg == eventideMfg {
			mfg = "Eventide(0x1C)"
		}
		return fmt.Sprintf("IDENTITY dev=0x%02X mfg=%s familyCode=%s familyMember=%s swRev=%s",
			id.devID, mfg, hexs(id.familyCode), hexs(id.familyNum), hexs(id.swRev))
	}
	if d := decodeDot9(f); d != "" {
		return d
	}
	if len(f) >= 3 && f[0] == 0xF0 && f[len(f)-1] == 0xF7 {
		body := f[1 : len(f)-1]
		clean := true
		for _, b := range body {
			if b&0x80 != 0 {
				clean = false
				break
			}
		}
		return fmt.Sprintf("SYSEX len=%d 7bit-clean=%v body=%s ascii=%q",
			len(f), clean, hexs(body), ascii(body))
	}
	return fmt.Sprintf("(non-sysex) %s", hexs(f))
}

// runH90TRPC drives the TRPC path: the safe Dot9 ping, a raw-frame replay, a
// handshake probe, or (with no sub-flag) a short explainer of what is missing.
func runH90TRPC(s *session, raw string, handshake, ping bool, dev byte) {
	any := false

	if ping {
		log.Printf("TX  Dot9 ping  %s", hexs(dot9Ping))
		got := s.send(dot9Ping)
		if len(got) == 0 {
			log.Printf("RX  (no reply) — wrong port (use the H90's OWN USB-MIDI, not a WIDI bridge)?")
		}
		for _, r := range got {
			any = true
			log.Printf("RX  %s", decodeH90TRPC(r))
		}
		log.Printf("A DOT9 reply with status 0x02 confirms the F0 1C 77 framing is honoured")
		log.Printf("(device rejected the empty/opcode-0 request) — see docs/research/h90.md.")
	}

	if handshake {
		req := buildIdentityRequest(dev)
		log.Printf("TX  Identity Request (dev=0x%02X)  %s", dev, hexs(req))
		for _, r := range s.send(req) {
			any = true
			log.Printf("RX  %s", decodeH90TRPC(r))
		}
		log.Printf("Note: the H90 does not answer the Universal Identity Request; it only")
		log.Printf("speaks its own Dot9 framing (F0 1C 77). Use --h90-ping to verify that.")
	}

	if raw != "" {
		b, err := parsePayload(raw)
		if err != nil {
			log.Fatalf("usb-probe: --h90-raw: %v", err)
		}
		if len(b) < 2 || b[0] != 0xF0 || b[len(b)-1] != 0xF7 {
			log.Printf("warning: --h90-raw is not a complete SysEx (expected F0 … F7)")
		}
		log.Printf("TX  raw  %s", hexs(b))
		got := s.send(b)
		if len(got) == 0 {
			log.Printf("RX  (no reply)")
		}
		for _, r := range got {
			any = true
			log.Printf("RX  %s", decodeH90TRPC(r))
		}
	}

	if !handshake && !ping && raw == "" {
		log.Printf("H90 TRPC path (read-only). The Dot9 SysEx prefix F0 1C 77 is recovered")
		log.Printf("and live-verified (see docs/research/h90.md). Use:")
		log.Printf("  --h90-ping               send the safe Dot9 ping and decode the reply")
		log.Printf("  --h90-handshake          send the Universal Identity Request and listen")
		log.Printf("  --h90-raw \"F0 .. F7\"     replay a captured/handcrafted SysEx frame")
		log.Printf("  --h90-dev <id>           target a specific Eventide SysEx id (default 0x7F)")
		return
	}

	if !any {
		log.Printf("No reply. Over the WIDI-bridged USB-MIDI the H90 stays silent on SysEx;")
		log.Printf("the Dot9/TRPC path works on the H90's OWN class-compliant USB-MIDI (the")
		log.Printf("port to H90 Control) or its own BLE-MIDI peripheral. See h90.md.")
	}
}

// --- EQ2 track: Source Audio Neuro HID protocol -------------------------------
//
// The Neuro editor talks to One Series pedals over a vendor HID interface, not
// USB-MIDI SysEx (see the package doc + docs/research/eq-2.md). This track reads
// device memory with the 0x36 dump command and decodes the 32-byte blocks. It is
// strictly read-only: it never sends 0x77 (preset select) or any write.

const (
	eqVID       = 0x29A4
	eqPID       = 0x0400
	eqCmdDump   = 0x36 // followed by a 3-byte big-endian address; dumps 32 bytes
	eqReportLen = 38   // EQ2 HID report length (1 header + 32 data + padding)
)

// findHidraw locates a /dev/hidrawN node by matching the sysfs HID_ID
// (bus:vendor:product, hex) against the given VID/PID. name is only used in the
// "not connected" error message.
func findHidraw(vid, pid uint32, name string) (string, error) {
	nodes, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
	want := fmt.Sprintf("%08X:%08X", vid, pid) // e.g. 000029A4:00000400
	for _, n := range nodes {
		ue, err := os.ReadFile(filepath.Join(n, "device", "uevent"))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToUpper(string(ue)), want) {
			return "/dev/" + filepath.Base(n), nil
		}
	}
	return "", fmt.Errorf("no hidraw node for VID:PID %04X:%04X (is the %s connected?)", vid, pid, name)
}

// findEQ2Hidraw locates the EQ2's /dev/hidrawN node by VID:PID.
func findEQ2Hidraw() (string, error) { return findHidraw(eqVID, eqPID, "EQ2") }

// hidDev is a minimal read/write wrapper over a Linux hidraw node (no cgo / no
// hidapi dependency — direct read/write/poll on the device fd).
type hidDev struct{ fd int }

func openHID(path string) (*hidDev, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return &hidDev{fd: fd}, nil
}

func (h *hidDev) close() { _ = unix.Close(h.fd) }

// writeReport sends an output report. hidraw expects byte 0 to be the report id
// (0x00 for devices with unnumbered reports, which the EQ2 is), so the command
// bytes are prefixed with 0x00.
func (h *hidDev) writeReport(cmd []byte) error {
	buf := append([]byte{0x00}, cmd...)
	_, err := unix.Write(h.fd, buf)
	return err
}

// readReport waits up to timeout for one input report; returns nil on timeout.
func (h *hidDev) readReport(timeout time.Duration) ([]byte, error) {
	pfd := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}}
	var n int
	var err error
	for { // retry across signal interruptions (EINTR)
		n, err = unix.Poll(pfd, int(timeout.Milliseconds()))
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, 64)
	m, err := unix.Read(h.fd, buf)
	if err != nil {
		return nil, err
	}
	return buf[:m], nil
}

// eqAddr3 splits a 24-bit address into 3 big-endian bytes (hi, mid, lo).
func eqAddr3(addr uint32) []byte { return []byte{byte(addr >> 16), byte(addr >> 8), byte(addr)} }

// dump issues a 0x36 read of 32 bytes at the given 24-bit address.
func (h *hidDev) dump(addr uint32, timeout time.Duration) ([]byte, error) {
	if err := h.writeReport(append([]byte{eqCmdDump}, eqAddr3(addr)...)); err != nil {
		return nil, err
	}
	return h.readReport(timeout)
}

func parseAddr(s string) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	var v uint64
	if _, err := fmt.Sscanf(s, "%x", &v); err != nil {
		return 0, fmt.Errorf("bad address %q: %w", s, err)
	}
	return uint32(v), nil
}

// parseRange parses a "from:to" pair of 24-bit hex addresses for scan mode.
func parseRange(s string) (uint32, uint32, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want \"from:to\" hex addresses, got %q", s)
	}
	lo, err := parseAddr(parts[0])
	if err != nil {
		return 0, 0, err
	}
	hi, err := parseAddr(parts[1])
	if err != nil {
		return 0, 0, err
	}
	if hi <= lo {
		return 0, 0, fmt.Errorf("range end 0x%X must be > start 0x%X", hi, lo)
	}
	return lo, hi, nil
}

// eqData returns the 32-byte data window of a reply report (byte[1:33]).
func eqData(rep []byte) []byte {
	if len(rep) >= 33 {
		return rep[1:33]
	}
	return rep
}

// runEQ2 reads EQ2 state over the Neuro HID interface. With --eq2-addr it dumps a
// single 24-bit address, with --eq2-scan it walks a range looking for text, and
// otherwise it sweeps the preset-name field of each slot. The default address
// math (base 0x80000, stride 0x1000, name offset 0x97) was confirmed live on the
// EQ2; override with the --eq2-base/-stride/-name-off flags for other layouts.
// asciiRun returns the longest run of consecutive printable-letter/digit bytes,
// used to flag windows that look like text (names) during a memory scan.
func asciiRun(b []byte) int {
	best, cur := 0, 0
	for _, c := range b {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == ' ' {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

func runEQ2(hidraw, addrStr, scanStr string, base, stride, nameOff uint, count int, wait time.Duration) {
	path := hidraw
	if path == "" {
		p, err := findEQ2Hidraw()
		if err != nil {
			log.Fatalf("usb-probe: %v", err)
		}
		path = p
	}
	h, err := openHID(path)
	if err != nil {
		log.Fatalf("usb-probe: open %s: %v\n  (hidraw nodes are root-only; grant access with `sudo setfacl -m u:$USER:rw %s`)", path, err, path)
	}
	defer h.close()
	log.Printf("eq2 Neuro HID probe on %s (read-only: only the 0x36 dump is sent)", path)

	// HID reads return promptly; cap the per-read poll so a 128-slot sweep over
	// non-responding addresses can't stall for minutes.
	rt := wait
	if rt > 400*time.Millisecond {
		rt = 400 * time.Millisecond
	}

	// Single-address mode: dump 32 bytes at one 24-bit address (for mapping the
	// EQ2's memory layout when it differs from the C4's).
	if addrStr != "" {
		a, err := parseAddr(addrStr)
		if err != nil {
			log.Fatalf("usb-probe: --eq2-addr: %v", err)
		}
		cmd := append([]byte{eqCmdDump}, eqAddr3(a)...)
		log.Printf("TX  0x36 dump addr=0x%06X  %s", a, hexs(cmd))
		rep, err := h.dump(a, rt)
		if err != nil {
			log.Fatalf("usb-probe: dump: %v", err)
		}
		if len(rep) == 0 {
			log.Printf("RX  (no reply within %v)", rt)
			return
		}
		d := eqData(rep)
		log.Printf("RX  hdr=0x%02X len=%d data=%s", rep[0], len(rep), hexs(d))
		log.Printf("    ascii=%q", ascii(d))
		return
	}

	// Scan mode: walk a "from:to" range in 32-byte windows in a single process
	// (no per-read process spawn) and print windows that look like text. Used to
	// discover where the EQ2 keeps preset names / parameter blocks.
	if scanStr != "" {
		lo, hi, err := parseRange(scanStr)
		if err != nil {
			log.Fatalf("usb-probe: --eq2-scan: %v", err)
		}
		log.Printf("scanning 0x%06X..0x%06X in 32-byte windows (printing windows with a >=3 ASCII run)", lo, hi)
		hits := 0
		for a := lo; a < hi; a += 32 {
			rep, err := h.dump(a, rt)
			if err != nil {
				log.Printf("  0x%06X dump error: %v", a, err)
				continue
			}
			if len(rep) == 0 {
				continue
			}
			d := eqData(rep)
			if asciiRun(d) >= 3 {
				hits++
				log.Printf("0x%06X  %q  [% X]", a, ascii(d), d)
			}
		}
		log.Printf("eq2 scan done: %d window(s) with readable text", hits)
		return
	}

	// Preset-name sweep: read the name field of each preset slot.
	log.Printf("sweeping %d preset-name slots: base=0x%X stride=0x%X name_off=0x%X",
		count, base, stride, nameOff)
	active, replies := 0, 0
	for i := 0; i < count; i++ {
		a := uint32(base) + uint32(i)*uint32(stride) + uint32(nameOff)
		rep, err := h.dump(a, rt)
		if err != nil {
			log.Printf("  slot %d dump error: %v", i, err)
			continue
		}
		if len(rep) == 0 {
			continue // no reply for this slot
		}
		replies++
		name := eqData(rep)
		if len(name) == 0 || name[0] == 0xFF {
			continue // empty slot (name filled with 0xFF)
		}
		active++
		log.Printf("slot %3d addr=0x%06X  name=%q  [% X]", i, a, ascii(name), name)
	}
	log.Printf("eq2: %d/%d slots replied; %d non-empty names", replies, count, active)
	if replies == 0 {
		log.Printf("No HID replies at all — the device may not answer 0x36, or the report")
		log.Printf("layout differs. Confirm the node and try a single --eq2-addr dump.")
	} else if active == 0 {
		log.Printf("Replies arrived but no ASCII names decoded — the EQ2 preset memory map")
		log.Printf("(base/stride/name-offset) likely differs from the C4's. Sweep addresses")
		log.Printf("with --eq2-addr to find where readable names begin, then set the --eq2-* flags.")
	}
}

// --- Opus track: Two Notes Torpedo Remote HID pipe ----------------------------
//
// The Opus exposes ONLY a vendor HID interface (no USB-MIDI/CDC/audio): a raw
// 64-byte bidirectional pipe, usage page 0xFF00, no report IDs, no FEATURE
// reports — interrupt EP 0x01 OUT / 0x81 IN (descriptor decoded in
// docs/research/usb.md + opus.md). Torpedo Remote drives this pipe with a
// proprietary, undocumented command set, and the editor is Mac/Win/iOS only, so
// the semantic layout is blocked on a non-Linux capture.
//
// Because the command bytes are unknown and a wrong write could change device
// state, this track NEVER synthesises a request. It only:
//   - listens (default): drains input reports the Opus emits on its own (e.g.
//     front-panel/preset changes), printing the raw 64-byte frames; sends nothing.
//   - --opus-raw "F0 .." : replays exactly ONE operator-supplied captured frame
//     (padded/truncated to 64 bytes) and dumps the reply, for replaying a frame
//     lifted from a Torpedo Remote capture.

const (
	opusVID       = 0x0483 // STMicroelectronics (the Opus is an STM32-based device)
	opusPID       = 0xA334
	opusReportLen = 64 // 64-byte in/out interrupt reports, no report IDs
)

func findOpusHidraw() (string, error) { return findHidraw(opusVID, opusPID, "Opus") }

// runOpus opens the Opus HID pipe and either listens read-only (default) or
// replays one operator-supplied captured frame. It never invents request bytes.
func runOpus(hidraw, rawHex string, wait time.Duration) {
	path := hidraw
	if path == "" {
		p, err := findOpusHidraw()
		if err != nil {
			log.Fatalf("usb-probe: %v", err)
		}
		path = p
	}
	h, err := openHID(path)
	if err != nil {
		log.Fatalf("usb-probe: open %s: %v\n  (hidraw nodes are root-only; grant access with `sudo setfacl -m u:$USER:rw %s`)", path, err, path)
	}
	defer h.close()
	log.Printf("opus Torpedo Remote HID probe on %s (read-only)", path)

	// Replay mode: send exactly one operator-supplied frame (a captured Torpedo
	// Remote request) and dump whatever comes back.
	if rawHex != "" {
		b, err := parsePayload(rawHex)
		if err != nil {
			log.Fatalf("usb-probe: --opus-raw: %v", err)
		}
		frame := make([]byte, opusReportLen) // pad/truncate to a 64-byte output report
		copy(frame, b)
		log.Printf("TX  raw (%d bytes -> 64)  %s", len(b), hexs(frame))
		if err := h.writeReport(frame); err != nil {
			log.Fatalf("usb-probe: write: %v", err)
		}
		rt := wait
		if rt > 1*time.Second {
			rt = 1 * time.Second
		}
		got := 0
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			rep, err := h.readReport(rt)
			if err != nil {
				log.Printf("RX  read error: %v", err)
				break
			}
			if len(rep) == 0 {
				continue
			}
			got++
			log.Printf("RX  len=%d  %s", len(rep), hexs(rep))
			log.Printf("    ascii=%q", ascii(rep))
		}
		if got == 0 {
			log.Printf("RX  (no reply) — wrong frame, or this request expects no response")
		}
		return
	}

	// Default: listen-only. Drain input reports until the wait window elapses;
	// the Opus emits these when its state changes (e.g. a front-panel edit or a
	// preset recall over MIDI). Sends nothing — purely an observation window.
	log.Printf("listening %v for input reports (read-only; sends NOTHING).", wait)
	log.Printf("change a preset / turn a knob on the Opus, or recall a preset over MIDI,")
	log.Printf("to provoke a report. Use --opus-raw to replay a captured Torpedo Remote frame.")
	got := 0
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		rep, err := h.readReport(time.Until(deadline))
		if err != nil {
			log.Printf("read error: %v", err)
			break
		}
		if len(rep) == 0 {
			continue // poll timed out -> window elapsed
		}
		got++
		log.Printf("RX  len=%d  %s", len(rep), hexs(rep))
		log.Printf("    ascii=%q", ascii(rep))
	}
	log.Printf("opus: %d input report(s) observed.", got)
	if got == 0 {
		log.Printf("No unsolicited reports — the Opus likely only replies to a (proprietary)")
		log.Printf("Torpedo Remote request. The command layout is undocumented and the editor")
		log.Printf("is Mac/Win/iOS only, so decode it from a capture: run Torpedo Remote on a")
		log.Printf("Mac/Win host (or Android/iOS over BLE), snoop the HID traffic, then replay")
		log.Printf("a captured request here with --opus-raw. See docs/research/opus.md.")
	}
}
