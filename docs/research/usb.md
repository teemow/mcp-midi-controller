# USB readback — capture tooling & device inventory

Cross-cutting note for the USB readback-protocol research: how to **read real
device state** off each USB-connected pedal, so a later phase can verify what
BLE-MIDI writes actually landed (today `verify_control` only does a MIDI echo —
see `internal/engine/feedback.go`). This note covers the two foundations every
per-device track builds on:

1. the **shared capture tooling** (the four channels below), and
2. the **USB descriptor inventory** per device (interfaces, endpoints, MIDI
   jacks) — the gold reference for which transport carries device state.

The actual per-device protocol findings (SysEx maps, CDC framing, HID feature
reports, on-disk file formats) live in the per-device notes
(`docs/research/<device>.md`) and are filled in by their own research tracks.

> **Public vs. private.** Descriptor *structure* (classes, endpoints, MIDI jack
> layout, report sizes) is generic to the device and stays here. Anything
> installation-specific — serial numbers, bus/port topology, ALSA card numbers,
> `/dev` node assignments — lives in `docs/private/rig.md` (gitignored), with the
> raw `lsusb -v` dumps under `docs/private/usb-descriptors/` (also gitignored).

## What is physically connected

A 3-stage Genesys Logic hub (`05e3:0610`) fans out to five pedals. The **Boss
MD-200 has no USB port** and stays BLE-only. Two Notes Opus is **HID-only**; the
other four expose USB-MIDI (class 1 / subclass 3, MIDIStreaming).

| Device | VID:PID | USB function(s) | Verification channel to research |
|--------|---------|-----------------|----------------------------------|
| Two Notes Opus | `0483:a334` | HID only | **Torpedo Remote 64-byte HID vendor pipe** (transport confirmed; proprietary command layout blocked on a Mac/Win/iOS capture) |
| Eventide H90 | `1b12:0041` | Mass Storage + USB-Audio(MIDI) | **none usable** — SysEx not implemented; mass storage is a Recovery-Mode firmware volume |
| Morningstar ML10X | `331b:0008` | CDC-ACM serial + USB-MIDI | **USB-MIDI SysEx editor protocol** (confirmed; *not* CDC) |
| Source Audio EQ2 | `29a4:0400` | USB-MIDI + HID | **Neuro HID protocol** (confirmed; USB-MIDI carries only PC/CC) |
| Boss SL-2 | `0582:02af` | USB-MIDI only | **Roland address-based SysEx editor** (confirmed; model id `00 00 00 00 1D`, RQ1/DT1, full patch map) |

## USB descriptor inventory (generic, from `lsusb -v` + sysfs)

Captured with `scripts/usb-capture.sh descriptors` (read-only). Endpoint
addresses are per-interface and stable across the device family; string
descriptors resolve from the sysfs cache even without root. The only field that
needs root (or hidraw) is the **HID report-descriptor body** — see the gap note.

### Two Notes Opus — `0483:a334` (HID only)

- 1 interface: **HID** (class 03/00/00), 2 interrupt endpoints.
  - EP `0x81` IN / `0x01` OUT, interrupt, 64-byte reports.
- No USB-MIDI, no CDC, no audio. All readback must go over HID
  (Torpedo Remote protocol).
- **Report descriptor (36 B, decoded):** vendor Usage Page `0xFF00`, one
  Application collection, **no report IDs and no FEATURE reports**. One **64-byte
  Input** report (usages `0x02..0x41`) and one **64-byte Output** report (usages
  `0x42..0x81`), each field Report Size 8 / Logical 0..127. So HID is a raw
  64-byte bidirectional vendor pipe (Input is flagged *Relative*, Output
  *Absolute*) — i.e. write a request out EP `0x01`, read the reply on EP `0x81`.
  Readback rides the **Input** report, *not* a HID FEATURE report (the plan's
  "feature-report" wording is inaccurate). Full Torpedo Remote write-up in
  `docs/research/opus.md`.

### Eventide H90 — `1b12:0041` (Mass Storage + USB-Audio/MIDI), high speed

- Interface 0: **Mass Storage** (08/06/50, Bulk-Only). EP `0x81` IN / `0x01`
  OUT, bulk, 512-byte. Backs `/dev/sdX` once the pedal enters file-transfer mode.
- Interface 1: **Audio Control** (01/01), 0 endpoints (named "H90 Pedal").
- Interface 2: **MIDIStreaming** (01/03). 2 IN jacks + 2 OUT jacks; EP `0x82`
  IN / `0x02` OUT, bulk, 512-byte.
