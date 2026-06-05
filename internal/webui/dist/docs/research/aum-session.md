# AUM session (`.aumproj`) + MIDI-mapping file format — research

Research note for **Phase C** of the AUM roadmap: the *session-aware* mapping
layer (`cmd/aum-probe`, `import_aum_session`, `diff_aum_session`). Scope: the
on-disk structure of an AUM **session** (`.aumproj`) and of AUM's **standalone
MIDI-mapping** files, so the importer can (1) propose per-node bindings, (2)
match each plugin node to its `auv3-probe` dump for param-accurate definitions,
and (3) **diff** AUM's actual CC→parameter wiring against the server's
convention (`docs/research/aum.md`) — the build-time verification that stands in
for the absent MIDI echo (`docs/research/auv3-feedback.md`).

> **Why this depth matters.** How well we model the session *is* how much the
> in-host brain can control: it can only reach what the session maps. This format
> work is therefore pillar one of the "sessions + standard mapping → brain scene
> control" vision (`docs/aum-brain-control.md`), which also names this note's open
> gaps (the PC/PBEND/CHPRS `type` codes; AUM's globally-stored Session-Load
> actions that never appear in the file).

> **Status: schema verified by parsing 75 real sessions + a standalone map + two
> public samples** off-device on Linux with Python `plistlib` (the Go importer
> will use `howett.net/plist`; no `plutil` on Linux). Confirmed across **three
> session `version`s (8 / 10 / 13)**, sessions up to ~70 nodes / 36 channels, and
> ~78 distinct AUv3s: the `NSKeyedArchiver` structure, the full `AUMSession` key
> set, node identity + built-in-node state, **both** mapping-leaf encodings
> (packed `spec` and decomposed `specState`), the packed-`spec` bit layout, the
> message-`type` codes for **CC and note-style** messages, and the standalone
> **`.aum_midimap`** format. AUM **enumerates every mappable parameter** as a
> disabled placeholder, so most leaves are unassigned. Remaining open item: the
> exact `type` code for **Program Change / Pitch Bend / Channel Pressure** — no
> *enabled* mapping of those exists anywhere in the corpus to read.

## Samples

No private rig session is committed (real session/node/preset names are
**private** — see `.cursor/rules/public-vs-private.mdc`). The schema below was
reverse-engineered from two **publicly distributed** sample sessions, parsed in
a scratch dir and never committed:

- **Mapping-rich, no plugins** — `8_Tracks_Audio_BS.aumproj` from
  [mjm1138/Beatstep-AUM](https://github.com/mjm1138/Beatstep-AUM) (8 empty audio
  tracks + 2 MIDI tracks + master; a documented BeatStep CC map). Its
  `midiCtrlState` is the ground truth for the **`spec` CC encoding** because the
  repo README states the exact CC/channel assignments.
