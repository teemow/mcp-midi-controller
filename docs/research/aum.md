# AUM (iPad host) — MIDI control research

Research note for the device definition at
`internal/device/definitions/aum.yaml`. Scope: what AUM **can be controlled by**
over MIDI so this MCP server can drive its mixer/transport, and how AUM's MIDI
mapping system works. AUM is Kymatica's iOS audio mixer / AUv3 + IAA host; this
server reaches it over **BLE-MIDI** (the iPad's WIDI dongle), so
`transport: blemidi` in the definition.

Primary source (all behaviour below comes from this page unless noted), the AUM
help / manual: <https://kymatica.com/aum/help> (see the **"MIDI Control"** and
**"Clock and transport"** sections).

## The crucial fact: AUM has no fixed CC map

AUM ships **no factory CC numbers**. Every controllable parameter responds only
to whatever MIDI message *you* assign to it through AUM's **MIDI Control** system
(via the **LEARN** button or manual entry). Therefore the CC numbers in
`aum.yaml` are a **convention this server invents and owns**, not documented AUM
defaults. The workflow is: pick a convention here → mirror those exact CC numbers
into AUM's MIDI Control matrix on the bound MIDI channel.

This mirrors the project's general "AUv3 / convention model" (see
`docs/design.md` → "AUv3 plugins & AUM").

## How AUM's MIDI Control works

Source: <https://kymatica.com/aum/help> ("MIDI Control").

- **Where it listens.** AUM has a dedicated **"MIDI Control"** CoreMIDI
  destination. You connect a MIDI source to it (via the MIDI Sources shortcut on
  the MIDI Control page or the main MIDI routing matrix). For this rig that source
  is the iPad's BLE-MIDI input fed by this server.
- **Collections.** Mappings are grouped into **collections**: each channel has a
  collection per node, and audio channels additionally have a **"Channel
  Controls"** collection (level fader, mute, solo, rec, "Scroll to this channel").
  Plugin parameters appear in the plugin's own collection (with sub-collections
  for parameter subgroups). Some collections are dynamic (Session Load, Preset
  Load, Tempo Presets).
- **Parameter kinds.** Four kinds, each with slightly different settings:
  **Values** (continuous), **Toggles** (on/off), **Triggers** (fire an action),
  and **Indexed** (discrete steps).
- **Message types AUM can match:**
  - `CC` — Continuous Controller, value **0-127 (7-bit)**.
  - `NOTE` — note-on velocity 0-127 as value; note-off = 0.
  - `PC` — Program Change 0-127.
  - `PBEND` — Pitch Bend, value **0-16383 (14-bit)**.
  - `CHPRS` — Channel Pressure 0-127.
- **MIDI channel.** Each mapping responds on a chosen channel **1-16 or OMNI**
  (any), or **OFF** to disable. A collection can be **batch-set** to one channel
  ("Set MIDI Channels", 1-16 or 0=OMNI). In this project the channel is **not**
  stored in the definition — it is supplied by the binding, so set the AUM
  collection's channel to match the binding.
- **LEARN.** The LEARN button auto-configures a parameter to the next incoming
  MIDI message — handy, but for AUM we drive the convention the other way: we
  assign the numbers from `aum.yaml`.
- **Range.** Value and Indexed parameters have an adjustable input range
  ("0 → 100%" min/max), so a 0-127 CC can be scaled to part of a fader's travel.
- **Cycle & Invert.** For Toggle/Indexed: with **Cycle ON**, a non-zero value
  cycles the value (good for momentary buttons). With **Cycle OFF**, a Toggle is
  ON only while CC value **> 64** (latching). **Invert** swaps on/off (equivalent
  to swapping the range min/max). This is why the mute convention uses
  `0 = unmute`, `127 = mute` — with Cycle off, 127 (>64) latches mute on, 0
  releases it.

### 14-bit / NRPN support

- **No NRPN.** AUM's MIDI Control message types do not include NRPN/RPN; CC is
  **7-bit only** (no high-resolution CC MSB/LSB pairing).
- The **only** 14-bit (0-16383) path is **Pitch Bend (PBEND)**. This convention
  uses plain 7-bit CC throughout for simplicity, so faders/pans have 128 steps.
  If finer fader resolution is ever needed, map a parameter to PBEND instead.

## What AUM can be MIDI-controlled (exposed parameters)

Source: <https://kymatica.com/aum/help> ("MIDI Control", "Transport Control",
"System actions").

### Per audio channel — "Channel Controls" collection
- **Volume / level fader** (Value)
- **Mute** (Toggle)
- **Solo** (Toggle)
- **Record-enable** (Toggle)
- **Scroll to this channel** (Trigger) — also present on MIDI strips.

### Per channel — node parameters
- **Any parameter of any node** in the strip, including all AUv3 plugin
  parameters, plus **node bypass**, plugin **preset load**, and **show/hide
  plugin** actions.
- **Pan is NOT a native channel control.** AUM channel strips have no built-in
  pan knob. Panning is done by adding a **Stereo Balance** (or **Stereo Panning**)
  processing node to the strip; that node's knob is then MIDI-controllable as a
  node parameter. So `chN_pan` in the convention must be mapped to a Stereo
  Balance/Panning node placed on that channel.