- **Readback (blocked — both candidate routes are dead ends):**
  - USB-MIDI SysEx: the H90 does **not** answer the Universal Identity Request or
    the documented Eventide Factor/H9 system SysEx (`F0 1C 70 …` TJ WANT reads);
    confirmed live via `cmd/usb-probe --device h90` (read-only, zero replies). Its
    USB-MIDI carries only standard PC/CC/Clock.
  - Mass storage: the LUN has **no medium in normal use** (`/dev/sda` = 0
    sectors). It only mounts in **Recovery Mode** (Select+Perform+QK1 at power-on)
    as a firmware-install volume (`.os`/`.pak`/`.bam`), and writing to it replaces
    firmware — it is **not** a preset filesystem.
  - Preset/program files (`.pgm90` Program, `.lst90` List, device backup) live
    **host-side in H90 Control**, not on a device volume. Full detail in
    `docs/research/h90.md`.

### Morningstar ML10X — `331b:0008` (CDC-ACM + USB-MIDI)

- Interface 0: **CDC Communications / ACM** (02/02/01). Notification EP `0x82`
  IN, interrupt, 16-byte. (`CDC ACM`, `CDC Union`, bcdCDC 1.10.)
- Interface 1: **CDC Data** (0a). EP `0x03` OUT / `0x83` IN, bulk, 64-byte →
  `/dev/ttyACM0`. This is the **editor serial channel** (richest readback).
- Interface 2: **MIDIStreaming** (01/03). 2 IN + 2 OUT jacks; EP `0x04` OUT /
  `0x84` IN, bulk, 64-byte. **This is the editor's channel** — the Morningstar
  web editor reads/writes config as SysEx over USB-MIDI here (Web MIDI), *not*
  over the CDC port. See `docs/research/ml10x.md` for the decoded framing.
- **Readback (confirmed):** the editor protocol is Morningstar SysEx
  `F0 00 21 24 07 00 <op1> <op2> … <cksum> F7` (model id `0x07`) on the USB-MIDI
  interface. The CDC-ACM port stays silent during an editor read; it is **not**
  the readback channel for config (revises the earlier "CDC editor protocol"
  assumption). Full protocol + the full-config read flow: `docs/research/ml10x.md`.

### Source Audio EQ2 — `29a4:0400` (USB-MIDI + HID)

- Interface 0: **Audio Control** (01/01), 0 endpoints.
- Interface 1: **MIDIStreaming** (01/03). 1 embedded + 1 external IN jack,
  1 embedded + 1 external OUT jack; EP `0x82` IN / `0x02` OUT, bulk, 64-byte.
- Interface 2: **HID** (03/00/00). EP `0x81` IN / `0x01` OUT, interrupt,
  38-byte reports (`wDescriptorLength` 34). The Neuro alternate transport.
- **Report descriptor (34 B, decoded):** vendor Usage Page `0xFFA0`, one
  Application collection, **no report IDs**. One **38-byte Input** report
  (usage `0x03`) and one **38-byte Output** report (usage `0x04`), each field
  Report Size 8 / Logical 0..255 / Absolute. A fixed 38-byte bidirectional
  vendor pipe matching the interrupt endpoints.
- **Readback (confirmed): the Neuro channel is HID, *not* USB-MIDI SysEx.** The
  USB-MIDI interface only carries standard PC/CC; the Neuro Desktop editor reads
  device state over this HID pipe with `0x36 <24-bit addr>` (dump 32 bytes) and
  selects presets with `0x77 <preset>` (a write). Reverse-engineered on the
  sibling C4 Synth (same `0x29A4` framework) and confirmed live on the EQ2: the
  128 preset blocks live at `0x080000` (stride `0x1000`), names at offset `0x097`,
  with a 10-band frequency table inside each block. Full protocol + memory map:
  `docs/research/eq-2.md`. `cmd/usb-probe --device eq2` speaks it over hidraw.

### Boss SL-2 — `0582:02af` (USB-MIDI only)

- Interface 0: **Audio Control** (01/01), 0 endpoints.
- Interface 1: **MIDIStreaming** (01/03, "Generic Audio MIDI 1.0"). 2 IN + 2
  OUT jacks; EP `0x03` OUT / `0x84` IN, bulk, 64-byte.
- Only channel is USB-MIDI → readback via **Roland/Boss SysEx**.
- **The USB port is SysEx/editor-only: it ignores channel-voice CC.** Verified
  live — CC#81 (tap) sent to `hw:6,0` left the tempo unchanged, whereas the same
  CC over BLE/TRS moved it. So RQ1/DT1 readback/writeback goes over USB, but live
  CC control (on/off, EXP, tap) must go over the BLE/TRS path, not USB.
