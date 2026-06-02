# Boss SL-2 — MIDI implementation research

Research note backing `internal/device/definitions/sl-2.yaml`. The SL-2 Slicer
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
| EXP | 16 | Controls the output level of the effect sound (same as a connected expression pedal). | `expression` |
| EFFECTS ON/OFF | 80 | Turns the SL-2 effect on/off. | `on_off` |
| TAP TEMPO | 81 | Sets the tempo from the interval between received CC#81 messages. | `tap_tempo` |

- **On/Off value semantics are not printed** in the manual. Boss convention is
  0 = off / 127 = on; modeled as `enum {off: 0, on: 127}` with a
  `# TODO: confirm` (the SL-2 manual does not state a 0/127 vs. 0-63/64-127
  threshold).
- **TAP TEMPO (CC#81)** is a *rate-from-interval* trigger, not a value: the
  pedal measures the time between successive CC#81 messages. It is therefore a
  poor fit for the desired-state/scene model (there is no stable "value" to
  store). Modeled as a `range 0-127` trigger and flagged as not scene-relevant.

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

## No Program Change / no preset recall over MIDI

- The SL-2 has **no Program Change reception** and exposes **no way to select
  the slicer Type or pattern Variation over MIDI**. Type (`SINGLE`, `DUAL`,
  `TREMOLO`, `HARMONIC`, `SFX`) and Variation are front-panel knobs only.
  [manual](https://static.roland.com/manuals/sl-2/eng/33861479.html)
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

## Summary for the YAML

- Three CC controls only: `expression` (16), `on_off` (80), `tap_tempo` (81).
- No `program_change` control; pattern/type is not MIDI-addressable.
- `settle_ms: 0` (no preset load to wait on).
- MIDI clock sync exists but is a transport-level/system-real-time concern, not
  a per-control value — not modeled as a control.
