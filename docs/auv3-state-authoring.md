# AUv3 fullState authoring (capture-and-mutate)

Extends the **auv3 default state** plan
(`.cursor/plans/auv3_default_state_e0c5b55f.plan.md`,
[docs/auv3-probe-app-plan.md](auv3-probe-app-plan.md)). That plan stores a
per-audio-unit default `AuStateDoc` as a YAML container with `text` entries for
our own plugins and **opaque `base64`** entries for third-party `fullState`.
This doc is about shrinking the opaque slice: turning as much of that captured
state as possible into something an agent can *introspect and precisely edit*,
so session generation can persist exact per-plugin configuration — not just
recall presets and drive a curated CC subset live.

## Why this matters (the capability gap)

A plugin's parameter values are **not** a separate, editable field in the
session. They live *inside* the opaque `fullState` blob: at load AUM
instantiates the AU and calls its `setFullState` with the `AuStateDoc` bytes.
There is no "set param 12 = 0.7" knob in the file. So today there are only two
ways to get a plugin into a target state, both deliberate (see the open-loop
posture in [docs/research/auv3-feedback.md](research/auv3-feedback.md)):

1. **Recall a preset** by Program Change — coarse, only states the vendor saved.
2. **Drive parameters live via MIDI CC** after load — only writable, addressable
   `AUParameter`s, only the curated CC budget, and *runtime* (the file does not
   open already in that state; a live brain has to dial it in).

What is missing is **persisted, arbitrary, precise per-plugin state baked into a
generated session**. That requires authoring `fullState` — which means
understanding (enough of) the blob. The good news from the survey below is that
"the blob" is, for a large fraction of real plugins, not opaque at all.

## Key finding: fullState leaves are not uniformly opaque

A survey of the live rig's `AuStateDoc` leaves (rig-specific results in
[docs/private/auv3-fullstate-survey.md](private/auv3-fullstate-survey.md)) shows
the per-key blob falls into a small set of encodings, and a large share is
**already structured text**:

| Class | Examples (vendor key) | Authorable? |
|-------|-----------------------|-------------|
| **JSON** | PatternBud `data`, Moog Model D `State`, KORG Module `globalData`/`kernelData`, Atom Piano Roll² per-field | Yes — parse/edit/re-emit |
| **XML** (JUCE) | `jucePluginState` (Audio Damage, Caelum, BABY Audio, Audible Genius, Continua), UVI `<ModulePreset>` | Yes — with a small binary prefix preserved (see below) |
| **NSKeyedArchiver bplist** | cpio `data`, FPKT DigiStix `seq`, Agonizer `sequencerState` | **Yes — same format as `.aumproj`; reuse `internal/aum/archive.go`** |
| **Structured binary** (name→float table) | most `data` keys (iSEM, KORG, Moog, Agonizer, SynthMaster, Zeeon) | Partially — recognizable `[name][float32]` records |
| **Proprietary opaque** | FabFilter `FFBS`, iSEM `ISEMPatch` (RIFF), Loopy `Document` (rtfd), Bram Bos `SETTINGS` | No — keep `base64`, use preset/macro CC |

Two structural facts drive the design:

- **Many plugins store a readable twin beside a binary `data`.** A JUCE plugin
  carries both an opaque `data` and a full `jucePluginState` XML; Moog carries
  both `data` (binary) and `State` (JSON); KORG carries `data` plus JSON
  `globalData`/`kernelData`. When a readable twin exists, we never need to
  decode the binary sibling.
- **The biggest opaque blobs are recall-only state, not knob-by-knob config.**
  The dominant opaque case (the FabFilter mastering chain) is set-and-forget /
  preset-recalled in practice — exactly where precise persisted authoring adds
  least. The introspectable cases are concentrated in the MIDI-brain and synth
  plugins — exactly where it adds most.

## The obstacle is re-encoding, not decoding

Reading a blob is far easier than producing one the plugin accepts. Proprietary
binary formats carry length prefixes, version tags, checksums, internal offsets,
sometimes compression. Two consequences shape the design:

- **Always capture-then-mutate**, never synthesize from scratch. Start from a
  real, plugin-emitted blob (the plan's `capture_auv3_default_state`), edit only
  the fields we understand, and leave the rest byte-for-byte. This sidesteps
  format-validity reproduction almost entirely.
- **Text/JSON/XML is the safe zone.** Re-serializing well-formed XML/JSON
  round-trips robustly (lenient parsers). Binary editing is restricted to
  whole-record replacement of a captured-valid blob, or skipped.

## Open empirical question (must verify before trusting mutation)

When a plugin exposes both a binary `data` and a readable twin
(`jucePluginState` / `State` / `globalData`), **which key does the plugin
authoritatively read on load?** Editing the twin only changes the loaded state
if the plugin reads the twin (or derives `data` from it). This is per-plugin and
must be confirmed by an on-device round-trip test (capture → mutate one known
field → author a minimal session → load → save → re-capture and compare) before
the mutation path is trusted for that plugin. Until confirmed, a plugin stays
capture-only (faithful base64/text), which is always safe.

### Verified per-plugin results

- **KORG Module** (`aumu/frdl/KORG`) — **twin authoritative; mutation trusted.**
  On-device round-trip (2026-06): captured its `fullState` from a real session,
  set `kernelData.root.programName` and `kernelData.root.parameters[0]` to
  sentinels (leaving the binary `data` deliberately disagreeing), authored a
  minimal one-node session via `BuildSession`, loaded + saved it in AUM, and
  re-captured. Result: the sentinels survived and `parameters[0]` came back
  re-serialized as `float32(0.4242) = 0.42419999837875366` — i.e. KORG parsed
  the mutated `kernelData`, ingested the value into its engine, and re-emitted
  it. The binary `data` stayed byte-identical, so it is not the parameter store.
  The JSON `kernelData`/`globalData` twins are the authoritative, editable state.

The empirical surprise from the same rig survey: on this rig the JUCE
`jucePluginState` twins (Audio Damage, BABY Audio, Caelum) capture as **opaque
`base64`, not XML** — they are compressed/wrapped, so Tier-1 XML editing does not
apply to them as-is (a decompression step would be needed first). The editable
twins that actually exist here are **JSON** (KORG, PatternBud, Moog `State`).

## Design

### StateEntry encodings

`device.StateEntry` is a deliberately small, **byte-exact** schema so capture
round-trips losslessly. Exactly one encoding per entry (validated):

```yaml
state:
  data:                    # our own JSON / a JSON synth / a JUCE XML body
    text: '{"host":"box:7800"}'
  jucePluginState:         # a text body behind a short binary header (JUCE "VC2!")
    prefix: "VkMyIQ=="     #   base64 of the leading header bytes, preserved verbatim
    text: |
      <?xml ...?><Preset/>
  ISEMPatch:               # opaque binary, untouched
    base64: "UklGRv..."
```

- `text` → `bytes = []byte(text)`.
- `text` + `prefix` → `bytes = base64-decode(prefix) || []byte(text)` (handles a
  short binary header in front of a text body).
- `base64` → `bytes = base64-decode(base64)`.

XML and JSON are stored as `text` because they *are* UTF-8 — this keeps the
storage model a clean text-vs-binary split and guarantees byte-exact
re-encoding. **Structured (set-by-path) editing** of those text bodies, and the
**`archive` form** for NSKeyedArchiver `bplist` leaves (decoded via
`internal/aum`), are layered on top later (Tiers 1–2 below) and do not change
this storage contract.

### Tier 0 — text detection on capture (do first; cheap, high value)

Classifier in the capture path (the throwaway scanner already prototypes it):

1. `bplist00` + `$archiver` → `archive` (decode via `internal/aum`).
2. leading `<?xml`/`<` (after an optional short binary prefix) → `xml`
   (split off and base64 the prefix).
3. leading `{`/`[`, valid UTF-8, parses as JSON → `json`.
4. printable-ratio ≥ 0.95 and valid UTF-8 → `text`.
5. else → `base64`.

This alone makes a large class of third-party state human- and agent-readable
with no per-plugin work.

### Tier 1 — field-level mutation of text/structured state

For `xml` / `json` entries, set-by-path edits via the MCP tool
`set_auv3_default_state_field` (`id`, `key`, `path`, `value`, `delete`). The
entry's format is auto-detected and only the addressed path is rewritten;
everything else — sibling fields and the binary `prefix` — is preserved. This is
the capture-and-mutate core: precise edits on a known-valid base. (`archive`
entries are Tier 2.)

- **JSON** edits go through `tidwall/sjson`+`gjson`, which rewrite only the
  addressed path and keep the rest of the document (key order, formatting) byte-
  stable. Paths are gjson dot syntax: `host`, `mixer.0.gain`, `voices.-1`
  (append). `delete: true` removes the path.
- **XML** (JUCE `jucePluginState` and similar) edits go through `beevik/etree`.
  Paths are slash-separated element steps relative to the root's children, each
  optionally `Name[index]` (0-based) or `Name[@attr=value]`, with a trailing
  `@attr` to target an attribute (otherwise the element's text). Example:
  `PARAM[@id=cutoff]/@value`. `value` must be a scalar; the binary `prefix` is
  re-prepended unchanged.
- **Opaque `base64`** entries have no addressable structure and are rejected;
  replace them wholesale with `set_auv3_default_state`.

### Tier 2 — reuse the NSKeyedArchiver codec

`bplist` leaves are the *same* format as `.aumproj`. `internal/aum/archive.go`
already decodes and re-encodes it. Wiring `archive`-typed entries through it
gives full read/write of sequencer/preset state (cpio, DigiStix, Agonizer) for
free — the single highest-leverage reuse in this design.

### Tier 3 — structured binary param tables (optional, deferred)

Most binary `data` blobs are `[name][float32]` records (param name as
UTF-16-ish text, then a little-endian float). A generic reader could expose them
read-only for inspection/diffing. Editing is risky (record sizing, sibling
consistency) and usually unnecessary when a readable twin exists, so defer; if
pursued, restrict to in-place float replacement of a captured-valid blob.

### Tier 4 — proprietary opaque (keep base64)

FabFilter `FFBS`, iSEM `ISEMPatch` (RIFF/OBSEPRUID), Loopy `rtfd`, Bram Bos
float-array `SETTINGS`: no readable twin, no public schema. Keep faithful
`base64`; configure these via preset recall + a few macro CCs. Per-plugin
reverse engineering is possible but high-effort, version-brittle, and low-ROI
relative to the recall/CC path — out of scope unless one specific plugin
justifies a bespoke effort.

## Implementation status

Done (this slice, fully tested locally — no iPad needed):

- `config.AUv3DefaultStatesDir()` (rig-as-code under the config dir).
- `device.AUv3DefaultState` / `StateEntry` with `StateDoc()` + validation, and
  the **Tier-0 classifier** `device.ClassifyStateDoc` (text / text+prefix /
  base64, every result verified to round-trip).
- `aum.Session.NodeAuStateDoc(channel, slot)` — the read counterpart of
  `SetAuStateDoc` (productionized `harvestRealNodeBlobs`).
- MCP tools `capture_auv3_default_state` (harvest + classify + write YAML),
  `list_auv3_default_states`, `get_auv3_default_state`.
- **Auto-apply on author:** `loadAUv3DefaultStates` / `findDefaultStateForComponent`
  / `applyDefaultState` (in `auv3_default_state_tools.go`), wired into both
  probe→`NodeSpec` author sites — `nodeArg.resolve()` and `loopNodeSpec()`. A
  captured default for an audio unit is merged into every node of that unit on
  author, with precedence **per-call `state` arg > audio-unit default >
  identity-only** (per-call keys win, the default fills the rest; a no-match is
  a no-op; a matching-but-broken default fails the author loudly). Plus the
  write-side CRUD tools `set_auv3_default_state` (hand-author/edit a default,
  with `merge`) and `delete_auv3_default_state`, completing the
  capture→get→edit→set→apply loop.

Verified against the real rig: a PatternBud node captures as readable `text`
JSON; KORG/Audio-Damage binary `data` stays `base64`. Auto-apply, precedence,
and CRUD are covered by `internal/mcpserver/auv3_default_state_tools_test.go`
(including an end-to-end author→re-open→assert-state check).

- **Tier 1 — field-level editing:** `set_auv3_default_state_field`
  (`internal/mcpserver/auv3_default_state_field.go`) edits one path inside a
  structured `text` entry, auto-detecting JSON (`tidwall/sjson`+`gjson`) vs XML
  (`beevik/etree`), preserving every other field and the binary `prefix`. JSON
  dot paths and an XML element/attribute path grammar (`Name[i]`,
  `Name[@attr=val]`, trailing `@attr`); `delete` supported for both. Opaque
  base64 entries are rejected. Covered by
  `internal/mcpserver/auv3_default_state_field_test.go`.

Next slices:

- **Tier 2** `archive` form for `bplist` leaves via the `internal/aum` codec
  (the package-layering note in the handoff still applies: the `archive` form
  lives in `aum`/`mcpserver`, not `device`).

## Round-trip safety

- A captured entry that is *not* edited must re-encode to byte-identical wire
  bytes (assert this on capture for `xml`/`json`/`archive`; fall back to
  `base64` if it does not round-trip).
- An edited entry is only trusted for plugins whose authoritative-key question
  (above) has been answered on-device.

## Privacy

Captured state is a vendor + rig artifact: it stays under the gitignored state
dir / config dir per the public-vs-private rule, never committed. Vendor
*format* names (jucePluginState, FFBS, ISEMPatch) are public and already
documented in [docs/research/aum-session.md](research/aum-session.md); concrete
captured *contents* are not.