- **Readback + writeback: fully mapped (confirmed live).** Model id
  **`00 00 00 00 1D`** (5 bytes), device id `0x10`, 4-byte addr + 4-byte size,
  standard Roland RQ1/DT1 + checksum. The full address map (SYSTEM, temp patch,
  88 stored patterns, command block) was decompiled from BOSS Tone Studio for
  SL-2 and verified against the rig. The earlier "blocked" note was a red herring
  — the probe had used a 4-byte model id. Details + write flow:
  `docs/research/sl-2.md`; extracted reference under `docs/private/sl2-bts/`.
- **DT1 *writes* are NOT USB-exclusive.** Confirmed live: a DT1 sent over BLE
  (→ WIDI hub → TRS MIDI IN) changes the parameter (verified by USB readback of
  `EXP_FUNC` `02→04→02`). So the SL-2 can be fully reprogrammed over **BLE MIDI**
  with no laptop/USB host. **RQ1 readback still requires USB** (TRS is IN-only, no
  MIDI OUT), so BLE editing is open-loop (write absolute values, can't read back).

## Shared capture tooling

All four channels are wrapped by `scripts/usb-capture.sh` (read-only by design;
nothing writes to a device). Run with no args (or `descriptors`) for the
descriptor dump; the other subcommands are the live-capture channels.

### 0. One-time privileged setup

`usbmon` (raw USB capture) needs the kernel module loaded and SysEx snooping
needs `alsa-utils` (`amidi`). Both require root once:

```bash
sudo scripts/usb-capture.sh setup   # modprobe usbmon; install alsa-utils; group hints
```

After setup, add yourself to `wireshark` (usbmon access), `audio`, and `uucp`
(for `/dev/ttyACM*`) groups and re-login to capture without `sudo`.

### 1. Descriptors (gold reference) — `descriptors`

```bash
scripts/usb-capture.sh descriptors            # all rig devices -> docs/private/usb-descriptors/
sudo scripts/usb-capture.sh descriptors       # also resolves HID report descriptors
```

Dumps `lsusb -v`, sysfs string descriptors, the interface class map, and any
bound HID report descriptor per device. This is step 1 of every per-device track.

### 2. Raw USB — usbmon + Wireshark — `usbmon`

For capturing the *vendor editor's* traffic (the gold path: diff a known state
change against the wire bytes), or any non-MIDI/HID transfer.

```bash
scripts/usb-capture.sh usbmon 3 30            # capture bus 3 for 30s -> .pcapng
wireshark docs/private/usb-descriptors/usbmon-bus3-*.pcapng
```

Useful Wireshark filters: `usb.transfer_type==URB_BULK` (MIDI/CDC/mass-storage),
`usb.transfer_type==URB_INTERRUPT` (HID), `usb.src=="3.18.2"` to isolate one
device's endpoint. Find the bus with `lsusb` (all rig devices are on bus 3 here).

### 3. USB-MIDI SysEx snoop — amidi — `midi`

