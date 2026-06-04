# Source Audio EQ2 — MIDI implementation research

Research note backing `internal/device/definitions/eq-2.yaml`. The EQ2
(Programmable EQ, model SA270) is a Source Audio **One Series** pedal with
**full MIDI implementation**: 128 presets recallable by Program Change, and
effectively every DSP parameter reachable by Control Change. Unlike the Boss
pedals, the EQ2's CC map is **remappable** (global, via the Neuro editor), so
the default CC numbers are a starting point rather than a fixed contract.

## Sources

- **EQ2 User Guide (PDF)** — Source Audio (`SA270`, v4.7):
  <https://sourceaudio.net/uploads/1/1/5/1/115104065/eq2manualv4_7.pdf>
- **EQ2 product page** (features, links to manual + MIDI Implementation Guide):
  <https://sourceaudio.net/products/eq2>
- **EQ2 User Guide mirror** (full text used for the MIDI section below):
  <https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html>
- **Source Audio MIDI / Neuro FAQ** (channel + MIDI-map editing, clock sync):
  <https://sourceaudio.net/pages/faq>
- **Retailer feature summary** (cross-check of CC 103/104 preset recall):
  <https://www.pitbullaudio.com/source-audio-eq2-programmable-eq-guitar-effects-pedal-w-midi-in-thru.html>

> **The per-parameter default CC table is published only in the separate "EQ2
> Programmable EQ MIDI Implementation Guide"** (linked at the bottom of the EQ2
> product page), which is a download and not reproduced in the user guide.
> The confirmed facts below come from the user guide and product pages; the full
> default CC→parameter list is marked `# TODO` in the YAML and is best captured
> per-rig via **MIDI-learn** or read from the Implementation Guide.

## Transport / MIDI ports

- The EQ2 has **5-pin DIN MIDI IN and OUT/THRU** plus a **mini-USB** port
  (class-compliant USB-MIDI). It also accepts MIDI through a **Neuro Hub**
  connected to the Control Input.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- In this project it is reached over the DIN chain behind a BLE-MIDI hub, so it
  is a `blemidi` device addressed by `(endpoint, channel)`.

## Preset recall (Program Change + CC)

