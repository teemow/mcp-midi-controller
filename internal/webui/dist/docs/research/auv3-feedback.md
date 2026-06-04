# AUv3 feedback — verifying plugin definitions hosted in AUM

Research/design note on **how to get feedback from AUv3 instruments/effects
hosted in AUM**, so the per-plugin device definitions (`isem.yaml`,
`ims20.yaml`, `agonizer.yaml`, `fabfilter-pro-q.yaml`, …) can be verified
**correct** and proven to cover the plugin's **maximum controllable
functionality** — the AUv3 counterpart to the BLE echo (`verify_control` /
`probe_feedback`) and the USB readback (`docs/usb-tools.md`).

This is a *design* note. It records the constraints (two of which are confirmed
from Apple's API and AUM's documented behaviour) and the options that follow
from them.

> **Status: option 1 is implemented and verified live.** The `auv3-probe` iPad
> app exists at [github.com/teemow/auv3-probe](https://github.com/teemow/auv3-probe)
> and has been run on-device: it enumerates installed AUv3s, dumps each
> `AUParameterTree`, and POSTs the JSON to the daemon's **built-in probe
> receiver** (`internal/auv3receiver`, a LAN listener separate from the
> loopback MCP endpoint; config `auv3_receiver_addr`, default `:7800`), which
> stages one `<ProbeID>.json` per plugin under the state dir. Agents read what's
> available and configurable via `list_auv3_probes` / `get_auv3_probe` and
> scaffold/verify definitions via `import_auv3_probe`. (`cmd/auv3-probe` still
> ships the same receiver standalone, for running it apart from the daemon.)
> The app can be built/signed/installed from Linux (no Mac) via xtool — see the
> app repo's `docs/building-on-linux.md`.

## What "feedback" means in the existing transports

The project already has two feedback loops, both resting on the device being
**readable**:

- **BLE — in-band echo.** `internal/engine/feedback.go`: `verify_control` /
  `probe_feedback` send a CC and listen on the inbound notify channel for the
  device's CC-OUT/PC-OUT echo; MIDI-learn reuses the same channel. Works only
  because the pedal talks back.
- **USB — request/reply readback.** `docs/usb-tools.md`: editor protocols
  (RQ1→DT1, Neuro `0x36`, Morningstar TLV) read structured device memory.
  Reads are first-class — the explicit purpose is to *verify what actually
  landed* and dump full patches, closing the open-loop gap the BLE echo leaves.

## The two facts that decide everything for AUv3

1. **AUM does not echo / does not generate MIDI on its own.** AUM's
   "MIDI Control" is **input-only** — it listens for CC/NOTE/PC/PBEND/CHPRS to
   *drive* parameters but does not emit MIDI when a parameter changes (on-screen
   or otherwise). "AUM doesn't generate MIDI on its own, so it has nothing to
   send." The only exception is an individual AUv3 that itself has a MIDI-out
   (Turnado-style) — **none of the v1 targets** (iSEM, iMS-20, Agonizer,
   Pro-Q) do. **Consequence: the BLE-echo path is structurally unavailable** for
   AUM-hosted plugins; `verify_control` will always return `no_feedback` here.
   (Source: AUM help "MIDI Control"; community-confirmed on the Loopy Pro /
   BeepStreet forums — bidirectional feedback requires a scripting middleman
   like Mozaic/StreamByter, AUM has no native parameter→MIDI feedback.)

2. **The real truth-source is the AU parameter tree.** Every AUv3 exposes an
   `AUParameterTree` (`AudioToolbox`); each `AUParameter` carries:

   | Field | Use for us |
   |-------|------------|
   | `address` (`AUParameterAddress`) | the wire handle AUM maps a CC onto |
   | `keyPath` | **stable** identifier (address may change if the tree is rearranged) |
   | `identifier` / `displayName` | match to our control `name` / description |
   | `minValue` / `maxValue` | correct `value: range` bounds |
   | `unit` / `unitName` | human units (Hz, dB, %) |
   | `valueStrings` | enum labels for indexed params |
   | flags (writable / readable / `ValuesHaveStrings`) | **which params are controllable / AUM-mappable at all** |
   | `value` | current value (readback) |

   This is the AUv3 analog of a USB patch dump. The catch: the tree lives
   **inside the host process on the iPad**, and AUM does not expose it over the
   network. (Source: Apple `AUParameterTree` / `AUParameters.h`; the tree is
   KVO-compliant and hosts are told to persist `keyPath`, not `address`.)

### The sandbox constraint (why "live verify of AUM's instance" is the hard part)

An AUv3 is sandboxed and **cannot see sibling plugins' trees**, and a separate
host app loads its **own** instance, not the one AUM is running. So reading back
the live value of *the exact instance AUM is playing* is not possible without
AUM's cooperation (which it doesn't offer).

**But** — definition **correctness** and **maximum-functionality coverage** do
**not** need AUM's instance: any instance of the same plugin has the same
parameter tree. That is what makes option 1 below sufficient for the stated
goal, even though it cannot read AUM's live instance.