For the SysEx-based readbacks (SL-2 Identity/RQ1; the H90 system-SysEx probe,
which came back empty). `amidi -d` dumps incoming bytes; send a request from a
second `amidi -S` (or the project's transport) and watch the reply.

```bash
scripts/usb-capture.sh midi                   # list ALSA rawmidi ports (amidi -l)
scripts/usb-capture.sh midi hw:5,0,0          # dump SysEx/MIDI from one port
```

The pedals enumerate as ALSA rawmidi cards (e.g. H90/ML10X/EQ2/SL-2); the live
card numbers for this rig are in `docs/private/rig.md`.

### 4. CDC serial (ML10X) — `serial`

```bash
scripts/usb-capture.sh serial /dev/ttyACM0    # stty line settings + raw byte snoop
```

> **Note (revised):** the ML10X CDC-ACM port turned out **not** to be the
> editor's channel. The Morningstar web editor talks to the ML10X over
> **USB-MIDI (Web MIDI)**, not Web Serial — the CDC line is silent during a
> config read. The capture that established this used **usbmon** (channel 2),
> not the serial snoop; decode it with `tshark -e usbaudio.midi.event` and
> reassemble the F0…F7 frames. The decoded protocol lives in
> `docs/research/ml10x.md`; `cmd/usb-probe --port ML10X` speaks it directly over
> ALSA rawmidi. The `serial` channel is kept for any future device whose editor
> genuinely uses Web Serial.

## Editor-app constraint (where the gold-path capture must happen)

Only the **Morningstar editor is web-based** (Linux Chrome, Web Serial); its
traffic is capturable directly on this host. The other vendor editors are
Mac/Win/iOS only:

| Device | Editor | Platform | Capture plan |
|--------|--------|----------|--------------|
| ML10X | Morningstar web editor | Linux Chrome | **done** — usbmon capture of USB-MIDI SysEx (Web MIDI, *not* Web Serial); decoded in `ml10x.md` |
| EQ2 | Neuro Desktop | Mac/Win | **not needed** — Neuro is a vendor **HID** protocol, reverse-engineered (C4 sibling) and confirmed directly on Linux via hidraw; `cmd/usb-probe --device eq2` |
| SL-2 | BOSS Tone Studio | Mac/Win | **protocol fully decompiled from the editor** — RQ1/DT1 work natively on Linux (model id `00 00 00 00 1D`); no VM/capture needed |
| H90 | H90 Control | Mac/Win/iOS | **blocked** — SysEx not implemented (probed) and mass storage is a Recovery-Mode firmware volume, not presets; preset files (`.pgm90`/`.lst90`) are host-side only |
| Opus | Torpedo Remote | Mac/Win desktop (USB), Android/iOS (BLE) | **transport done, decode blocked** — HID pipe confirmed + `cmd/usb-probe --device opus` opens it read-only; the proprietary command bytes need a Torpedo Remote capture (HID on a Mac/Win host, or BLE from the mobile app). Desktop editor does not run on Linux. (`docs/research/opus.md`) |

## HID report descriptors (resolved via hidraw)

`lsusb -v` prints HID report descriptors as `** UNAVAILABLE **` even as root,
because the kernel `usbhid` driver claims the Opus/EQ2 HID interfaces and `lsusb`
won't detach it. The descriptors are instead read from sysfs once `usbhid` is
bound — `scripts/usb-capture.sh descriptors` pulls them from
`/sys/.../<intf>/<hidbus-id>/report_descriptor` and includes the hex in the dump.
Both decode to simple **raw vendor pipes with no report IDs and no FEATURE
reports** (see the Opus/EQ2 inventory entries above): Opus = 64-byte in/out (page
`0xFF00`), EQ2 = 38-byte in/out (page `0xFFA0`). These are transport envelopes,
not semantic maps — the actual command/parameter layout inside them is the
per-device track's job. For the **EQ2 this job is done**: the envelope carries
the Neuro `0x36`/`0x77` commands (`docs/research/eq-2.md`). For the **Opus** the
envelope is confirmed (write a request to EP `0x01`, read the reply on EP `0x81`;
readback rides the Input report, **not** a HID FEATURE report) but the Torpedo
Remote command bytes inside it are proprietary and **blocked on a Mac/Win/iOS
capture** — `cmd/usb-probe --device opus` opens the pipe read-only (listen +
`--opus-raw` replay). See `docs/research/opus.md`.

## Status

| Track | Capture channel | Status |
|-------|-----------------|--------|
| Descriptor inventory (all devices) | descriptors | **confirmed** (this note) |
| Shared tooling (usbmon/amidi/serial/web-serial) | all four | **set up** (`scripts/usb-capture.sh`) |
| HID report descriptors (Opus, EQ2) | descriptors (hidraw) | **confirmed** (raw vendor pipes) |
| ML10X editor protocol decode | usbmon + `cmd/usb-probe` | **confirmed** — USB-MIDI SysEx, model `0x07`, TLV blocks (`docs/research/ml10x.md`) |
| SL-2 readback decode | `cmd/usb-probe --device sl-2` + `amidi` | **confirmed** — identity + RQ1/DT1 with model id `00 00 00 00 1D`; full patch address map decompiled from BOSS Tone Studio (`docs/research/sl-2.md`) |
| EQ2 readback decode | `cmd/usb-probe --device eq2` (hidraw) | **confirmed** — Neuro **HID** (`0x36` dump), preset blocks `0x080000`/stride `0x1000`, names at `0x097`, band-frequency table decoded (`docs/research/eq-2.md`) |
| H90 readback decode | `cmd/usb-probe --device h90` + `amidi` | **blocked (resolved as no-channel)** — Identity + Eventide TJ WANT all unanswered over USB-MIDI; mass storage is a Recovery-Mode firmware volume, not presets; preset files (`.pgm90`/`.lst90`) host-side only (`docs/research/h90.md`) |
| Opus readback decode | `cmd/usb-probe --device opus` (hidraw) | **transport confirmed; semantic layout blocked on capture** — raw 64-byte vendor HID pipe (page `0xFF00`, no report IDs, no FEATURE reports); Torpedo Remote command bytes are proprietary, editor is Mac/Win/iOS only. Probe is read-only (listen + `--opus-raw` replay) (`docs/research/opus.md`) |
