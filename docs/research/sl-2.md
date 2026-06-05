# Boss SL-2 — MIDI implementation research

Research note backing `internal/device/device-types/sl-2.yaml`. The SL-2 Slicer
is a Boss **compact** pedal (not a 200-series unit), but unlike most Boss
compacts it has a **TRS MIDI IN** for clock sync and a small set of Control
Change messages. It has **no Program Change** and **no preset recall over
MIDI** — only three CCs plus MIDI-clock sync.

## Sources

- **Owner's Manual — "Controlling This Unit from an External MIDI Device"**
  (Roland, hosted manual):
  <https://static.roland.com/manuals/sl-2/eng/33861479.html>
- **Owner's Manual (full)** — manuals.plus mirror:
  <https://manuals.plus/boss/sl-2-slicer-manual>
- **User Guide (PDF)** — Full Compass mirror (`84299-SL2SlicerUserGuide.pdf`):
  <https://www.fullcompass.com/common/files/84299-SL2SlicerUserGuide.pdf>
- **Product specifications / support** — boss.info:
  <https://www.boss.info/global/products/sl-2/support/>
- **Community pattern editor** (USB SysEx reverse-engineering, *unofficial*):
  <https://github.com/pylonsarenice/SL2-Pattern-Editor>

## Control Change map (the entire live MIDI surface)