- **Plugin-rich, no mappings** — `Free-App-Playground.aumproj` from
  [patchstorage](https://patchstorage.com/wp-content/uploads/2022/10/Free-App-Playground.aumproj)
  (audio tracks hosting free AUv3s). Ground truth for **node identity**
  (`audioComponentDescription`, `componentName`) and the plugin **`fullState`**
  (`AuStateDoc`) shape.

- **Real rig sessions (private, not committed)** — **75** of the author's own
  AUM sessions (`version` 8/10/13), parsed locally to verify the schema across
  large, complex sessions (up to ~70 nodes / 36 channels, ~78 distinct AUv3s
  spanning instrument / effect / music-effect / MIDI-processor types) and to
  read **real mapping leaves** (older `version` 10 sessions carry an actual
  hardware-controller map; the current sessions are unmapped). Only **generic
  protocol facts** appear below; the rig itself (session/channel/plugin names,
  the controller map, counts) is documented in `docs/private/aum-projects.md`
  per the public-vs-private rule.

Together they cover everything an importer needs: the mapping tree + both leaf
encodings (BeatStep + the version-10 rig sessions), node/plugin identity and
`fullState` (Playground + every rig session), and the standalone map format.

## The crucial fact: `.aumproj` is an NSKeyedArchiver archive

`.aumproj` is an Apple **binary plist** (`bplist00`), but *not* a flat plist:
its top level is an **`NSKeyedArchiver`** object graph:

```
{ "$archiver": "NSKeyedArchiver", "$version": 100000,
  "$top": { "root": <UID 1> },
  "$objects": [ "$null", <root AUMSession>, … ] }   // flat object table
```

Decoding implications for the Go importer:

- Every value is either an inline scalar or a **`CF$UID`** index into the
  `$objects` array. You must **resolve UIDs** to walk the graph; object `0` is
  the string `"$null"` (AUM's nil).
- Each non-scalar object carries a `$class` UID pointing to a class-definition
  object whose `$classname` names the type (`AUMSession`, `AUMAudioStrip`,
  `AUMMIDIStrip`, `AUMNodeArchive`, plus Foundation `NSMutableDictionary` /
  `NSMutableArray` / `NSDictionary` / `NSArray`). `NS*` containers store their
  contents in `NS.keys` / `NS.objects`.
- `howett.net/plist` decodes the bplist into this raw `$objects`/`$top`
  structure (it does **not** run a keyed-unarchiver). The importer should
  decode into a generic structure (`map[string]interface{}` / a small typed
  shim) and then resolve UIDs itself — there is no Go `NSKeyedUnarchiver`. A
  thin helper `resolve(uid) -> object` plus class-name dispatch is enough; we
  only read a handful of classes.
- It is **read-only** for us. Re-emitting a valid `NSKeyedArchiver` graph (for
  Phase E mapping export) is a separate, harder problem — prefer emitting the
  standalone *MIDI-mapping* file format (below) over rewriting whole sessions.

## `AUMSession` (the root object)

Full key set seen on the root `AUMSession` across all versions (optional keys
are `$null`/absent when unused):

| Key | Type | Meaning / use for us |
|-----|------|----------------------|
| `version` | int | session schema version — **varies (8, 10, 13 observed)**; do **not** hardcode it. Drives the mapping-leaf encoding (see below). |
| `title` | string | session name — **private** |
| `folder` / `notes` | string | session folder / free notes — private |
| `sampleRate` | double | engine sample rate (e.g. 48000) |
| `minimumLatency` / `syncOffset` | double/int | session min-latency / sync offset |
| `channels` | array | ordered `AUMAudioStrip` / `AUMMIDIStrip` (the mixer) |
| `nodeArchives` | array | **parallel** to `channels`: one inner array of `AUMNodeArchive` per channel |
| `mixBusses` | array | AUM's internal mix-bus list (routing targets) |
| `hwBusses` | array | hardware I/O bus descriptors |
| `midiCtrlState` | dict | **the MIDI Control mappings** — the core for `diff_aum_session` |
| `midiMatrixState` | dict | the MIDI routing matrix (sources/dests/connections/filters/customNames) |
| `transportClockState` | dict | tempo/metronome/clock settings (see below) |
| `keyboardState` | dict/`$null` | on-screen keyboard state |
| `metroOutDesc` | `HWOutputDescription` | metronome output routing |

### Channels (`channels`)

An ordered array. Audio tracks are `AUMAudioStrip`; MIDI tracks are
`AUMMIDIStrip`; the last audio strip is the **master**. Fields:

| Field | Type | Notes |
|-------|------|-------|
| `index` | int | 0-based strip index; the **join key** to `midiCtrlState` (`"chan<index>"`) and to `nodeArchives[index]` |
| `title` | string/`$null` | user channel name — **private** |
| `nodeCount` | int | nodes in this strip's slot chain |
| `faderIndex` | int | which slot is the fader |
| `faderLevel` | double | current fader (audio strips only; `$null` on MIDI strips) |
| `muted` / `soloed` / `bookmarked` | bool | strip state |

The strip object does **not** embed its nodes; the per-channel node list lives
in `nodeArchives` at the same array position.

### Nodes (`nodeArchives` → `AUMNodeArchive`)

`nodeArchives[i]` is an array of `AUMNodeArchive` for `channels[i]`'s slot chain
(input slot, effect slots, fader/output). Each node:

| Field | Type | Meaning |
|-------|------|---------|
| `archiveDescClass` | string | node **kind** (see the full list below); `$null` for an empty slot |
| `audioComponentDescription` | data(20) | **only for AUv3 nodes** — the `AudioComponentDescription` struct (decode below); the match key to a probe dump |
| `componentName` | string | human `"Manufacturer: Plugin"` (e.g. `"Arturia: iSEM"`) |
| `fallbackTitle` | string | name shown if the plugin is missing — **private** |
| `componentIcon` | `UIImage` | ignore |
| `archiveNodeState` | dict | per-node state (plugin `fullState` and/or built-in params, bypass, main param — see below) |
| `busIndex` | int | for bus nodes: which AUM bus |
| `monoSelect` / `hwBusIndex` | int | for HW I/O nodes: channel select / hardware bus |
| `parentChannel` / `parentSlot` | int | the node's owning channel index + slot position |
| `isFilter` | bool | node is an inline filter |

**`archiveDescClass` values seen** (the node taxonomy): `AUXNodeDescription`
(a hosted AUv3 — the only one with `audioComponentDescription`), `AUXIONodeDescription`,
`IAANodeDescription` (Inter-App Audio), `FilePlayerNodeDescription` (audio file
player), `HWInputDescription` / `HWOutputDescription` / `HWSendDescription`
(hardware I/O), `BusSourceDescription` / `BusDestDescription` / `BusSendDescription`
(internal bus routing), `GainNodeDescription`, `PanDescription` / `BalDescription`
(pan / stereo balance), `MonoDescription` / `MidSideConvertDescription` /
`MidSideBalDescription` (mono / mid-side), `SatNodeDescription` (saturator),
`EQHiPassDescription` / `EQLowPassDescription` (filters), and `$null`.

#### Decoding `audioComponentDescription` (→ matches a probe dump)

The 20-byte blob is the C `AudioComponentDescription` struct, five `UInt32`s
stored **little-endian**: `componentType`, `componentSubType`,
`componentManufacturer`, `componentFlags`, `componentFlagsMask`. Each of the
first three is a **FourCC** — reverse each 4-byte group to render it:

| Raw bytes (hex) | LE chars | FourCC | Field |
|-----------------|----------|--------|-------|
| `756d7561` | `umua` | **`aumu`** | type = music device (instrument) |
| `78667561` | `xfua` | **`aufx`** | type = effect |
| `616d754e` | `amuN` | **`Numa`** | subtype (example) |
| `676f4c53` | `goLS` | **`SLog`** | manufacturer (example) |

The `componentType` is any standard AU type FourCC. Observed across the corpus
(all decode cleanly): **`aumf`** (music effect), **`aufx`** (effect), **`aumu`**
(instrument), **`aumi`** (MIDI processor), and rarely `aurx`/`aurg`. The importer
should treat the type as an opaque FourCC, not a fixed enum — what matters is
that the `{type, subtype, manufacturer}` tuple matches a probe dump.

So `audioComponentDescription` →
`{type:"aumu", subtype:"…", manufacturer:"…"}` — **exactly** the
`ProbeComponent` fields (`type`/`subtype`/`manufacturer`, FourCC strings) in
`internal/device/auv3probe.go`. That makes node→probe matching a direct
component-tuple lookup; `componentName` is the human fallback / sanity check.
(`componentFlags`/`Mask` are the trailing 8 bytes; not needed.)

#### `archiveNodeState` (plugin `fullState` + built-in params)

`archiveNodeState` is keyed by short strings. Some keys are **common to every
node kind**, some are **AUv3-only**, and **built-in nodes store their actual
parameter values here as named keys** (useful — pan/send/gain/EQ are readable
directly, no opaque blob):

| Key(s) | Scope | Meaning |
|--------|-------|---------|
| `AUMNode.bypassed` | all | node bypass state (bool) |
| `_version`, `AuContextName` | all | node-state version / context name |
| `AUMNode.AutoShow`, `AUMNode.LastZ`, `AUMNode.window*` (`windowMode`/`windowSize`/`windowPos`/`windowTopOfs`/`prevWindowMode`), `AuLastViewFrame`, `AUMNode.stats.save_time` | all | UI/window bookkeeping — ignore |
| `AuStateDoc` | AUv3 | the plugin's **`fullState`** (see below) |
| `AuMainParam`, `AuPresetCtrl`, `AuClockFactorPower`/`AuClockFactorCustom` | AUv3 | headline param keyPath, preset control, host-clock factor |
| `PanPosition` / `BalPosition` / `BanPosition` | built-in | pan / balance node value |
| `Gain` | built-in | gain-node value |
| `BusSendAmount`, `inBus` / `outBus` | built-in | send level / bus routing |
| `Freq` / `Reso` | built-in | EQ filter frequency / resonance |
| `MonoInput` / `MidSide` | built-in | mono-select / mid-side mode |
| `FilePlayer*` (`URLBookmark`, `Path`, `Loop`, `Sync`, `Tempo`, `BeatOffset`, `Normalize`, `UserRate`, `Enabled`) | FilePlayer | the audio-file player's source + transport (`Path`/`URLBookmark` are **private**) |
| `IAALatency` | IAA | Inter-App Audio latency |

**`AuStateDoc`** (AUv3 `fullState`) is a dict whose standard keys are `version`,
`manufacturer`, `type`, `subtype`, `data`, `name`, `presetName`, plus a
plugin-defined state blob under a vendor key (`jucePluginState` for JUCE plugins,
`FabFilterPluginState`, `ISEMPatch`, `StreamByter-Rules`, …). `AuStateDoc.data`
and the vendor blob are **plugin-defined and opaque** — the same caveat as
`docs/research/auv3-feedback.md` option 2: do **not** decode them for parameter
readback. Presets recalled by Program Change (from the probe dump's
`factoryPresets`/`userPresets`) are the right scene granularity, not `fullState`.
What `AuStateDoc` *is* good for: confirming a node really is the plugin we
matched (`type`/`subtype`/`manufacturer` echo the component) and reading
`AuMainParam` / `presetName`.

## `midiCtrlState` — the MIDI Control mappings (the core)

A nested dictionary tree mirroring AUM's **MIDI Control collections**
(`docs/research/aum.md` → "MIDI Control Collections"). Top level has three keys:

```
midiCtrlState
├─ "Transport" → { "Toggle Play": <leaf>, "Stop/Rewind": <leaf>, "Rewind": …,
│                  "Start Play": …, "Toggle Record": …, "Tap Tempo": …,
│                  "Receive MMC": <bool> }
├─ "System"    → { "_AUM:ShowSelf": <leaf>, … }     // system actions
└─ "Channels"  → { "chan<index>" → {
                     "Channel controls" → { "Mute":<leaf>, "Solo":<leaf>,
                                            "Rec enable":<leaf>, "Volume":<leaf> },
                     "slot<N>" → { "<paramName>":<leaf>, "Pan":<leaf>,
                                   "_AUMNode:Bypass":<leaf> },
                     … } }
```

Notes:
- `"chan<index>"` joins to `channels[].index`.
- Inside a channel: the `"Channel controls"` collection holds the strip
  controls (`Mute`, `Solo`, `Rec enable`, `Volume`); each node's collection is
  keyed `"slot<N>"` (its slot position) and contains that node's mappable
  parameters by **parameter name/identifier** (e.g. an AUv3's `keyPath`), plus
  reserved keys `"_AUMNode:Bypass"`, `"_AUMNode:FrontPlugin"` (Show & Front)
  and `"_AUMNode:TogglePlugin"` (Show/Hide) and, on a pan node, `"Pan"`.
- Reserved/internal targets use an underscore prefix: `"_AUMNode:Bypass"`,
  `"_AUMNode:FrontPlugin"`, `"_AUMNode:TogglePlugin"`, `"_AUM:ShowSelf"`
  (System → Switch to AUM), etc. (key strings confirmed from the probe capture).
- `"Receive MMC"` under Transport is a plain bool, not a mapping leaf.
- Dynamic collections (Session Load, Preset Load, Tempo Presets) are
  user-populated. A standalone Session Load `.aum_midimap` (below) shows their
  shape: keyed by **action name**, each with a `specState` leaf.

> **Key fact — AUM enumerates *every* mappable parameter.** A leaf exists for
> **every** channel control, node parameter, transport/system action and reserved
> target the session can map, **whether or not** the user mapped it. Real
> sessions therefore have **thousands** of leaves, almost all **unassigned**
> placeholders. So:
> - The collections are **never empty** in a non-trivial session; "this target
>   exists" tells you nothing — you must check the **assigned/enabled** flag.
> - `midiCtrlState` doubles as a **complete catalogue of mappable targets** for a
>   session (handy for proposing a mapping), but `diff_aum_session` must **filter
>   to assigned leaves only** (see the leaf encodings + the placeholder rule
>   below) or it will report the whole catalogue as "wired".

### The mapping leaf — two encodings (version-dependent)

A leaf is a dict. There are **two encodings**, and which one a session uses is
**driven by `version`** — the importer must handle both:

**(a) Packed `spec` (`version` 8 / 10):**

| Key | Type | Meaning |
|-----|------|---------|
| `spec` | int | **packed MIDI message** (type + number + channel) — decode below |
| `min` | double | input-range low (the "0 → 100%" min in AUM) |
| `max` | double | input-range high |
| `autoToggle` | bool | AUM's "Cycle" — toggles on non-zero (vs latch `>64`) |

**(b) Decomposed `specState` (`version` 13):**

| Key | Type | Meaning |
|-----|------|---------|
| `specState.enabled` | bool | **whether a MIDI trigger is assigned** (`false` = the placeholder; the target exists but is unmapped) |
| `specState.data1` | int | the data byte — CC#, note#, or PC# (0–127) |
| `specState.type` | int | message-**type enum** (see below) |
| `channel` | int | **0-based** MIDI channel: `0` = send/MIDI ch 1 … `15` = ch 16 (verified live 2026-06-05; the brain drives a leaf on `channel+1`). An OMNI sentinel, if any, is not yet corpus-confirmed — the AUM picker's "0 = OMNI" label does **not** match the stored value |
| `min` / `max` / `autoToggle` | — | as in (a) |

This is empirical: every `version` 13 session in the corpus uses `specState`;
every `version` 8/10 session uses packed `spec`. The standalone `.aum_midimap`
(current AUM) also uses `specState`. The two carry the same fields (`type`,
`data1`, `channel`); `specState` just makes them explicit and adds the
**`enabled`** flag. In `version` 13, **unmapped placeholders are
`enabled:false` with `type` null / `data1` null**; in `version` 8/10 the
placeholder is encoded in the packed `spec` itself (next section).

### Decoding the packed `spec`

`spec` packs message type, data byte and channel into one int. **Verified bit
layout** (across the version-8/10 rig sessions + the BeatStep sample):

```
channel = spec & 0x0F          // 0-based MIDI channel (0..15 → ch 1..16)
data1   = (spec >> 4) & 0x7F   // 7-bit data byte: CC#, note#, …
type    = spec >> 11           // message-type code (see table)
                               // i.e. spec = (type << 11) | (data1 << 4) | channel
```

**Message-`type` codes** (the high bits), with how each was established:

| `type` | Message | Evidence | `data1` |
|--------|---------|----------|---------|
| **0** | **Control Change** | **confirmed** — `Channel controls/Volume` = `0x0070+ch` = CC **7** on every channel (matches the BeatStep README *and* the rig's version-10 sessions) | CC# |
| **5** | **Note** | strongly supported — `Mute`/`Solo`/`Rec enable` carry notes **60/62/64**, transport `Rewind`/`Toggle Record`/`Tap Tempo` notes **82/81/90**, consistently across the version-10 sessions (a saved controller template) | note# |
| 4 | value-target default | the dominant leaf: `0x2000 \| channel`, i.e. `type 4, data1 0` — the **unassigned placeholder** for a continuous/value target (occasionally `data1>0` for a real assignment, so 4 is a real code whose *unset* form is `data1 0`) | 0 = unset |
| 6 | trigger-target default | `0x3000 \| channel` (`type 6, data1 0`) — the unassigned placeholder for **trigger/show** actions (`_AUM:ShowSelf`, `_AUMNode:FrontPlugin`) | 0 = unset |

> **The version-13 `specState` encoding uses a *different* `type` enum** — do
> not reapply the packed codes above to it. Confirmed 2026-06-05 from a
> hand-mapped probe capture (see `docs/aum-control-surface.md`): **0 = CC**,
> **1 = Note**, **2 = Program Change**, **3 = Pitch Bend / Channel Pressure**
> (the two share type 3 and are split by `data1`: `0` = PBEND, `1` = CHPRS).
> Unassigned placeholders are `{enabled:false, type:0, data1:0}` — the explicit
> `enabled` flag marks them, not a type-default trick. These are the codes the
> `aum` library writes/reads for v13 sessions and the standalone `.aum_midimap`.

For a **CC** mapping this reduces to **`spec = (cc << 4) | (channel-1)`** — all
`diff_aum_session` needs to compare AUM's real wiring to the convention CCs in
`aum.yaml`:

| Path in `midiCtrlState` | `spec` (hex) | decoded |
|-------------------------|--------------|---------|
| `chan0/Channel controls/Volume` | `0x0072` | CC **7**, ch (low nibble) |
| `chan0/Channel controls/Mute` | `0x2bc2` | **type 5 / note 60** |
| `chan0/Channel controls/Solo` | `0x2be2` | type 5 / note 62 |
| `chanN/slotM/<param>` (unmapped) | `0x2000\|ch` | type 4 / **data1 0** → placeholder |
| `System/_AUM:ShowSelf` (unmapped) | `0x3000\|ch` | type 6 / data1 0 → placeholder |

> **Placeholder rule (both encodings).** A leaf is an **actual mapping** only
> when assigned: `specState.enabled == true` (version 13), or — in the packed
> form — the `spec` is **not** the type-default-with-`data1 0` (`0x2000|ch` for
> value targets, `0x3000|ch` for triggers). `diff_aum_session` must apply this
> filter before comparing to the convention, or every (enumerated) target reads
> as "wired".

The codes for **Program Change / Pitch Bend / Channel Pressure** are **not**
pinned down: no *enabled* mapping of those exists anywhere in the corpus. The
standalone Session Load map defaulted its (disabled) `specState.type` to **`2`**,
and the Session Load collection is Program-Change-recallable, so `2` is the
leading candidate for **Program Change** — but unconfirmed (see open items). For
Phase C this is acceptable: the convention + probe-preset workflow are CC +
Program Change, and the diff's primary job is the CC parameter wiring.

### What this means for an *unmapped* session

The current rig sessions (`version` 13) are **unmapped** — every leaf is an
`enabled:false` placeholder (the user drives those by touch, not external MIDI).
Older `version` 10 sessions **do** carry a controller map (CC7 volume + notes on
mute/solo/rec). Consequences for Phase C:

- `import_aum_session` must work from the **channel + node structure alone**
  (propose bindings, match nodes to probes) and not depend on `midiCtrlState`
  carrying *assigned* mappings.
- `diff_aum_session` against an unmapped session should report "AUM is **not
  wired** to the convention yet" (every convention CC missing) rather than
  erroring — that *is* the useful answer pre-setup; Phase E's mapping **export**
  fills the gap.

## `transportClockState` + `midiMatrixState`

**`transportClockState`** — tempo / metronome / MIDI-clock settings. Keys seen:
`clockTempo` (BPM, double), `clockBeatsPerBar`, `clockSendMidi` (send MIDI
clock), `clockSendSPP` (song-position pointer), `clockMidiOffset` /
`clockMidiLatency` / `clockLinkOffset`, `clockMidiClockOutEndpoint`,
`clockMetronome` / `clockMetronomeLevel`, `clockPreRoll` /
`clockPreRollMetronome`, `clockSyncQuant`. `clockTempo` is the session BPM (a
useful generic readout for scenes that set tempo on the gear).

**`midiMatrixState`** — AUM's MIDI routing matrix (the "MIDI" tab), not the
control surface. Keys: `connections` (the source→destination edges),
`sourcesInfo` / `destsInfo` (the endpoints — names are **private**),
`customNames`, and `filters` (per-connection event filtering). Read this only if
routing needs to be diffed; the control surface for the convention is
`midiCtrlState`.

## How Phase C consumes this

- **`cmd/aum-probe`** (off-daemon, mirrors `internal/auv3receiver`): decode the
  bplist with `howett.net/plist`, resolve the `NSKeyedArchiver` graph, and emit
  a flat **session map** JSON: ordered channels (kind, index, fader/mute/solo),
  per channel its nodes (`archiveDescClass`; for AUv3 the decoded component
  tuple + `componentName` + `AuMainParam`), and the `midiCtrlState` mappings
  flattened to `{collection, target, type, number, channel, min, max,
  autoToggle}`. Stage under `StateDir()/aum-sessions/` (gitignored — channel
  names, plugin set, and mappings are a **private rig snapshot**).
- **`import_aum_session`**: from the session map, propose `devices.yaml`
  (the AUM mixer + each plugin node on distinct MIDI channels), match each AUv3
  node to its staged probe dump by **component tuple** for param-accurate
  definitions, and emit a session-specific cheat-sheet with the real channel
  numbers + node names.
- **`diff_aum_session`**: decode each CC mapping's `spec`
  (`cc`, `channel`) and compare to the definition+binding convention — "is AUM
  actually wired to match?" This is the verification that replaces the missing
  MIDI echo.

## Standalone MIDI-mapping files (`On my iPad/AUM/MIDI Mappings`)

AUM can **Save/Load** the MIDI mappings of a single collection as a standalone
file (AUM help → "Save/Load mappings"): files live in the iOS Files app under
`On my iPad/AUM/MIDI Mappings`, can be shared/renamed, and on **Load** AUM
matches a saved collection to existing nodes by **kind and order** (it skips
nodes absent from either side and preserves "first Bus Send" vs "second Bus
Send" ordering — it ignores exact slot location). This is what makes a
generated mapping **importable**, not just a printed cheat-sheet, and is the
basis for Phase E (emit loadable mappings so AUM setup is "Load", not
hand-entry).

**Format (verified from a real `Session Load` map):**

- **Extension `.aum_midimap`**, stored as `…/AUM/MIDI Mappings/<Collection
  Kind>/<name>.aum_midimap` (e.g. `MIDI Mappings/Session Load/<name>.aum_midimap`).
- Same container as a session: **`bplist00` / `NSKeyedArchiver`**, root an
  `NSMutableDictionary` (resolve `CF$UID` refs the same way).
- The root dict is **one collection**: target/action names → leaf, plus meta
  keys:
  - `_collection_map_name` — the collection kind (e.g. `"Session Load"`).
  - `_collection_editor_states` — array of UI editor state; ignore.
  - collection-specific extras (a Session Load map also had `"Force Link
    Tempo": <bool>`).
- Leaves use the **decomposed `specState` encoding** (`{enabled, data1, type}`
  + `channel` + `min`/`max`/`autoToggle`) described above.

This confirms the Phase E direction: emit a `.aum_midimap` per collection
(node-kind/order-matched on Load, per AUM's matching rule) rather than rewriting
whole `NSKeyedArchiver` sessions. The container and leaf shape are known; the
standalone map uses the same **decomposed `specState`** leaf as a `version` 13
inline session, so the export format is identical to what current sessions hold.
The only remaining export unknown is the PC/PBEND/CHPRS `type` codes (open item).

## Open items / TODO

1. **PC / Pitch-Bend / Channel-Pressure `type` codes.** The remaining gap.
   `type 0 = CC` and `type 5 = Note` are established; the corpus has **no
   enabled** PC/PBEND/CHPRS mapping to read. To fix: in AUM, MIDI-learn one
   action of each kind (a **Program Change** on a Preset/Session Load action is
   the priority — needed for Phase D), save the collection as a `.aum_midimap`,
   and read `specState.type`. Leading guess: PC = `2` (the disabled Session Load
   default).
2. **`.aum_aupreset` (Phase D).** AUM stores user AU presets as
   `*.aum_aupreset` under `AUM/Audio Unit Presets/<hex component id>/` (the
   corpus has a `4272616D6175706F61756D75`-style folder). These are the
   PC-recallable presets the scene workflow targets; document their format from
   a materialized (non-placeholder) sample.
3. **Mapping export round-trip.** `specState`/`spec` write semantics for Phase E
   export — emit a `.aum_midimap` (the easier target) and confirm AUM loads it.

Resolved since the first draft: session-key set, node taxonomy, built-in-node
state, **both** leaf encodings + their version split, the packed `spec` layout,
the CC/Note `type` codes + the placeholder rule, and `transportClockState` /
`midiMatrixState` key sets — all now verified above against 75 sessions.

## Sources

- AUM help / manual (MIDI Control collections, Save/Load mappings, transport):
  <https://kymatica.com/aum/help>
- Standalone mapping file format (`.aum_midimap`, `bplist00`/`NSKeyedArchiver`,
  per-collection, decomposed `specState` leaf) and `.aum_aupreset` storage:
  verified by inspecting a real `AUM/MIDI Mappings/Session Load/*.aum_midimap`
  (private; only the generic format is recorded here).
- Sample sessions: [mjm1138/Beatstep-AUM](https://github.com/mjm1138/Beatstep-AUM)
  (documented CC map) and the
  [patchstorage Free App Playground](https://patchstorage.com/wp-content/uploads/2022/10/Free-App-Playground.aumproj).
- Apple binary plist format (`bplist00` header/object-table/offset-table/trailer)
  and `NSKeyedArchiver` (`$archiver`/`$objects`/`$top`/`$version`, `CF$UID`):
  CFBinaryPList.c; <https://en.wikipedia.org/wiki/Property_list>.
- `AudioComponentDescription` (type/subtype/manufacturer FourCCs):
  Apple `AudioComponent.h`.
- Go plist decoder: `howett.net/plist`.
- Project context: `docs/research/aum.md` (convention + message types),
  `docs/research/auv3-feedback.md` (probe dumps, open-loop posture,
  `fullState` opacity), `internal/device/auv3probe.go` (`ProbeComponent`),
  the AUM roadmap (Phase C).
```