### Global transport — "Transport Control"
- **MMC (SysEx)** via "Receive MMC": Play, Stop, Pause, Rewind, Record,
  Goto/Locate.
- **Simple MIDI control** items (Trigger/Value, mappable to NOTE or CC):
  Rewind, Start play, Stop/rewind, **Toggle play**, Toggle record, Previous bar,
  Next bar, Tap tempo, Tempo, Tempo Presets, Metronome on/off.
- Note: **Triggers fire on non-zero only** — a CC value of 0 does not trigger.
  So a single CC with `{stop:0, start:127}` cleanly drives **Toggle play**
  (start), but to get a deterministic stop you map **Stop/rewind** to its own
  message, or use MMC.

### System / session actions
- Switch to AUM (foreground), Hide/Unhide plugins, **Unsolo channels**,
  **Session Load** (load a saved session via MIDI; saved globally), Preset Load.

### Other MIDI-controllable bits
- File player **play-enable** toggle and **playback rate**; MIDI Bus node
  **Enabled** toggle. (Not in the convention map, but available.)

## Proposed convention CC map

All CCs ride the **MIDI channel supplied by the binding**. Numbers sit in MIDI's
general-purpose/undefined CC range to avoid clashing with common controller
defaults (mod wheel, sustain, bank select, etc.).

Per-channel formula (channel `N`, 1-based):
- `mute  = 18 + 3*N`
- `level = 19 + 3*N`
- `pan   = 20 + 3*N`

| Control      | CC | Value spec                         | AUM mapping target |
|--------------|----|------------------------------------|--------------------|
| transport    | 20 | enum `{stop:0, start:127}`         | Transport → "Toggle play" (or Start play / Stop-rewind triggers) |
| ch1_mute     | 21 | enum `{unmute:0, mute:127}`        | Ch 1 "Channel Controls" → Mute toggle (Cycle off) |
| ch1_level    | 22 | range `0-127`                      | Ch 1 "Channel Controls" → Volume |
| ch1_pan      | 23 | range `0-127` (64 = center)        | Ch 1 → Stereo Balance node knob |
| ch2_mute     | 24 | enum `{unmute:0, mute:127}`        | Ch 2 → Mute toggle |
| ch2_level    | 25 | range `0-127`                      | Ch 2 → Volume |
| ch2_pan      | 26 | range `0-127` (64 = center)        | Ch 2 → Stereo Balance node knob |
| ch3_mute     | 27 | enum `{unmute:0, mute:127}`        | Ch 3 → Mute toggle |
| ch3_level    | 28 | range `0-127`                      | Ch 3 → Volume |
| ch3_pan      | 29 | range `0-127` (64 = center)        | Ch 3 → Stereo Balance node knob |
| ch4_mute     | 30 | enum `{unmute:0, mute:127}`        | Ch 4 → Mute toggle |
| ch4_level    | 31 | range `0-127`                      | Ch 4 → Volume |
| ch4_pan      | 32 | range `0-127` (64 = center)        | Ch 4 → Stereo Balance node knob |

The set covers transport + channels 1-4 as a representative slice; extend with the
same stride for more channels (ch5 → 33/34/35, …). `solo` and `rec` are exposed by
AUM too and can be added later (e.g. a parallel block once mute/level/pan settle).

## Generating an "AUM mapping cheat-sheet"

The project plans to emit a per-device cheat-sheet so configuring AUM is
mechanical (see `docs/design.md` → authoring tools). For AUM the cheat-sheet
should be derived directly from this YAML plus the binding's MIDI channel:

- **Inputs:** the bound MIDI channel `C`, and each control's `name` + `cc` from
  `aum.yaml`.
- **Output rows:** `MIDI channel C, CC <n>  →  <human target>` where the target is
  derived from the control name (`chN_mute` → "Channel N → Channel Controls →
  Mute"; `chN_level` → "Channel N → Channel Controls → Volume"; `chN_pan` →
  "Channel N → Stereo Balance node knob"; `transport` → "Transport → Toggle
  play"). The `description` field already carries these targets in prose, so a
  generator can lean on it.
- **Per-control hints to surface:** for `enum` mute controls, note "Toggle, Cycle
  **off**" (so >64 = on); for `range` pan, note "center = 64"; for `transport`,
  note the Trigger-fires-on-non-zero caveat.
- **Setup order for the user in AUM:** (1) connect this server's BLE-MIDI source
  to AUM's "MIDI Control" destination; (2) on each channel's collection use **Set
  MIDI Channels** to channel `C`; (3) LEARN or hand-enter each CC from the
  cheat-sheet; (4) optionally **Save** the collection mapping (stored under "On my
  iPad/AUM/MIDI Mappings") so it can be reloaded across sessions.

## Source URLs

- AUM help / manual (MIDI Control, Transport, MIDI routing):
  <https://kymatica.com/aum/help>
- Project convention model context: `docs/design.md` ("AUv3 plugins & AUM").