## Options (cheapest / most-aligned first)

### 1. `auv3-probe` companion (iPad) — the direct analog of `widi-probe`/`usb-probe` — RECOMMENDED

A small iOS app (any app may host installed AUv3s via
`AVAudioUnitComponentManager.components(...)` +
`AUAudioUnit.instantiate(with:)`) that, for each target plugin:

1. instantiates it,
2. walks `auAudioUnit.parameterTree.allParameters`,
3. emits JSON: `address`, `keyPath`, `identifier`, `displayName`, `min`, `max`,
   `unit`, `unitName`, `valueStrings`, flags, current `value`,
4. ships it to the daemon — simplest first: write a JSON file synced off-device,
   or one-shot HTTP POST to the daemon's loopback when on the same LAN; a
   BLE-MIDI SysEx channel is possible but heavier.

> **As built:** the app ([github.com/teemow/auv3-probe](https://github.com/teemow/auv3-probe))
> POSTs each dump to the daemon's built-in probe receiver (binds the LAN on
> `:7800`; the daemon's own MCP endpoint is loopback-only so the iPad cannot
> reach it directly). When a dump lands the receiver notifies connected MCP
> clients (`auv3-probe` logger) so an agent sees new plugin data arrive. One
> gotcha worth recording: enumerating **third-party** AUv3s requires the host
> app to carry the **Inter-App Audio** entitlement — without it
> `AVAudioUnitComponentManager` returns only Apple's built-in Audio Units.
> (Works with a free Apple ID.)
>
> **Robustness pass (2026-06):** probing *all* installed plugins surfaced two
> spurious error classes that are now handled. (1) AU params commonly report
> non-finite `min`/`max`/`value` (`±Inf`/`NaN`), which neither JSON nor Go's
> `encoding/json` can encode — the app now clamps them to finite sentinels and
> records the fact in a `nonFinite` field. (2) Plugins with an empty (or absent)
> parameter tree are now **accepted and staged** (params=0 is valid diagnostic
> data), not rejected with a 400. On top of the per-plugin dump the app POSTs a
> per-run **diagnostics report** to `POST /auv3-probe/diagnostics` capturing
> every outcome — including plugins that *fail to instantiate* and so never
> produce a dump — which the receiver stores under `_diagnostics/<ts>.json`. The
> dump itself was also enriched (human manufacturer name + version, factory
> presets, short name, and per-param parameter-group, flag bitfield, and decoded
> flags such as `displayLogarithmic`/`isHighResolution`). A later pass added more
> AU metadata: unit-level `channelCapabilities` (mono/stereo/multi-out),
> `latency`/`tailTime` (seconds), `supportsUserPresets`, factory **and user**
> presets (`factoryPresets`/`userPresets`, name + number), and per-param
> `dependentParameters` — the addresses a meta/macro control drives, so the
> authoring side knows not to also map those derived params independently.
>
> **Why presets matter for scenes.** Both factory and user presets are
> recallable by **Program Change** through AUM (PC → the plugin node's preset),
> so their `number` is the handle an MCP scene uses to recall a named preset.
> Capturing `userPresets` is therefore first-class scene material, not just
> diagnostics: an agent can author "recall *my Lead patch*" by number. User
> preset **names are installation-specific**, so a dump carrying them is only
> ever staged in the gitignored state dir (`auv3-probes/`) and any derived
> per-plugin definition lives in the user config dir — never committed. We do
> **not** dump `fullState`: it is an opaque, plugin-defined blob applied via the
> AU host API, not reachable over the MIDI/AUM control path, so it is not
> actionable for scenes (presets, recalled by PC, are the right granularity).

This **fully answers "are the definitions correct and do we cover the maximum
functionality?"**:

- full parameter list → we know we modeled the maximum, not a guessed subset;
- ranges / units / `valueStrings` → real `value` specs instead of a blanket
  `range 0-127`;
- writable/automatable flags + `keyPath` → exactly which params AUM can map, so
  the **AUM mapping cheat-sheet** (see `docs/research/aum.md`) becomes precise
  rather than aspirational.

Like `widi-probe` / `usb-probe` it is a **utility spike, not part of the shipped
daemon**. Its output is what turns the "convention we invented" tables in
`docs/research/auv3-plugins.md` into **measured** tables, and can feed the
`create_device_definition` / `add_control` authoring tools (generate a YAML
skeleton from the dumped tree; a human curates names).

**Proposed dump shape** (one file per plugin, consumed by the authoring tools):

```json
{
  "component": { "type": "aumu", "subtype": "iSEM", "manufacturer": "Artu" },
  "name": "Arturia iSEM",
  "parameters": [
    {
      "address": 0,
      "keyPath": "cutoff",
      "identifier": "cutoff",
      "displayName": "Cutoff",
      "min": 0.0, "max": 1.0,
      "unit": "generic", "unitName": null,
      "valueStrings": null,
      "writable": true, "readable": true,
      "value": 0.5
    }
  ]
}
```

The authoring side maps each AU parameter to a YAML control: pick the convention
CC, carry `displayName`→description, `min/max/unit`→`value` spec,
`valueStrings`→`enum`. `keyPath` is recorded so a future re-probe can detect
when a plugin update reshuffles its tree.

### 2. Parse AUM session / `.aupreset` / MIDI-mapping files (offline, no iOS code)

AUM stores its **MIDI mappings** and each node's plugin **`fullState`** in the
saved session bundle (and `.aupreset` files are plists carrying `fullState` /
parameter values). A `cmd/aum-probe` (or a daemon tool) reading an *exported*
session can:

- **Diff AUM's actual CC→parameter mapping against the YAML convention** — the
  most direct "is the definition correct / is AUM wired to match?" check, with
  **no live loop**;
- read plugin `fullState` plists for **offline "what landed" readback** after a
  Save (the patch-dump analog). Caveat: `fullState` is **plugin-defined and
  often opaque**, so decoding is per-plugin and best-effort.

Needs a file hop (Files / iCloud / SMB) but zero runtime coupling. Complements
option 1: option 1 says "here is the full tree," option 2 says "here is how AUM
is actually wired right now."

### 3. Document live control as open-loop (the SL-2-over-BLE posture)

Because AUM won't echo, treat live plugin control as **open-loop**: write
absolute CC values, no per-recall readback — exactly the stance
`docs/usb-tools.md` already takes for the SL-2 over BLE (TRS MIDI IN only, no
readback). Verification moves to **authoring time** (options 1/2), not every
scene recall. This is primarily a `docs/design.md` note so the open-loop is
intentional rather than a silent gap.

### 4. (Optional, coarse) Audio-domain smoke test

Record an AUM output bus while the daemon sweeps a CC and check the audio
actually changed (spectral centroid for `cutoff`, RMS for `volume`, …).
Confirms "the mapping is live and affects the right *kind* of thing," not exact
values — a `probe_feedback`-style capability matrix rather than precise verify.
Likely past v1.

### 5. North star — a daemon-driven scriptable AUv3 host

True bidirectional verify is only possible if **we are the host**: a companion
iOS app that hosts the plugins itself and exposes set-by-address + read-back +
enumerate to the daemon over OSC/WebSocket — effectively a scriptable mini-AUM.
Large effort, real payoff (exact verify, no AUM-mapping guesswork), explicitly
**not v1**. Recorded here as the long-term target.

### Not worth it: middleware feedback (Mozaic / StreamByter)

A scripting middleman can mirror CCs back to a controller, but it only
**state-tracks the CC values we already sent** — it never reads the plugin's
actual parameter. No advantage over the (absent) BLE echo for our purposes.

## Recommendation

- Build **option 1 (`auv3-probe`)** as the primary item — it is the clean analog
  to `widi-probe` / `usb-probe` and, because enumeration is instance-independent,
  it alone closes the "correct definitions + maximum functionality" question.
- Add **option 2** as the complementary offline check that the AUM wiring matches
  the convention.
- Document **option 3** as the deliberate control posture in `docs/design.md`.
- Keep **option 5** on the roadmap.

## Implications for the project

- **No daemon transport change for option 1/2.** The probe and session parser
  are off-daemon utilities; their product is verified facts + generated YAML, not
  a new runtime data path.
- **Authoring tools gain a real source.** `create_device_definition` /
  `add_control` can ingest an `auv3-probe` dump to scaffold a definition with
  correct ranges/units/enums, instead of starting from a hand-guessed CC block.
- **`verify_control` honesty.** For blemidi-via-AUM bindings, the echo will be
  `no_feedback` by design; the AUv3 "verify" is the build-time tree/mapping check,
  not a live recall echo. Worth surfacing so the result isn't read as a failure.

## Sources

- AUM help / manual (MIDI Control, Transport, routing):
  <https://kymatica.com/aum/help>
- Apple `AUParameterTree` — <https://developer.apple.com/documentation/audiotoolbox/auparametertree>
  and `AUParameters.h` (address / keyPath / displayName / min/max / unit /
  valueStrings / flags; KVO; persist keyPath not address).
- AUv3 dynamic parameter tree (host guidance, keyPath stability) —
  <https://nikolozi.com/mela/tech-notes/parameter-tree/>
- AUv3 parameter setup (flags, valueStrings, addressing) —
  <https://audiokitpro.com/auv3-midi-tutorial-part2/>
- AUM has no native parameter→MIDI feedback (requires Mozaic/StreamByter
  middleman) — Loopy Pro forum "AUM MIDI OUT / MIDI SEND TO CONTROLLER" and
  BeepStreet "Bidirectional Feedback for MIDI controllers".
- Project context: `docs/design.md` ("AUv3 plugins & AUM"),
  `docs/research/aum.md`, `docs/research/auv3-plugins.md`, `docs/usb-tools.md`,
  `internal/engine/feedback.go`.