- The EQ2 stores **128 presets**, **all 128 recallable via MIDI Program
  Change** (PC). On the pedal itself only 4 (or 8 in Preset Extension Mode) are
  reachable by footswitch; MIDI reaches all 128. Modeled as `program_change`
  `0-127`.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- Presets are also recallable via **CC#103 and CC#104** (per the product
  feature list). The exact semantics of the pair (e.g. select vs. bank, or
  up/down) are **not** documented in the user guide — `# TODO: confirm` against
  the Implementation Guide. PC is the clean, unambiguous recall path and is what
  the YAML models.
  [feature summary](https://www.pitbullaudio.com/source-audio-eq2-programmable-eq-guitar-effects-pedal-w-midi-in-thru.html)
- Tip from the manual: to recall a preset **with the effect bypassed**, save the
  preset in its bypassed state; recalling loads the stored settings with the
  effect off until engaged. (Useful for "queue up but don't engage" scenes.)

## Parameter control (CC) — remappable, default map in the guide

- "Many of the EQ2's parameters (even those not assigned to a control knob) are
  directly accessible via MIDI CC." The pedal ships **pre-mapped to a default
  set of CC numbers**; the full list + ranges is in the **MIDI Implementation
  Guide** download.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- **CC mapping is global and user-remappable** via the **Neuro Desktop Editor**
  (`Device → Edit Device MIDI Map`): any of the 128 CC numbers can be assigned
  to any parameter. Custom maps are **not per-preset** — they apply globally.
  [faq](https://sourceaudio.net/pages/faq)
- Consequence for this project: the EQ2 is the canonical case for the design's
  **MIDI-learn / generic-CC** story. Rather than hard-coding the vendor default
  numbers (which a user may have remapped), the recommended path is to capture
  the actual CC per parameter with `learn_start`/`learn_capture`, or to transcribe
  the Implementation Guide into the YAML once and keep it as the source of truth
  that the Neuro map must match (mirrors the AUM convention model).

### Default CC map (transcribed verbatim from the MIDI Mapping Table)

The factory default CC numbers come from the official **"EQ2 Programmable
Equalizer MIDI Mapping Table"** (the Implementation Guide download). The full
map is now transcribed into `eq-2.yaml`; the table below is the authoritative
reference. Remember the map is **globally remappable** in Neuro, so a rig that
remapped it must be restored or re-bound via MIDI-learn.

| Parameter | CC | Range | Encoding |
|-----------|----|-------|----------|
| Master Volume (Output) | 0 | 0–127 | 0 = −∞, 64 = unity, 127 = +12 dB |
| Input 1 Trim | 1 | 0–127 | 0 = −∞, 64 = unity, 127 = +6 dB |
| Input 2 Trim | 2 | 0–127 | 0 = −∞, 64 = unity, 127 = +6 dB |
| Ch1 Band 1–10 Level | 3–12 | 0–36 | 0 = −18 dB, 18 = 0 dB, 36 = +18 dB |
| Ch2 Band 1–10 Level | 13–22 | 0–36 | " |
| Ch1 Band 1–10 Frequency | 23–32 | 0–127 | 20 Hz–20 kHz, log spaced |
| Ch2 Band 1–10 Frequency | 33–42 | 0–127 | " |
| Ch1 Band 1–10 Q | 43–52 | 0–95 | 0 = 0.5, 5 = 1.0 (default), 95 = 10.0 |
| Ch2 Band 1–10 Q | 53–62 | 0–95 | " |
| Ch1 Gain | 69 | 0–36 | 0 = −18 dB, 18 = 0 dB, 36 = +18 dB |
| Ch2 Gain | 70 | 0–36 | " |
| Ch1 Input High Pass | 71 | 0–28 | 0 = 10 Hz, 28 = 80 Hz (0.5 Hz/step) |
| Ch2 Input High Pass | 72 | 0–28 | " |
| Routing Option | 73 | 0–4 | 0 = Auto, 1 = Mono→Stereo, 2 = Stereo→Stereo, 3 = Stereo→Mono, 4 = (Mono→Mono; truncated in PDF) |
| Preset Decrement | 80 | any | trigger |
| Preset Increment | 82 | any | trigger |
| Enable/Disable Tuner | 83 | any | any value toggles |
| Remote Switch Function | 94 | any | latching only |
| Remote Expression | 100 | 0–127 | external-control source |
| Bypass/Engage | 101 | 0–127 | < 64 bypass, ≥ 64 engage |
| Toggle Bypass/Engage | 102 | any | trigger |
| Recall Preset Bypassed | 103 | 0–127 | recall preset (value) bypassed |
| Recall Preset Engaged | 104 | 0–127 | recall preset (value) engaged |

**Parameters with NO factory default CC** (assign in Neuro, then add to the
YAML): Band Type (Ch1/Ch2 bands 1 & 10; peak/shelf), Channel Config
(parallel/series), Channel Phase, Split Channels, Limiter (enable/look-ahead/
stereo-link), Gate (enable 0–3 / threshold 0–75 / source), Switch Assign (0–4),
Switch Action (momentary/latching), Enable External Control.

## USB readback — Neuro HID protocol (state verification)

For the USB readback research (verifying what a BLE-MIDI write actually landed —
see `docs/research/usb.md`), the EQ2 exposes its real device state over its
**vendor HID interface**, *not* over USB-MIDI. This corrects the earlier
assumption of a "Neuro SysEx over USB-MIDI" channel: the EQ2's USB-MIDI interface
only carries standard PC/CC (there is **no published Source Audio readback
SysEx**), while the Neuro Desktop editor talks to the pedal over HID.

### Sources (reverse-engineered, cross-device)

The protocol is undocumented by Source Audio; it was reverse-engineered on the
sibling **C4 Synth**, which shares the same vendor (`0x29A4`) and Neuro HID
framework, and **confirmed live on this EQ2** (read-only):

- thierryd25/Source-Audio-C4-Synth-Preset-Browse — the `0x36` dump / `0x77`
  select commands and the preset-memory layout (Python over `hidapi`).
- MichaelMCE/TeensyC4Synth, MichaelMCE/Sa-C4 — a fuller C4 driver/controller
  (HID interrupt EP `0x01` OUT / `0x81` IN, 38-byte reports).
- Source Audio's registered MIDI SysEx manufacturer id is `00 01 6C` (MIDI.org) —
  noted for completeness; it is **not** used by the HID readback path.

### Transport

- Vendor **HID** interface (EQ2 USB interface 2): interrupt EP `0x01` OUT /
  `0x81` IN, **38-byte** reports, vendor usage page `0xFFA0`, no report IDs — a
  raw bidirectional vendor pipe (descriptor decoded in `docs/research/usb.md`).
- On Linux it is reachable as a `hidraw` node; the Neuro Desktop editor is
  Mac/Win only, so on Linux the HID interface is free for direct access.
- `cmd/usb-probe --device eq2` speaks this directly (no cgo/hidapi — raw
  `read`/`write`/`poll` on the hidraw fd). **Read-only by design.**

### Commands (host → device, in the 38-byte output report)

| Command | Bytes | Effect | Used here |
|---------|-------|--------|-----------|
| Memory dump | `0x36 <a2> <a1> <a0>` | reply dumps **32 bytes** from 24-bit big-endian address `a2a1a0` | **yes** (read-only) |
| Preset select | `0x77 <preset 0-127>` | program-change to a preset | **no** — it is a write/state change; documented but never issued |

The reply is a 38-byte **input** report: byte `[0]` echoes the command (`0x36`),
bytes `[1:33]` are the 32 dumped bytes, the remainder is padding.

### EQ2 memory map (confirmed live; addresses are EQ2-specific)

The C4's base/offsets did **not** match the EQ2 (C4's `0x80000 + n*0x1000 +
0xA0` read empty). The EQ2 layout, confirmed by dumping the flash, is:

- **Preset blocks:** 128 presets, base `0x080000`, **stride `0x1000`** (4 KB
  each), i.e. preset `n` at `0x080000 + n*0x1000`. Range `0x080000…0x100000`.
- **Block magic:** each used block starts with `EE 37 77 00 …`. An **unused
  preset is all `0xFF`** (whole block erased) — the probe treats a `0xFF` name
  byte as "empty" and skips it.
- **Preset name:** ASCII, NUL-padded, at offset **`0x097`** within the block (up
  to ~32 bytes, then `0xFF` fill). This is the reliable "which preset is in this
  slot" readback used to enumerate active presets.
- **Header flags:** offset `0x080` reads `01 00 00 7F …` on a used block —
  inferred preset-valid flag (`0x01`) and an output/level byte (`0x7F` = max);
  to be correlated against a known state change.
- **Band frequency table (inferred, strong):** offset `0x033` holds 10× 16-bit
  little-endian values that decode as ascending **Hz band centers** (e.g. for
  "Jazz Bass": 40, 80, 160, 320, 600, 900, 1300, 1807, 3000, 4300) — matching the
  EQ2's **10 parametric bands**. The table appears **twice** per block (Ch1/Ch2,
  consistent with Split Mode). Band **level/Q**, shelf type, routing, gate and
  the other CC-reachable parameters live in the same block but their exact byte
  offsets are **not yet decoded** (a follow-on: diff a block before/after a known
  single-parameter change).