The owner's manual lists exactly **three** CCs. There are no other documented
CCs and no NRPN/SysEx for live control.
[manual](https://static.roland.com/manuals/sl-2/eng/33861479.html)

| Control | CC# | Function | YAML control |
|---------|-----|----------|--------------|
| EXP | 16 | Controls **whichever single parameter is assigned to EXP** (SYSTEM `EXP_FUNC`), *not* a fixed "expression level". | `expression` |
| EFFECTS ON/OFF | 80 | Turns the SL-2 effect on/off. | `on_off` |
| TAP TEMPO | 81 | Sets the tempo from the interval between received CC#81 messages. | `tap_tempo` |

- **On/Off (CC#80)** — **confirmed live**: `CC#80 = 127` turns the effect on
  (clearly audible), `CC#80 = 0` turns it off. The 0/127 convention holds; the
  manual just doesn't print it.
- **EXP (CC#16) is the assignable EXP controller, not a fixed "level".** The SL-2
  routes CC#16 to the one parameter selected by the SYSTEM `EXP_FUNC` setting
  (`0x10000007`), whose choices are
  `0=DUTY, 1=ATTACK, 2=TEMPO, 3=BALANCE, 4=OUTPUT LEVEL` (factory default `2`).
  The manual's "controls the output level of the effect sound" describes only the
  `OUTPUT LEVEL` assignment (value `4`).
  - **Confirmed live on the rig (EXP_FUNC = 2 = TEMPO):** CC#16 sets the tempo
    linearly over the pedal's 40–300 BPM range:
    `tempo_BPM = 40 + (cc/127) × 260`. Measured: `cc=0 → 40.0`, `cc=64 → 171.0`,
    `cc=127 → 300.0`, `cc=40 → 121.9`. So on this unit CC#16 is **a precise,
    single-message tempo control** — much more reliable than tap tempo.
  - To make CC#16 behave as the manual's "effect output level", set
    `EXP_FUNC = 4` (front panel, or `DT1 0x10000007 = 04` — **works over BLE or
    USB**, confirmed). Because `EXP_FUNC` is DT1-writable live, CC#16 can be
    re-pointed on the fly to act as a DUTY/ATTACK/TEMPO/BALANCE/LEVEL macro — but
    writing the target parameter's address directly via DT1 is usually simpler.
- **TAP TEMPO (CC#81)** is a *rate-from-interval* trigger, not a value: the
  pedal measures the time between successive CC#81 messages. It is therefore a
  poor fit for the desired-state/scene model (there is no stable "value" to
  store). Modeled as a `range 0-127` trigger and flagged as not scene-relevant.
  **Unreliable over BLE:** the BLE-MIDI bundling + PipeWire/WIDI bridge does not
  preserve sub-message timing, so a tapped interval collapses (an attempted 100
  BPM tap drove the tempo to the 300 BPM rail). Prefer CC#16 (when EXP=TEMPO) or
  MIDI clock for tempo; tap over BLE is not trustworthy.

## Tempo / MIDI clock sync

- The SL-2 syncs slice playback to **MIDI clock**: it responds to timing clock
  (`F8h`), start (`FAh`), continue (`FBh`) and stop (`FCh`). On start the slice
  pattern restarts from the beginning.
  [manual](https://static.roland.com/manuals/sl-2/eng/33861479.html)
- While synced to external MIDI clock, **tap tempo is ignored** — neither the
  pedal switch, an external footswitch, nor CC#81 can set the tempo. Tempo set
  via MIDI clock is **not stored** in the pedal.
- MIDI clock is system-real-time and **has no channel**, so it is sent to the
  whole DIN chain regardless of the pedal's receive channel.

## No Program Change — but Type/pattern ARE settable via DT1 SysEx

- The SL-2 has **no Program Change reception** and no **CC** to select the slicer
  Type or pattern Variation. The manual therefore says Type (`SINGLE`, `DUAL`,
  `TREMOLO`, `HARMONIC`, `SFX`) and Variation are "front-panel knobs only".
  [manual](https://static.roland.com/manuals/sl-2/eng/33861479.html)
- **However, they ARE writable via Roland DT1 SysEx** (temp-patch addresses, over
  BLE or USB) and the change is live — confirmed on the rig (see "Temp-patch edits
  apply to the LIVE sound over BLE"). `PATCH_SELECT` (`0x7F000100 = 00 <n>`) also
  recalls a full stored pattern into the temp buffer, i.e. preset recall over MIDI
  via SysEx. So the manual's "not over MIDI" is true only for PC/CC, not SysEx.
- This means a scene can capture **on/off and effect level**, but **not** the
  selected pattern/type — that must be dialed in on the hardware.

## USB (out of scope for this server)

- The USB port is for connecting a computer; **slice patterns can be swapped via
  Boss Tone Studio** (`.tsl` Live Sets). This is a SysEx-over-USB data exchange,
  not real-time MIDI control, and Boss ships no official pattern editor. A
  community editor exists (see Sources). This server targets the **TRS MIDI IN**
  (over the WIDI/DIN chain), so USB pattern editing is **out of scope**.
  [editor](https://github.com/pylonsarenice/SL2-Pattern-Editor)

## MIDI receive channel

- Set by an odd power-on knob combination: turn the **type knob to "DUAL (4)"**,
  turn all other knobs fully clockwise, then **hold the pedal switch while
  powering on** (insert the plug into INPUT A), and use the **[VARIATION] knob**
  to pick the channel.
  [manual](https://static.roland.com/manuals/sl-2/eng/33861479.html)
- Selectable channels are **1-10**, plus position **11 = All channels (omni)**.
  There are only 11 settings (not 1-16). The manual does not state the factory
  default; `# TODO: confirm` (Boss convention is channel 1).
- In this project the channel comes from the **binding**, so the binding's
  channel must equal the SL-2's selected receive channel (or the pedal must be
  set to "All").

## Connection / transport

- The SL-2's **MIDI IN is TRS** (Boss `BMIDI-*-35` TRS-to-DIN cables, or
  `BCC-*` TRS-to-TRS). It hangs off the DIN chain behind a BLE-MIDI hub via such
  a cable, so it is a `blemidi` device addressed by `(endpoint, channel)` like
  the other pedals.

## USB editor protocol — Roland address-based SysEx (CONFIRMED)

The SL-2's USB-MIDI port speaks the full Roland address-based SysEx editor
protocol (RQ1/DT1 over an address map) — the same channel BOSS Tone Studio uses.
This is **far richer than the 3-CC live surface above**: every patch parameter
(pattern, step length/level/band/effect/pitch, comp, mod FX, EQ, …) and all 88
stored patterns are readable *and writable* over USB. It is a USB-only channel
(the TRS MIDI IN cannot carry it), so it does not change the BLE control model,
but it does let an agent **create/edit/store SL-2 patterns**.

> **Source: decompiled "BOSS TONE STUDIO for SL-2" (Windows installer
> `bts_sl2_w100`).** The app is a CEF/Chromium shell wrapping a plain-JavaScript
> editor; the web bundle is embedded as the PE resource `.rsrc/ZHTML/BUNDLE`
> (a ZIP). The protocol lives in `config/product_setting.js`,
> `config/address_map.js`, `common/midi_controller.js`,
> `utilities/converter.js`/`constant.js`, and `businesslogic/{address_const,
> midi_connect_controller,patch_controller,util}.js`. Extracted reference (not
> committed — Roland-proprietary) kept under `docs/private/sl2-bts/`. Every fact
> below was **verified live, read-only**, against the rig's SL-2 (`hw:6,0,0`)
> with `cmd/usb-probe --device sl-2` and `amidi`.

### Device identity & framing

| Field | Value |
|-------|-------|
| Manufacturer | Roland `0x41` |
| **Model ID** | **`00 00 00 00 1D`** (5 bytes) |
| Device ID | `0x10` (default; the app re-reads it from the Identity Reply) |
| Address length | 4 bytes |
| Size length | 4 bytes |
| Identity Reply | `F0 7E 10 06 02 41 1D 04 00 00 00 00 00 00 F7` (family code `0x041D`) |

The earlier "blocked" finding was wrong only because the probe used a 4-byte
model id (`00 00 00 1D`); the real id is **5 bytes** (`00 00 00 00 1D`). With it,
plain RQ1 works immediately — no special session unlock is needed to read.

```
RQ1 (read):  F0 41 10 00 00 00 00 1D 11 <a3 a2 a1 a0> <s3 s2 s1 s0> <sum> F7
DT1 (write): F0 41 10 00 00 00 00 1D 12 <a3 a2 a1 a0> <data…>       <sum> F7
sum = (0x80 - (Σ(address+size|data bytes) & 0x7F)) & 0x7F   (standard Roland)
```

**Addresses are 7-bit-safe and used literally.** The app stores addresses
"nibbled" internally and runs `_7bitize()` before sending, but the two transforms
are inverses, so **the wire address equals the hex value written in
`address_map.js`** (e.g. temp patch = `20 00 00 00`, SYSTEM = `10 00 00 00`,
command block = `7F 00 00 00`). A child's absolute address = sum of the addresses
along its tree path; the map is spaced so no byte ever exceeds `0x7F`.

### Address map (top level)

| Base | Block | Contents |
|------|-------|----------|
| `0x00000000` | SETUP | current patch number |
| `0x10000000` | SYSTEM | tempo, output level/mode, CTL/EXP func, **MIDI ch** (`+0x08`), duty/attack/balance/level min-max, tempo min/max |
| `0x20000000` | **PATCH (temporary / edit buffer)** | the live, editable patch |
| `0x20100000`, `0x20200000`, … `0x2B000000` | **PATCH(1)…PATCH(88)** | the 88 stored patterns (8 types × 11 variations) |

Each PATCH block's children (offsets within the patch):

| Offset | Sub-block | Params |
|--------|-----------|--------|
| `+0x0000` | COM | 16-byte ASCII name |
| `+0x1000` / `+0x2000` | SLICER(1)/(2) | PATTERN, SWITCH, FX_TYPE, STEP_NUMBER, 24×(STEP_LENGTH, STEP_LEVEL, STEP_BAND, STEP_EFFECT, STEP_PITCH) |
| `+0x3000` | COMP | switch, sustain, attack, level, tone, ratio, direct mix |
| `+0x4000` | DIVIDER | mode, cutoff |
| `+0x5000`/`+0x6000` | PHASER(1)/(2) | switch, type, rate, depth, resonance, manual, step rate, init phase, effect level, direct mix |
| `+0x7000`/`+0x10000` | FLANGER(1)/(2) | switch, rate, depth, resonance, manual, step rate, init phase, separate, lo-cut, effect level, direct mix |
| `+0x11000`/`+0x12000` | TREMOLO(1)/(2) | switch, wave, rate, init phase, depth, level |
| `+0x13000`/`+0x14000` | OVERTONE(1)/(2) | switch, lower/upper/unison/direct level, detune, low, high, output mode |
| `+0x15000` | MIXER | mode, A sw/level, B sw/level |
| `+0x16000` | NS | switch, threshold, release |
| `+0x17000` | PEQ | switch, low/high gain, level, low-mid/high-mid freq/Q/gain, low-cut, high-cut |
| `+0x20000` | BEAT | beat |

Per-parameter `addr/size/min/max/init/ofs` (signed params use `ofs`, e.g. PITCH
`ofs:12` → wire 0..24 = −12..+12) are in `docs/private/sl2-bts/js/address_map.js`.

**Value encodings** (`size` field, see `constant.js`): `INTEGER1x7` = 1 byte
(0–127); `INTEGER1x1..1x6` = 1 byte, fewer bits; `INTEGER2x4` = 2 bytes, 4 bits
each (hi nibble, lo nibble); `INTEGER4x4` = 4 bytes × 4-bit nibbles (16-bit, used
for tempo); a bare integer `size:N` = N raw bytes (e.g. 16-byte name).

### Connect handshake (from `midi_connect_controller.js`)

1. Identity Request `F0 7E 7F 06 01 F7`; verify the reply's model id tail
   (`…06 02 41 1D 01`) and adopt the returned device id.
2. RQ1 `EDITOR_COMMUNICATION_LEVEL` (`0x7F000000`, size 1) → device's comm level.
3. **DT1 `EDITOR_COMMUNICATION_MODE` (`0x7F000001`) = `01`** → enter editor mode.
   (optional RQ1 `EDITOR_COMMUNICATION_REVISION` `0x7F000003`.)
4. Then read/write the temp patch and stored patches freely.

Reads of SYSTEM / temp-patch / command registers answered even **before** step 3
in testing; editor mode is the app's formal session and the safe precondition for
writing.

Command registers (`businesslogic/address_const.js`, all in the `0x7F000000`
block):

| Command | Address | Data |
|---------|---------|------|
| EDITOR_COMMUNICATION_LEVEL | `0x7F000000` | (RQ1, size 1) |
| EDITOR_COMMUNICATION_MODE | `0x7F000001` | `01` on / `00` off |
| EDITOR_COMMUNICATION_REVISION | `0x7F000003` | (RQ1, size 1) |
| PATCH_SELECT | `0x7F000100` | `00 <n>` recall stored patch n into temp |
| PATCH_WRITE | `0x7F000104` | `00 <n>` store temp → stored patch n |
| PATCH_TEMP_CLEAR | `0x7F000106` | `00 00` reset temp to init |

### Creating / editing a pattern over USB (write flow)

1. Handshake (above), editor mode on.
2. `DT1` each parameter into the **temporary patch** (`0x20000000` + sub-block +
   param) — e.g. name @ `0x20000000`, SLICER(1) PATTERN @ `0x20001000`, etc.
3. `DT1 PATCH_WRITE (0x7F000104) = 00 <slot>` to store the temp buffer into one of
   the 88 user slots; the device echoes a DT1 at that address on success.
   (`PATCH_SELECT` recalls a slot into temp; `PATCH_TEMP_CLEAR` resets it.)

This is exactly what BOSS Tone Studio does, and is what backs MCP tools that let
an agent build SL-2 patterns.

### DT1 writes work over BLE/TRS — not just USB (CONFIRMED)

**Key finding (2026-06-02):** the SL-2 accepts Roland **DT1 parameter writes on
its TRS MIDI IN**, reachable over the BLE → WIDI-hub path — not only over USB.

Test (open-loop write over BLE, verified out-of-band by reading back over USB):

| Step | Transport | Action | USB readback of `0x10000007` |
|------|-----------|--------|------------------------------|
| 1 | USB | read baseline | `02` |
| 2 | **BLE** | `DT1 0x10000007 = 04` | — |
| 3 | USB | re-read | **`04`** (changed) |
| 4 | **BLE** | `DT1 0x10000007 = 02` (restore) | — |
| 5 | USB | verify | `02` |

`EXP_FUNC` cannot be changed by any CC, so the change proves the DT1 SysEx was
parsed off the TRS MIDI IN. DT1 frame sent over BLE:
`F0 41 10 00 00 00 00 1D 12 10 00 00 07 04 65 F7` (Roland checksum
`(128 - (Σ(addr+data) & 0x7F)) & 0x7F`).

Consequences:

- **Full parameter editing is possible over BLE MIDI** (no laptop / no USB host):
  any DT1 — slicer Type/pattern, every step length+level, EXP_FUNC, tempo, patch
  name, even `PATCH_WRITE` to a stored slot — can be sent through the WIDI hub.
- **It is open-loop over BLE.** The SL-2 has TRS **MIDI IN only** (no MIDI OUT),
  so RQ1 *readback* still requires the USB port. Over BLE you can only **write
  absolute values**, not read current state.
- Discrete DT1 writes don't depend on inter-message timing, so the BLE bundling
  problem that breaks tap tempo does **not** affect parameter writes.
- This is the first time DT1 **writes** were exercised at all (earlier passes were
  read-only); the `EXP_FUNC` write above was reversible and restored.

### Temp-patch edits apply to the LIVE sound over BLE — no handshake (CONFIRMED)

**Key finding (2026-06-02):** writing temp-patch parameters via DT1 over BLE
changes the **running effect immediately** — no `EDITOR_COMMUNICATION_MODE`
handshake required. Confirmed both by USB readback *and* audibly on the rig (the
slicer pattern was heard changing through a `0 → 8 → 16 → 24 → 32 → 40 → 50` sweep,
5 s apart, sent over BLE while playing).

Temp-patch addresses exercised (`PATCH` base `0x20000000`, blocks at stride
`0x1000`; SLICER param offsets from `address_map.js`):

| Param | Address | Size | Range | Notes |
|-------|---------|------|-------|-------|
| SLICER(1) PATTERN | `0x20001000` | 1 | 0–50 | preset slice pattern |
| SLICER(1) FX_TYPE | `0x20001002` | 1 | 0–6 | slicer effect type |
| SLICER(1) STEP_NUMBER | `0x20001003` | 1 | 0–3 | step count |
| SLICER(2) PATTERN | `0x20002000` | 1 | 0–50 | (DUAL patch) |
| SYSTEM TEMPO | `0x10000000` | 4 | 40.0–300.0 | INTEGER4x4 (BPM×10) |

Demo result (all sent over BLE, verified over USB, then restored):
`TEMPO 80/150 BPM`, `PATTERN 10/30`, `FX_TYPE 5`, `STEP_NUMBER 3` — every write
landed in memory and the audible ones (tempo, pattern, type) were heard live.

**This breaks the manual's claim** that slicer Type and pattern Variation are
"front-panel knobs only, not selectable over MIDI": they are not reachable by
PC/CC, but they *are* directly writable via DT1 (over BLE or USB), and the change
is live.

### Live confirmation (read-only)

`cmd/usb-probe --device sl-2` against the rig (read-only RQ1), decoded replies:

| Read | Address / size | Reply data | Meaning |
|------|----------------|------------|---------|
| EDITOR_COMM_LEVEL | `7F000000` / 1 | `00` | level 0 |
| EDITOR_COMM_REV | `7F000003` / 1 | `00` | rev 0 |
| Temp patch name | `20000000` / 16 | `54 52 45 4D 4F 4C 4F 20 35 2D 30 32 …` | "TREMOLO 5-02" |
| SYSTEM MIDI ch | `10000008` / 1 | `05` | **channel 6** (0-indexed; 0=ch1 … 10=All) |
| SYSTEM tempo | `10000000` / 4 | `00 04 0C 04` | 0x4C4 = 1220 = 122.0 BPM (range 40.0–300.0) |
| SYSTEM EXP_FUNC | `10000007` / 1 | `02` | **TEMPO** (`0=DUTY 1=ATTACK 2=TEMPO 3=BALANCE 4=OUTPUT LEVEL`) |
| SYSTEM CTL_FUNC | `10000006` / 1 | `00` | CTL footswitch function (`MOMENTARY/TAP, TAP TEMPO, MOMENTARY`) |

The MIDI-channel readback (`05` = channel 6) independently confirms the rig's
SL-2 receive channel.

### Live CC behaviour confirmed via the BLE → WIDI hub → SL-2 path

Sending channel-voice CC on channel 6 over the real binding transport (BLE), with
the tempo read back over USB to verify:

- `CC#80 = 127 / 0` — effect on / off, audibly confirmed.
- `CC#16` (EXP = TEMPO on this unit) — sets tempo exactly per
  `40 + cc/127 × 260`: `cc=0 → 40.0`, `cc=64 → 171.0`, `cc=127 → 300.0`,
  `cc=40 → 121.9` BPM. This is the reliable tempo path.
- `CC#81` taps — received (they moved the tempo) but timing collapses over BLE,
  so the resulting BPM is not controllable. Avoid for tempo.

Note: the SL-2's **USB port is editor/SysEx-only** — it ignores channel-voice CC
(verified: CC#81 taps sent to the USB port left the tempo unchanged). RQ1/DT1
readback works over USB; live CC control must go over the BLE/TRS path.

## Summary for the YAML

- Three CC controls only: `expression` (16), `on_off` (80), `tap_tempo` (81).
  - **`expression` (CC#16) is mislabelled**: it is the assignable **EXP
    controller**, routed by SYSTEM `EXP_FUNC`. On this rig `EXP_FUNC = 2 = TEMPO`,
    so CC#16 is effectively a precise tempo control (40–300 BPM, linear). Consider
    renaming the control to `exp` and documenting the assignment + BPM mapping.
  - `on_off` (CC#80) confirmed (0 = off, 127 = on).
  - `tap_tempo` (CC#81) is unreliable over BLE (timing not preserved) — prefer
    CC#16/EXP=TEMPO or MIDI clock.
- No `program_change` control; pattern/type is not MIDI-addressable over CC/PC.
  It **is** writable via Roland **DT1 SysEx** (over BLE/TRS *or* USB), so the
  YAML now models it as `type: sysex` controls that write the temporary/edit
  patch (the running sound): `slicer1_pattern` (0x20001000), `slicer1_fx_type`
  (0x20001002), `slicer1_step_number` (0x20001003), `slicer2_pattern`
  (0x20002000), `exp_func` (0x10000007), and `patch_select` (PATCH_SELECT
  command 0x7F000100, recall a stored pattern into temp). The engine renders the
  Roland checksum via the `[ ... ] %k` template tokens (checksum over the
  address+data bytes). Storing to a slot (PATCH_WRITE) is intentionally **not**
  modeled (authoring/destructive).
- `settle_ms: 0` (no preset load to wait on).
- MIDI clock sync exists but is a transport-level/system-real-time concern, not
  a per-control value — not modeled as a control.
