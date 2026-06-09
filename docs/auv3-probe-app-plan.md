# auv3-probe app plan — capture the on-device-only artifacts the session author needs

Plan for the **auv3-probe iPad app** (separate repo `github.com/teemow/auv3-probe`).
The shared wire contract (`device.ProbeDump`) lives in *this* repo
(`internal/device/auv3probe.go`), so this plan documents both sides of it; the
app changes are the iOS-side work, the Go changes are the contract + author side.

## Why

The from-scratch AUM session author (`internal/aum`, see
`.cursor/plans/aum-session-author_*.plan.md`) reproduces a `.aumproj` off-device
from a `ProbeDump`. Two pieces of a real `AUMNodeArchive` exist **only on the
iPad** and cannot be reconstructed off-device, so the author currently omits or
fakes them — the two biggest open risks to AUM actually loading an authored
session:

1. **`componentIcon`** — every real `AUXNodeDescription` carries an archived
   `UIImage` (the plugin's icon: `UIImageData` PNG + `UIImageSizeInPixels` +
   `UIImageConfiguration` + a shared trait collection). `buildAUXNode` writes
   none. Whether AUM tolerates its absence is the **#1 load-fatal unknown**.
2. **default AU state (`fullState`)** — a real node's `AuStateDoc` carries the
   plugin's saved `fullState` blob; an authored third-party node carries only the
   identity tuple (empty blob). Whether AUM instantiates a plugin from
   identity-only is **unverified**, and even if it does, the plugin loads with
   *some* default rather than a captured one.

The app already runs on-device, enumerates components, and walks the parameter
tree — so it is the right place to also capture the icon and the default state.
This plan adds those captures to the dump, byte-faithfully, so the author can
stamp real per-plugin artifacts instead of guessing.

## Resolve cheapest-first (do NOT build the app feature blind)

The icon work is only worth doing if AUM actually needs the icon. Order the
investigation so the app change is the *last* resort, not the first:

1. **Absent** — author a node with **no** `componentIcon` and open S1 in AUM
   (Go-side only; already the current output). If AUM loads and instantiates the
   plugin, **no app change is needed** — stop here.
2. **Shared placeholder** — if absence fails, author *one* small placeholder
   `UIImage` referenced by every node (Go-side only, no app change). If AUM
   accepts it, stop here.