### Mapping readback → the parameters above

- **Preset / `program_change`** → readable now: dump each slot's name field
  (`cmd/usb-probe --device eq2`) to confirm the active preset and enumerate the
  128 slots by name. This is the verification handle for preset recall.
- **Output level, per-band Level/Frequency/Q, shelf type, routing, gate, split**
  (the CC-controllable parameters listed above) → all reside in the preset
  block; **frequency is located** (offset `0x033`), the rest are present but
  **offset-mapping is future work**. Once mapped, a CC write could be verified by
  dumping the corresponding block bytes (the USB counterpart to today's MIDI-echo
  `verify_control`).
- Status: **confirmed** = transport, dump command, preset-name readback, band
  frequency table; **inferred** = header flag / output byte; **future** =
  per-parameter byte offsets and any round-trip write test (out of scope here).

## MIDI channel & clock

- **Default receive channel is 1.** The channel is configurable in the Neuro
  editors (1-16). In this project the channel comes from the **binding**, which
  must match the EQ2's configured channel.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- **MIDI clock**: One Series pedals only sync to clock if the active preset has
  "Sync to MIDI Clock"/Tap enabled — and **EQ-type effects have no time-based
  parameters**, so MIDI clock is effectively irrelevant for the EQ2. Not
  modeled.
  [faq](https://sourceaudio.net/pages/faq)

## Neuro Hub "Scenes" (out of scope)

- A **Neuro Hub** can store multi-pedal **Scenes** (up to 128) recalled over
  MIDI. That is a hub-level feature layered over several Source Audio pedals; it
  is **not** an EQ2 MIDI capability per se and is out of scope for the single
  device definition. This server's own scene model supersedes it.

## Summary for the YAML

- `transport: blemidi`; channel from the binding (device default = 1).
- `preset` as `program_change` `0-127` (all 128 presets) — the reliable recall
  path; `settle_ms` set conservatively (preset load) and tuned on hardware.
- CC#103/CC#104 are now modeled: **Recall Preset Bypassed** (103) and **Recall
  Preset Engaged** (104) — value = preset number.
- The full **default CC map** (master/input levels, Ch1+Ch2 per-band
  level/frequency/Q, gains, high-pass, routing, bypass, tuner, preset inc/dec,
  remote expression) is **transcribed into the YAML** from the MIDI Mapping
  Table above. The map is **remappable** in Neuro, so if a rig has a custom map,
  restore the defaults or re-bind via MIDI-learn.
- The handful of parameters with no factory CC (band type, channel config/phase,
  split, limiter, gate, switch assign/action, external-control enable) are left
  out of the YAML controls and listed as a comment; assign them a CC in Neuro to
  add them.
