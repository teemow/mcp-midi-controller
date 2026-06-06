# AUM (iPad host) — MIDI control research

Research note for the device definition at
`internal/device/device-types/aum.yaml`. Scope: what AUM **can be controlled by**
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

> **Now measured, and the reason it matters.** That AUM responds to these mapped
> messages is no longer just from the help page — the in-host `ProbeMidiBrain`
> has driven tempo (20–500 BPM), channel mute, and a node parameter through AUM's
> MIDI Control end-to-end (auv3-probe
> [aum-control-surface.md](https://github.com/teemow/auv3-probe/blob/main/docs/aum-control-surface.md)).
> The convention map below is therefore the **control surface the brain gets** —
> author it into every session and the brain can run the whole rig, including
> scene changes. The strategy + open gaps: `docs/aum-brain-control.md`.

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
  ("Set MIDI Channels", 1-16 or 0=OMNI). Note this is the *UI* label: on disk
  the channel is stored **0-based** (UI ch 1 → stored `0`, ch 16 → `15`;
  verified live 2026-06-05). In this project the channel is **not** stored in
  the definition — it is supplied by the binding, so set the AUM collection's
  channel to match the binding (the brain drives a leaf stored `N` on send
  channel `N+1`).
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

### Per channel — sends
- **Bus Send** is a node placed in an effect slot (AUM has no fixed per-channel
  send matrix; routing is node-based). Its **send-amount knob** is MIDI-
  controllable as a node parameter, exactly like the Stereo Balance pan knob. So
  `chN_send1` in the convention maps to the first Bus Send node's knob on that
  channel. Add more sends with the same approach (`send2`, …).

### Global transport — "Transport Control"
- **MMC (SysEx)** via "Receive MMC" (a Transport toggle). When enabled, AUM
  reacts to these MMC SysEx commands (`F0 7F <dev> 06 <cmd> F7`, `dev` = device
  id, `0x7F` = all-call):
  - Play `0x02` (also `0x03` deferred play)
  - Stop `0x01` (also rewinds if already stopped)
  - Pause `0x09`
  - Rewind `0x05`
  - Record `0x06`
  - Goto/Locate `0x44` (needs a target time argument — not a single-byte command)
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

## Convention CC map

All CCs ride the **MIDI channel supplied by the binding**. Numbers sit in MIDI's
general-purpose/undefined CC range to avoid clashing with common controller
defaults (mod wheel, sustain, bank select, etc.). The map covers AUM's full
mappable host surface; `aum.yaml` materializes it for **channels 1-8**.

### Per-channel mixer block (interleaved, stride 3), channel `N` = 1..8
- `mute  = 18 + 3*N`  → ch1=21 ch2=24 … ch8=42 — "Channel Controls" Mute toggle
- `level = 19 + 3*N`  → ch1=22 ch2=25 … ch8=43 — "Channel Controls" Volume
- `pan   = 20 + 3*N`  → ch1=23 ch2=26 … ch8=44 — Stereo Balance node knob (64 = center)

### Per-channel parallel blocks, channel `N` = 1..8
- `solo   = 44 + N`  → 45..52 — "Channel Controls" Solo toggle (Cycle off → >64 = solo)
- `rec    = 52 + N`  → 53..60 — "Channel Controls" Rec toggle (Cycle off → >64 = armed)
- `scroll = 60 + N`  → 61..68 — "Scroll to this channel" trigger (fires on non-zero)
- `send1  = 68 + N`  → 69..76 — first Bus Send node's send-amount knob on the channel

### Transport / system (single toggle + undefined-CC block)

| Control                 | CC  | Value spec                  | AUM mapping target |
|-------------------------|-----|-----------------------------|--------------------|
| transport               | 20  | enum `{stop:0, start:127}`  | Transport → "Toggle play" (start on >0; 0 won't stop) |
| transport_start         | 102 | enum `{trigger:127}`        | Transport → "Start play" |
| transport_stop          | 103 | enum `{trigger:127}`        | Transport → "Stop/rewind" (deterministic stop) |
| transport_rewind        | 104 | enum `{trigger:127}`        | Transport → "Rewind" |
| transport_toggle_record | 105 | enum `{trigger:127}`        | Transport → "Toggle record" |
| transport_prev_bar      | 106 | enum `{trigger:127}`        | Transport → "Previous bar" |
| transport_next_bar      | 107 | enum `{trigger:127}`        | Transport → "Next bar" |
| tap_tempo               | 108 | enum `{trigger:127}`        | Transport → "Tap tempo" |
| tempo                   | 109 | range `0-127`               | Transport → "Tempo" Value (0-127 maps to AUM's configured BPM range) |
| metronome               | 110 | enum `{off:0, on:127}`      | Transport → "Metronome on/off" toggle |
| unsolo_all              | 111 | enum `{trigger:127}`        | System action → "Unsolo Channels" |
| hide_plugins            | 112 | enum `{trigger:127}`        | System action → "Hide/Unhide Plugins" |
| switch_to_aum           | 113 | enum `{trigger:127}`        | System action → "Switch to AUM" |

### Post-fader tap toggles — reserved channel (the brain's "ears" switch)

Authored sessions place a post-fader `ProbeAudioTap` (an `aufx` node) in tapped
audio channels so the agent can listen to each channel's signal. Each tap's
**bypass** is the on/off switch for that tap's audio stream, and the brain flips
it over MIDI so it can choose which channel to listen to at runtime.

The convention maps each tap's node **`_AUMNode:Bypass`** target to a unique CC,
all on a **reserved MIDI channel of their own** — **channel 16** (stored `15`),
separate from the mixer/node/transport channel a binding supplies. Riding their
own channel is what keeps tap CCs from ever colliding with the mixer (CC 20–76)
or node-parameter (CC 30+) blocks, whatever channel those use. Because channel
16 is reserved for taps, the mixer/node convention channel must stay in **1–15**.

- Tap `N` (1-based, in channel order) → CC `76 + N` → **77, 78, … 95** —
  ProbeAudioTap node **Bypass** toggle, on channel 16.
- **AutoToggle (Cycle) ON**, so a single non-zero CC value *flips* the tap on/off
  (a momentary brain pulse toggles the stream) rather than latching like the
  mute/solo toggles. The brain emits the tap's CC to start/stop that tap.
- The block is **77–95** (19 slots), clear of the mixer (≤76), transport/system
  (≥102) and the MIDI-reserved 96–101 / 120–127 ranges, so it stays distinct
  even if ever read on a shared channel. A session with more than 19 taps
  overflows the block; the extra taps stay unassigned placeholders.

Single source of truth: `device.TapControlChannel` + `device.ConventionTapCC`;
`internal/aum` (`applyConvention`) wires it into every authored session.

### Session / Preset load — Program Change

| Control      | Type           | Value spec     | AUM mapping target |
|--------------|----------------|----------------|--------------------|
| session_load | program_change | range `0-127`  | MIDI Control → "Session Load" actions (one per session, each on a distinct PC) |
| preset_load  | program_change | range `0-127`  | a plugin node's "Preset Load" actions (one per preset, each on a distinct PC) |

Both are Program Change. Because the bound MIDI channel is shared, mapping both
on the **same** channel makes one PC fire both — bind them on **separate
channels** (or drive presets through the per-plugin auv3 probe workflow) to keep
them independent. The scene engine sends PC before CC, so these recall first.

### MMC over SysEx (alternative transport path)

Channel-less SysEx (the binding channel does not apply). Requires AUM Transport
→ **"Receive MMC"** enabled. Template `F0 7F 7F 06 <cmd> F7` (device id `7F` =
all-call).

| Control     | SysEx                 | MMC cmd |
|-------------|-----------------------|---------|
| mmc_play    | `F0 7F 7F 06 02 F7`   | Play    |
| mmc_stop    | `F0 7F 7F 06 01 F7`   | Stop (also rewinds if already stopped) |
| mmc_pause   | `F0 7F 7F 06 09 F7`   | Pause   |
| mmc_rewind  | `F0 7F 7F 06 05 F7`   | Rewind  |
| mmc_record  | `F0 7F 7F 06 06 F7`   | Record  |

Extend with the same strides for more channels/sends as a rig needs. The block
layout keeps the mixer (20-76), transport/system (102-113), and NRPN/RPN-reserved
CCs (98-101) clear of each other. Post-fader tap toggles sit in their own block
(77-95) on the reserved tap channel (16), so they never collide with any of the
above (see "Post-fader tap toggles").

## Generating an "AUM mapping cheat-sheet"

The project plans to emit a per-device cheat-sheet so configuring AUM is
mechanical (see `docs/design.md` → authoring tools). For AUM the cheat-sheet
should be derived directly from this YAML plus the binding's MIDI channel:

- **Inputs:** the bound MIDI channel `C`, and each control's `name` + `cc` from
  `aum.yaml`.
- **Output rows:** `MIDI channel C, CC <n>  →  <human target>` where the target is
  derived from the control name (`chN_mute`/`_solo`/`_rec` → "Channel N → Channel
  Controls → Mute/Solo/Rec"; `chN_level` → "… → Volume"; `chN_scroll` → "… →
  Scroll to this channel"; `chN_pan` → "Channel N → Stereo Balance node knob";
  `chN_send1` → "Channel N → first Bus Send node knob"; `transport*`/`tap_tempo`/
  `tempo`/`metronome` → the matching Transport item; `session_load`/`preset_load`
  → the matching Program Change action; `mmc_*` → SysEx, no channel). The
  `description` field already carries these targets in prose, so a generator can
  lean on it.
- **Per-control hints to surface:** for `enum` toggle controls (mute/solo/rec/
  metronome), note "Toggle, Cycle **off**" (so >64 = on); for `range` pan, note
  "center = 64"; for trigger controls (scroll/transport_*/system actions), note
  the Trigger-fires-on-non-zero caveat; `program_change` controls are not CC and
  map to PC actions; `mmc_*` are channel-less SysEx.
- **Setup order for the user in AUM:** (1) connect this server's BLE-MIDI source
  to AUM's "MIDI Control" destination; (2) on each channel's collection use **Set
  MIDI Channels** to channel `C`; (3) LEARN or hand-enter each CC from the
  cheat-sheet; (4) optionally **Save** the collection mapping (stored under "On my
  iPad/AUM/MIDI Mappings") so it can be reloaded across sessions.

## Source URLs

- AUM help / manual (MIDI Control, Transport, MIDI routing):
  <https://kymatica.com/aum/help>
- Project convention model context: `docs/design.md` ("AUv3 plugins & AUM").
