# Morningstar ML10X — Incoming MIDI control research

Research note for the device definition at
`internal/device/definitions/ml10x.yaml`. Scope: what the ML10X **responds to**
over MIDI so this MCP server can control/configure it. The ML10X is a
MIDI-controlled reorderable loop switcher (10 loops over 5 TRS send/return
ports). It is also a live foot controller, but its *outgoing* foot-controller
messages are out of scope here.

Primary source (all values below come from this page unless noted), the ML10X
User Manual "MIDI Implementation" section, **updated 22/12/2025**:
<https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>

## Banks & presets

- 4 banks, 128 presets each; preset numbers range **0-127**.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#banks-and-presets>

## MIDI ports & transport

- One 5-pin MIDI IN, one 5-pin MIDI THRU, and one USB-C port.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#midi-ports>
- This project reaches the device over **BLE-MIDI** (via the rig's WIDI hub on
  the DIN chain), so `transport: blemidi` in the definition. The manual itself
  documents the wired DIN / USB ports; the MIDI message semantics are identical
  regardless of physical transport.

## MIDI channel behaviour

- MIDI channel is set on-device: **Menu > Global Settings > Edit MIDI Channel**.
  The device acts on incoming messages matching its configured channel.
- The device can be set to **ignore all incoming MIDI** from the same menu, in
  which case none of the messages below have any effect.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#setting-midi-channel>
- The channel is **not** stored in the device definition (per the project's
  binding model); it is supplied by the binding.

## Program Change (preset recall)

| Function | Message | Value | Notes |
|----------|---------|-------|-------|
| Recall preset | Program Change | PC 0-127 → Preset 0-127 | Recalls within the **current** bank. To recall a preset in another bank, send the `change_bank` CC **first**, then the PC. |

Source: <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>
(MIDI Implementation › Program Change messages).

> Note: ordering matters for cross-bank recall (bank-select CC before PC). The
> engine's scene recall already sends program-change-then-CC; for an explicit
> cross-bank jump, set `change_bank` then `preset`.

## Control Change messages

All rows below are from the manual's "Control Change Messages" table. Loop
engage/bypass/toggle messages **only affect Simple-mode presets** (in Advanced
mode, loops cannot be bypassed via CC). Source for every row:
<https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>

| Function | CC# | Value (per manual) | Modelled `value` in YAML |
|----------|-----|--------------------|--------------------------|
| Change Bank | 0 | 0-3 | enum bank_1=0, bank_2=1, bank_3=2, bank_4=3 |
| Engage/Bypass all loops (Simple only) | 4 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Scroll Up | 5 | any | enum trigger=127 |
| Scroll Down | 6 | any | enum trigger=127 |
| Mute | 7 | any | enum trigger=127 |
| Unmute | 8 | any | enum trigger=127 |
| Toggle Mute/Unmute | 9 | any | enum trigger=127 |
| Engage/Bypass Loop A Tip (Simple only) | 10 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop A Ring (Simple only) | 11 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop B Tip (Simple only) | 12 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop B Ring (Simple only) | 13 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop C Tip (Simple only) | 14 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop C Ring (Simple only) | 15 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop D Tip (Simple only) | 16 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop D Ring (Simple only) | 17 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop E Tip (Simple only) | 18 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop E Ring (Simple only) | 19 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Toggle Loop A Tip (Simple only) | 20 | 0-127 | enum trigger=127 |
| Toggle Loop A Ring (Simple only) | 21 | 0-127 | enum trigger=127 |
| Toggle Loop B Tip (Simple only) | 22 | 0-127 | enum trigger=127 |
| Toggle Loop B Ring (Simple only) | 23 | 0-127 | enum trigger=127 |
| Toggle Loop C Tip (Simple only) | 24 | 0-127 | enum trigger=127 |
| Toggle Loop C Ring (Simple only) | 25 | 0-127 | enum trigger=127 |
| Toggle Loop D Tip (Simple only) | 26 | 0-127 | enum trigger=127 |
| Toggle Loop D Ring (Simple only) | 27 | 0-127 | enum trigger=127 |
| Toggle Loop E Tip (Simple only) | 28 | 0-127 | enum trigger=127 |
| Toggle Loop E Ring (Simple only) | 29 | 0-127 | enum trigger=127 |

### Modelling decisions

- **"any" value triggers** (scroll, mute/unmute/toggle-mute, loop toggles): the
  manual accepts any value 0-127, so the YAML uses an `enum` with a single
  `trigger: 127` label. Any non-momentary value would also work; 127 is the
  conventional momentary trigger and keeps these controls discoverable.
- **Engage/bypass loops & "all loops"**: modelled as `enum {bypass: 0,
  engage: 127}`. The manual splits the value range at 64 (0-63 bypass / 64-127
  engage); the two enum labels pick representative values on each side.
- **Change Bank**: modelled as a 4-entry enum (bank_1..bank_4 → 0..3). Labels
  are 1-based for human readability while wire values stay 0-based per the
  manual. Banks are numbered 0-3 on the wire.

## Out of scope / not modelled

- **ML10X Message Type (SysEx)** — a dedicated Morningstar-controller message
  type (Set/Engage/Bypass/Toggle Selected Loops, Scroll, Select Preset) that
  engages loops without CC. It is SysEx-based and uses a per-device Device ID
  (or Omni). Not modelled here because the project controls the ML10X via CC/PC;
  this could be added later as `type: sysex` controls if needed.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#ml10x-message-type-for-morningstar-midi-controllers>
- **No expression / continuous-controller input** is documented for the ML10X
  in the manual's MIDI Implementation table (it is a loop switcher, not an
  expression target), so no expression control is included.

## Caveats

- Loop engage/bypass/toggle (CC 4, 10-29) **only work on Simple-mode presets**.
  In Advanced mode, loops cannot be bypassed via CC (subject to a beta firmware
  noted in the manual).
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#advanced-presets>
- Cross-bank preset recall requires `change_bank` **before** the PC.
- If the device's MIDI channel is set to ignore incoming MIDI, no message takes
  effect.
- Table reflects firmware/manual revision dated **22/12/2025**; re-verify
  against the live manual if firmware changes.