3. **Probe-captured real icons** (this plan's app work) — only if (1) and (2)
   both fail does the app need to capture the real per-plugin icon.

The same gate applies to `fullState`: if identity-only nodes instantiate fine on
S1/S2, the default-state capture is a fidelity nice-to-have, not a blocker.

So this plan is the **contingency that guarantees byte-faithful nodes**, scoped
and ready to execute the moment the on-device S1 test says we need it.

## Goal

Extend the app's per-plugin probe so a dump optionally carries (a) the plugin's
icon as an archived `UIImage` and (b) the plugin's default `fullState`, and
extend the Go author to embed both into authored nodes — making an authored
third-party `AUXNodeDescription` indistinguishable (for load purposes) from one
AUM wrote itself.

## Wire contract (shared `device.ProbeDump`)

Two new **optional** fields (older dumps without them keep decoding; the receiver
re-marshals the whole dump, so no receiver change is needed beyond the struct):

```jsonc
{
  "component": { "type": "...", "subtype": "...", "manufacturer": "..." },
  "name": "...",
  "parameters": [ /* ... */ ],

  // NEW — base64 of NSKeyedArchiver.archivedData(withRootObject: uiImage).
  // The archived UIImage is exactly what AUM stores in componentIcon, so the
  // author can graft it verbatim. Omitted when the plugin has no icon.
  "componentIcon": "<base64 NSKeyedArchiver UIImage>",

  // NEW — base64 of the plugin's default fullState, captured right after
  // instantiation before any tweak. Maps to the AuStateDoc plugin blob.
  // Optional; omitted when unavailable or when the dump is identity-only.
  "defaultState": "<base64 NSKeyedArchiver-or-raw fullState>"
}
```

Choice of `componentIcon` encoding: the app sends the **already-archived
`UIImage` bytes** (it has UIKit; `NSKeyedArchiver.archivedData(withRootObject:)`
is one line) rather than raw PNG + metadata, because reconstructing a faithful
`UIImage` archive subgraph in Go is fragile. Raw PNG + `width/height/scale` is a
documented fallback only if in-app archiving proves problematic.

## App-side design (iOS, separate repo)

### 1. Determine the icon source (spike — do this first)

iOS does not expose AUv3 icons as uniformly as macOS, so confirm the API before
building on it. The app already holds each `AVAudioUnitComponent` (from
`AVAudioUnitComponentManager.shared().components(matching:)`). Candidate sources,
in order of preference:

- `AVAudioUnitComponent.icon` if the deployment target exposes a `UIImage` icon.
- The component's `iconURL` / containing-app (host extension) icon loaded as a
  `UIImage`.
- A rendered generic fallback (so a plugin with no discoverable icon still gets
  *an* icon if AUM requires one).

**Validation anchor:** we already have real AUM sessions whose `componentIcon`
is a known plugin's real icon. For a plugin we have both for, capture via the app
and compare the archived bytes / rendered image to AUM's stored one to confirm
the source is right.

### 2. Capture + serialize the icon

- After resolving the `UIImage` for a component, archive it:
  `NSKeyedArchiver.archivedData(withRootObject: image, requiringSecureCoding: false)`
  (match the encoding AUM uses — verify `requiringSecureCoding` against the real
  stored object's class/flags during the spike).
- Base64-encode and attach as `componentIcon` on the dump JSON. Skip the field
  entirely when no icon is available (do not send an empty string).

### 3. Capture the default `fullState` (secondary)

- Right after instantiating the `AUAudioUnit` for the dump (before reading the
  parameter tree mutates nothing, so order is fine), read `auAudioUnit.fullState`
  (or `fullStateForDocument`), archive/serialize it, base64-encode, attach as
  `defaultState`.
- Gate behind a flag/opt-in: `fullState` can be large and some plugins embed all
  their presets; keep dumps lean by default and capture it only when asked.

### 4. POST unchanged

The dump still POSTs to `POST /auv3-probe`; only the JSON body grows. The
existing 8 MiB body cap is generous for an icon + state (bump only if a real
plugin's `fullState` exceeds it; surface that in diagnostics).

## Go-side design (this repo)

1. **`device.ProbeDump`**: add `ComponentIcon []byte` and `DefaultState []byte`
   (json `componentIcon` / `defaultState`, base64 via `[]byte`'s default JSON
   encoding). Optional; absent in old dumps. `NodeSpecFromDump` carries them onto
   the `NodeSpec`.
2. **`aum.NodeSpec`**: add `ComponentIcon []byte` (the archived UIImage bytes)
   and reuse the existing `StateDoc`/`AuStateDoc` path for `defaultState` (stamp
   the captured blob as the plugin-state entry of `AuStateDoc` when present).
3. **`buildAUXNode`** (`internal/aum/nodes.go`): when `n.ComponentIcon` is set,
   `Decode` it as a standalone archive and `Builder.Graft` its root `UIImage`
   object into the session, setting the node's `componentIcon` field — the same
   graft primitive `AddProbeRig` already uses to splice template nodes (handles
   shared class defs / trait collection / UID rewriting for free). Fall back to a
   shared placeholder (or omit) when absent.
4. **Round-trip**: the existing `Open(Encode(...))` re-decode gate and
   `GraphEqual` round-trip must still pass with a grafted icon present.

## Validation

> **Status 2026-06-08 — the `validate` gate is MET without icons, which likely
> makes this whole feature moot (see the "May be unnecessary" risk below).** A
> from-scratch authored session with **no `componentIcon` and no captured
> `defaultState`** was confirmed on a real iPad (iOS 26.5, AUM 1.4.8) to **load,
> instantiate its hosted AUv3 node, and produce audio to master**. Reaching this
> required fixing three *load* crashes (inline `audioComponentDescription`,
> always-present `midiMatrixState`, `notes` as the `$null` ref) and one *render*
> crash (an audio channel head must have an audio source — instrument/generator
> or a HW-input/bus/file-player source; now guarded in `BuildSession`). All four
> were diagnosed from on-device crash logs (`idevicecrashreport`); see
> `docs/research/aum-session.md` → "Authoring from scratch: what crashes AUM".
> The icon was never needed for the load/render gate; only proceed with the
> capture work below if a *visual* requirement (not loadability) justifies it.

1. **Bytes match the host (off-device):** for a plugin present in a real AUM
   session, capture its icon via the app, author a node with it, and confirm the
   authored `componentIcon` graph matches (or is accepted in place of) the real
   one.
2. **On-device S1:** stage S1 with probe-captured icons + default state, open in
   AUM, confirm load + instantiation + audio to master (the gate from the
   session-author plan's `validate` todo). This is the only proof of "AUM needs
   the icon and ours works."
3. **Regression:** older icon-less dumps must still author and round-trip
   (the new fields are optional).

## Work items

Status as of 2026-06-08 (verified against code in both repos + the running
daemon's staged probes/sessions):

- [x] **app/spike:** icon source determined — `AudioComponentCopyIcon(comp)`
  (iOS 14+, non-deprecated; `AVAudioUnitComponent.icon`/`.iconURL` are macOS-only).
  In `auv3-probe` `Sources/AUv3ProbeApp/ComponentIcon.swift`.
- [x] **contract:** `componentIcon` added to `device.ProbeDump`
  (`ComponentIcon []byte`, json `componentIcon`) and carried through
  `NodeSpecFromDump`. `defaultState` was **not** added (see remaining item).
- [x] **app:** captures the icon at discovery and NSKeyedArchiver-archives it,
  attaching it to the dump (`AudioUnitScanner.swift` → `AudioUnitDetails.componentIcon`),
  skipped when no icon.
- [ ] **app:** (secondary, opt-in) capture default `fullState` as `defaultState`
  — **not implemented** (the only remaining item; optional, see status below).
- [x] **author:** `aum.NodeSpec.ComponentIcon` + `buildAUXNode` Decode+Graft via
  `graftComponentIcon` (`internal/aum/nodes.go`), with tests
  (`TestBuildAUXNodeComponentIcon`, `TestGraftComponentIconFallback`). The
  `defaultState`→`AuStateDoc` stamp is **not** wired (the `StateDoc` loop in
  `buildAuStateDoc` is the hook; only our own plugins use it today).
- [x] **validate:** load/render gate **MET without icons** on real hardware (see
  status block above). Icon graft round-trips; icon-less regression holds.

> **Outcome:** the load/render blocker is fixed and the icon path is fully wired
> end-to-end (even though it proved unnecessary for loading). The single
> outstanding item — capturing the default `fullState` — is an optional fidelity
> nice-to-have, not a blocker. Identity-only nodes instantiate and play.

## Parameters: verified complete — no icon-style capture needed (2026-06-08)

A separate question came up: do we have everything about a plugin's **parameters**
via the probe, or is there an icon-style off-device gap? Verified against the
running daemon's staged probes + the five real rig sessions:

- The probe walks the full `AUParameterTree` and emits, per param, `address`,
  `keyPath`, `identifier`, `displayName`, range/unit/`valueStrings`, `flags`
  (logarithmic/high-res/meta/rampable), `dependentParameters`, plus presets,
  channel caps, latency. Nothing about a param's definition lives only on-device.
- **The mapping-leaf key AUM stores is present and exact.** AUM keys a node
  param at `midiCtrlState/Channels/chan<N>/slot<S>/<KEY>`. Ground truth from the
  corpus: `fast_forward` has two *assigned* leaves `…/slot0/VCF Freq → CC74` on
  Arturia iSEM nodes, and `captureprobe` mapped `…/slot0/Master Vol`. Both keys
  match the iSEM probe's captured name string **byte-for-byte** — no truncation,
  no `displayName(withLength:)` abbreviation, nothing missing.
- **Narrow residual (not a missing-data problem).** For iSEM
  `keyPath == identifier == displayName` for every param, and every assigned
  node-param mapping in the whole corpus happens to be on iSEM — so the corpus
  cannot prove **which** field AUM keys by when the three diverge. The author's
  `paramTarget` (`internal/aum/build.go`) prefers `identifier → displayName →
  keyPath`; correct for iSEM and any plugin where they agree. Settling the
  divergent case needs one real assigned param mapping on a plugin whose three
  names differ (e.g. a FabFilter node) — **zero probe changes required**, since
  the probe already carries all three candidates.

## Risks / open items

- **iOS icon API uncertainty:** ~~the icon source is not a single guaranteed
  public API on iOS~~ — **RESOLVED:** `AudioComponentCopyIcon(comp)` (iOS 14+).
- **Archive encoding parity:** the app's `NSKeyedArchiver` output must match what
  AUM's decoder expects (secure-coding flag, class names). The graft path decodes
  it as a standalone archive and degrades gracefully on a bad blob, but a stored
  icon was never round-tripped through AUM on-device (the load gate passed without
  one), so byte-parity is still unverified against a real AUM-stored icon.
- **May be unnecessary — CONFIRMED:** S1 loads, instantiates and plays with **no**
  icon, so the whole icon feature is moot for loadability. Only proceed past the
  built (but unproven-in-AUM) icon graft if a *visual* requirement justifies it.
- **`fullState` size/privacy:** can be large and vendor-internal; keep it opt-in,
  honor the public-vs-private rule (stays in the gitignored state dir, never
  committed), and watch the receiver body cap.
- **Privacy:** plugin icons/state are vendor artifacts, not user data, but the
  dump posture is unchanged — staged only under the state dir, never committed.
