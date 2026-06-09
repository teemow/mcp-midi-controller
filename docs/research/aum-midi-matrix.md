# AUM MIDI matrix + node AU-state (`midiMatrixState`, `AuStateDoc`)

Reverse-engineered from the local real-session corpus (`AUM_CORPUS`, never
committed) with `cmd/aumdump` (a throwaway analysis tool). This is the format
authoring needs to wire `ProbeMidiBrain` (MIDI out) into a synth and place
`ProbeAudioTap` on the synth's channel, and to author each plugin's saved state
(brain program, tap host/streaming config, synth preset). See also
`aum-session.md` for the overall `.aumproj` NSKeyedArchiver graph.

## `root["midiMatrixState"]`

An `NSDictionary` with exactly five keys (AUM applies the whole matrix on load):

| key           | type                  | shape |
|---------------|-----------------------|-------|
| `connections` | `NSMutableDictionary` | `sourceKey -> NSMutableArray[destKey, …]` |
| `sourcesInfo` | `NSMutableDictionary` | `sourceKey -> NSArray[displayName, category, ""]` |
| `destsInfo`   | `NSMutableDictionary` | `destKey   -> NSArray[displayName, category, ""]` |
| `filters`     | `NSMutableDictionary` | `destKey   -> NSDictionary{filter fields}` |
| `customNames` | `NSMutableDictionary` | usually empty |

### Endpoint keys

`Chan` is the 0-based channel index, `Slot` the 0-based node slot in that channel.

- Node MIDI **output** (a source): `Node:Chan<C>:Slot<S>:<port name>`, where
  `<port name>` is the AU's **declared MIDI-output port name** — e.g.
  `Node:Chan0:Slot1:photonAU Pad5`, `Node:Chan6:Slot2:PatternBud Out`,
  `Node:Chan6:Slot3:OutByter`. Only an AU that declares **no** named port falls
  back to the literal `MIDI OUT` label.

  > **Verified live (2026-06-09): the port name is load-bearing, and a mismatch
  > fails silently.** A from-scratch session authored with
  > `Node:Chan0:Slot0:MIDI OUT` for `ProbeMidiBrain` loaded fine in AUM but the
  > matrix showed **no connection at all** — no error, the route is just
  > dropped, so the brain reaches neither the synth nor MIDI Control (notes and
  > CC mappings are both dead). The brain declares a named output port
  > `ProbeMidiBrain` (visible in its host diagnostics under
  > `render.midiOutputNames`); patching the connection/sourcesInfo key to
  > `Node:Chan0:Slot0:ProbeMidiBrain` and reloading made AUM apply the route.
  > `SetMIDIRoutes` (`internal/aum/route.go`) and `mergeMIDIControlRoute`
  > (`internal/aum/probe_rig.go`) still hardcode `MIDI OUT` — they must use the
  > source AU's real port name (for our plugins: `ProbeMidiBrain`; for
  > third-party MIDI nodes, take it from the corpus/probe — see the examples
  > above).
- Node MIDI **input** (a destination): `Node:Chan<C>:Slot<S>` (no port suffix).
- Built-ins: `BuiltIn:MIDI Control`, `BuiltIn:Keyboard`, `BuiltIn:IAA Port N`.
- CoreMIDI: `CoreMIDIDest:<name>` / `CoreMIDISrc:<name>`.
- AUM virtual: `CoreMIDIDest:'AUM' Source`, `CoreMIDISrc:'AUM' Destination`,
  `AUM_MIDI_Clock_Src`.

### `connections`

Maps each source key to an array of destination keys it feeds. A brain → synth
wire is simply:

```
connections["Node:Chan<brainC>:Slot<brainS>:MIDI OUT"] = ["Node:Chan<synthC>:Slot<synthS>"]
```

Add `"BuiltIn:MIDI Control"` to that array to also drive AUM's MIDI Control /
transport from the brain. This wire is what makes the brain a near-complete AUM
remote — the measured surface and the "sessions + standard mapping" vision it
serves are in `docs/aum-brain-control.md`.

### `sourcesInfo` / `destsInfo`

Metadata, one entry per endpoint that participates. Value is a 3-element
`NSArray` `[displayName, category, ""]`. `category` ∈ `{"Audio Unit",
"Built-in", "Network", "Virtual", "Inter-App Audio"}`. The third element is the
empty string. AUM rebuilds these on load, but authoring them keeps the matrix
self-consistent.

### `filters`

One entry per destination, keyed by the dest key. The default pass-through
filter (verified leaf values):

```
{
  "channelFilter": 65535,   // 0xFFFF = all 16 channels pass
  "transpose":     0,
  "startNote":     0,
  "endNote":       127,
  "skipByType":    0,
  "skipCC0":       0,        // false
  "skipCC1":       0         // false
}
```

## Node AU state: `archiveNodeState["AuStateDoc"]`

Each hosted node is an `AUMNodeArchive` in `root["nodeArchives"]`. Its
`archiveNodeState` (`NSMutableDictionary`) carries AUM bookkeeping plus the
plugin's saved state:

```
archiveNodeState keys:
  AUMNode.bypassed         bool
  AUMNode.LastZ            (window z-order)
  AUMNode.AutoShow         bool
  AuMainParam              string (headline param keyPath)
  AuLastViewFrame          (UI frame)
  AUMNode.stats.save_time  (timestamp)
  AuStateDoc               <- the plugin's fullState
```

`AuStateDoc` is the AU's `fullState` dictionary stored directly in the graph
(`NSDictionary` of `string -> NSData`). For our two plugins the keys are exactly
the ones their AUs read in their `fullState` setters:

- `ProbeMidiBrain`: `{"probeMidiBrainProgram": <JSON Data>, "probeMidiBrainConfig": <JSON Data>}`
- `ProbeAudioTap`:  `{"probeAudioTapConfig": <JSON Data>}`

The values are the raw JSON bytes `JSONEncoder` produces for `BrainProgram` /
`BrainConfig` / `TapConfig`. Authoring `AuStateDoc` therefore lets the daemon set
the brain's song program, the tap's daemon host + streaming flag, and the
brain's control host + enable flag — entirely from the session file.

For a third-party synth we cannot author its private state, but `AuPresetCtrl`
in `archiveNodeState` selects a numbered factory preset (the existing
`SetPreset`).

## Decisions

- **Host config mechanism: authored `AuStateDoc`.** Because the tap/brain host
  lives in their `fullState` (`TapConfig.host` / `BrainConfig.host`) and that is
  exactly what `AuStateDoc` carries, the daemon can author the LAN host straight
  into the session — no App Group / shared container (which is fragile under
  free-Apple-ID signing) is needed.
- **Tap placement: same-channel insert.** Place `ProbeAudioTap` as a later slot
  in the synth's own channel so audio flows through the slot chain. This avoids
  authoring cross-channel audio bus routing (`audioMatrixState`), which is not
  needed for the loop.
