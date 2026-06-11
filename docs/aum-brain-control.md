# The brain controls AUM — sessions + a standard mapping (the vision)

The big picture behind the AUM work, and why it is more powerful than it looks
today. This ties together three things that already exist in pieces — the AUM
**session** model (`internal/aum`), the AUM **MIDI-control convention**
(`docs/research/aum.md`), and the in-host **brain** (`ProbeMidiBrain`) — into one
thesis, and names what is still missing.

## The measured fact

The `ProbeMidiBrain` AUv3, hosted **inside** AUM, can drive essentially AUM's
**entire** control surface over MIDI: transport (start/stop/record), **tempo**,
channel **volume / mute / solo / rec**, **any parameter of any node**, node
bypass, plugin **preset load**, and **session load**. This is measured, not
assumed — see the auv3-probe spec,
[aum-control-surface.md](https://github.com/teemow/auv3-probe/blob/main/docs/aum-control-surface.md)
(tempo swept 20–500 BPM, channel mute toggled, a FabFilter Pro-L2 output level
swept, all via CCs the brain emitted into AUM's MIDI Control).

So the ceiling is high: from inside the host, the brain is a near-complete remote
for AUM.

## The catch: control is only as good as the session

AUM ships **no fixed CC map** (`docs/research/aum.md`). A function responds to a
MIDI message **only if the session wires that message to it** in AUM's MIDI
Control matrix. The brain can flip a mute, set a tempo, or load a session **only
where the loaded `.aumproj` has a mapping for it**. Nothing is reachable by
default.

That single constraint is the whole game. It means the brain's power as an AUM
controller reduces to **two questions about the session**:

1. **Do we understand the session well enough** to know what is in it and what is
   reachable?
2. **Does the session carry a standard, predictable mapping** so the brain has a
   known control surface — including the ability to **change scenes**?

Everything below is those two pillars.

## Pillar 1 — deep session understanding

The daemon already reads, edits, and authors `.aumproj` files (the Go
`internal/aum` library; format in `docs/research/aum-session.md` and
`docs/research/aum-midi-matrix.md`), and exposes the flat layout +
every mapping via `get_aum_session`. That is the foundation: to reason about what
the brain can do in a session, we must know its channels, nodes, parameters, and
**which CC→target mappings exist on which channels**.

What "deep enough" still requires:

- **Channel encoding is 0-indexed.** A mapping stored as `ch=N` in the `.aumproj`
  fires on **MIDI channel N+1** (verified: chan1 Volume `ch=0` → MIDI ch1; Start
  `ch=15` → MIDI ch16). Any tool that authors mappings or dispatches to them must
  apply this `+1`, or the CC silently hits nothing. (This bit us once already —
  scene CCs authored as "ch1" were stored `ch=1` = MIDI ch2.)
- **Global actions are invisible to the file model.** AUM stores **Session Load**
  and some **Tempo Preset** actions **globally**, not inside the `.aumproj`, so
  they do **not** appear in `get_aum_session`. The daemon therefore cannot
  *author* the cross-session lever — it owns it a different way: the
  **session-switch registry** (see below) pins each staged session to a Program
  Change on the reserved channel 16, and the user hand-maps AUM's global
  "Session Load" action to that PC once via Learn.
- **Message-type gaps — mostly closed.** The version-13 `specState` `type` enum
  is now mapped (`docs/research/aum-session.md`): 0 = CC, 1 = Note, **2 =
  Program Change**, **3 = Pitch Bend / Channel Pressure** (split by `data1`).
  PC-driven actions (preset recall) are authorable and importable; PBEND/CHPRS
  remain readable-but-inexpressible — the device model has no such control type,
  so `DeriveRig` reports them in `Rig.Skipped` instead of dropping them. Only
  the *packed*-`spec` codes for these types stay unconfirmed.
- **No live graph read.** The host API gives the brain transport/tempo/beat but
  **no** read of the session graph or MIDI matrix; structured understanding comes
  only from parsing the file off-device. The deeper the off-device model, the more
  the daemon can author, verify, and reason on the brain's behalf.

## Pillar 2 — a standard mapping (so the brain can change scenes)

`docs/research/aum.md` drafts a **convention CC map** (an interleaved
mixer block, a transport/system block, and Session/Preset Load on Program
Change), and the authoring tools now **bake it in by default** — every session
the daemon authors carries the same baseline mapping, and
`instrument_aum_session` goes further: it banks **every** mappable target
(node params, triggers, preset PCs, tap toggles) collision-free across
channels, so the brain has a complete, self-describing control surface no
matter which session is loaded. Note the rig is no longer *guessed from* the
convention: `DeriveRig` reads the mappings back out of the session file, so
even a hand-wired session is fully addressable.

With a standard mapping in place, "what can the brain do here?" stops being a
per-session unknown and becomes a contract. The headline capability that unlocks
is **scene changes**, at two levels:

- **Within a session** — recall mute/level/param state via the standard CCs. This
  is the existing scene engine (`save_scene` / `recall_scene`), now executable
  **by the brain** because the session maps those CCs. The brain (driven by the
  daemon, or autonomously from its embedded program, or from a footswitch) can
  replay a scene live with no laptop in the path.
- **Across sessions** — **Session Load** lets the brain jump between whole
  `.aumproj` files. This is the most powerful lever and the least understood (the
  global-action gap above) — once solved, the brain can move the rig between
  entire setups on a footswitch press.

This is the same "convention model" the project already uses for plugins
(`docs/design.md` → "AUv3 plugins & AUM"), generalized: the YAML/convention is the
source of truth, AUM is configured to match, and now the **session author** bakes
the convention in so the match is automatic rather than hand-wired.

## The synthesis — why this is powerful

Put the two pillars together:

```
deep session model  +  standard mapping authored into every session
        │                          │
        ▼                          ▼
  the daemon KNOWS          the brain CAN REACH
  what's in the session     every function predictably
        └──────────┬───────────────┘
                   ▼
   the brain becomes a universal, self-describing AUM
   control surface — scenes (in- and cross-session) execute
   live, on-device, with no laptop in the path
```

The daemon authors the mapping and holds the model; the brain executes. That is
the bridge from the current author→play→hear→tweak agent loop
(`docs/research/agent-loop.md`) to a **footswitch-driven, laptop-free scene
player** (the `export_scene_to_footswitch` direction in `docs/design.md`) and,
further out, the north-star scriptable on-device host.

## Where we are — just the beginning

| Piece | Status |
|---|---|
| Brain can reach AUM's full surface (measured) | **confirmed** (tempo / mute / node param swept) |
| Session read/edit/author (`internal/aum`) | **strong** — channels, nodes, params, CC/Note/PC mappings |
| Convention CC map (`aum.md`) | **done** — authoring tools bake it in by default; `instrument_aum_session` (and `full_control:true`) banks **every** mappable target collision-free across channels |
| Session-derived rig (`DeriveRig` → `import_aum_session`) | **done** — devices/controls are read back from the session's *enabled mappings* (type/number/channel pinned per control), probe dumps enrich names/enums; re-import replaces the previous session's devices |
| Automatic import + manifest push | **done** — on session download to the iPad and on brain connect (`aum_auto_import`, default on); broadcasts `aum-rig`, pushes the `controlSurface` frame |
| Brain control surface UI (on-device, offline-capable) | **done** — the brain caches the manifest in its AU `fullState` and renders faders/toggles/triggers/enums/presets that emit locally into AUM, no daemon round-trip |
| Scene engine driving the brain via the standard map | **not yet** — scenes exist; wiring them to a session-baked brain mapping is open |
| Cross-session **Session Load** by the brain | **done** — the session-switch registry pins PCs on channel 16; `switch_aum_session` and the brain's switcher row both fire them (one-time AUM Learn wiring per session) |
| PC authoring (preset recall) | **done** — `specState` type 2 mapped and importable |
| Pitch Bend / Channel Pressure | **read-only** — parsed (`specState` type 3) but no control type in the device model or brain protocol; `DeriveRig` reports them as skipped |
| Live brain-driven scene-change loop on-device | **not yet** — the next end-to-end goal |

### How a session becomes a control surface (implemented)

1. The agent authors/instruments a `.aumproj` and stages it; the iPad downloads
   it (the aum receiver's download callback fires) and AUM loads it.
2. The daemon tracks it as the **current session** (persisted in the state dir)
   and — with `aum_auto_import` on — runs the import: `DeriveRig` turns every
   enabled mapping into a control on one **session device** (strips, transport,
   system, built-in knobs, tap toggles) plus one **device per hosted AUv3 node**,
   each control pinning its stored message type / number / MIDI channel.
3. The import broadcasts an `aum-rig` notification and pushes the
   **`controlSurface` manifest** over the `/midi-control` WebSocket: per device,
   per control, a widget kind (`fader | toggle | trigger | enum | preset`) and
   the exact wire message. The brain caches it in its AU `fullState` (AUM
   persists it) and renders the surface in its plugin UI — taps emit into AUM
   locally, so the surface keeps working when the daemon is offline.
4. A brain (re)connect re-runs the import for the current session and re-pushes
   the manifest, so a brain that was offline during the download still gets it.

The same `DeriveRig` output drives the MCP `control_*` tools and the web UI, so
all three surfaces (agent, browser, iPad) emit identical MIDI.

### Cross-session switching (implemented)

AUM's "Session Load" is a **global** action — never stored in the `.aumproj` —
so the daemon cannot author it. Instead it owns a **session-switch registry**
(`aum-session-switch.json` in the state dir): each staged session is pinned to a
Program Change on the reserved session-switch channel
(`device.SessionSwitchChannel` = 16; PCs cannot collide with the tap CCs that
share the channel, and preset-load PCs stay on the binding channel 1..15).
Programs are **never renumbered** — the user's hand-wired AUM mappings depend
on them; removing an entry leaves a hole.

- **Registry tools:** `register_aum_session_switch {session, program?}` pins a
  session (next free program by default), `list_aum_session_switches` lists the
  registry with a per-entry Learn cheat-sheet ("AUM > MIDI Control > Session
  Load `<name>` ← PC `<program>` ch16") and marks the current session,
  `remove_aum_session_switch {session}` unpins.
- **One-time AUM wiring per session:** open MIDI Control → add "Session Load
  `<session>`" → arm Learn → call `switch_aum_session` for that entry to bind
  the PC. From then on the switch is a single PC.
- **Switching, daemon side:** `switch_aum_session {session|program}` sends the
  PC through the brain hub, sets the target as the daemon's current session,
  re-imports its rig and re-pushes the manifest, and broadcasts
  `aum-session-switch` — so the `control_*` tools, web UI and brain surface all
  target the new rig.
- **Switching, brain side:** the manifest's `sessions` section renders as a
  switcher row above the device rack (fullState-cached per session like the
  rest of the surface, so it works offline). A tap emits the PC locally into
  AUM **and** sends a `{"type":"sessionSwitch","program":N}` frame upstream;
  the daemon resolves it via the registry and follows (current session,
  re-import, re-push). With the daemon offline the local PC still switches the
  session, and the daemon re-syncs on the next connect.

### Testing process — iterate on copies, never on the originals

The author→sync→load loop above (staging mirror, auto-download, auto-import,
session switching) is **not proven end-to-end yet**. Until it is, the user's
original `.aumproj` sessions are treated as **read-only masters** — a bug in the
serializer, an instrumentation pass, or AUM saving over a half-broken session
must never cost a real session. The process is:

1. **Generate copies from the originals.** For every session selected for the
   load list, create a **byte-identical copy** next to the original in the
   staging tree, suffixed ` (mcp test)` (plain file copy — no round-trip
   through the serializer, so the copy is exactly the original). Regenerate a
   copy from its original whenever the original changes on the iPad (the
   mirror sync keeps the staged originals current) or a test copy needs a
   clean reset.
2. **Feature work and tests iterate over the copies only.**
   `instrument_aum_session`, `edit_aum_session`, imports, brain/manifest tests
   — all of it targets the ` (mcp test)` ids. Never write to an original id.
3. **The session-switch registry pins the copies.** The load list
   (`register_aum_session_switch`) points at the copy ids, so
   `switch_aum_session` and the brain's switcher row only ever load test
   copies. Note: the switcher button label comes from the session's *internal*
   title, which the byte-copy preserves — the button says the original name
   while loading the copy.
4. **The user loads the copies.** The automatic session sync is upload-only
   (iPad → daemon); staged copies reach the iPad via the auv3-probe app's
   per-entry **download** action (which writes each file back to its mirrored
   folder in the AUM tree). In AUM the user opens the ` (mcp test)` session
   (not the original) and wires each global "Session Load" action to the
   **copy file** via the registry's Learn cheat-sheet
   (`list_aum_session_switches`).
5. **Graduate when proven.** Once the whole workflow is trusted, apply the
   final instrumentation to the originals and re-pin the registry — the copies
   are then disposable.

The concrete rig-specific list (which sessions, copy ids, pinned programs) is
private: `docs/private/aum-projects.md`.

### Next steps (rough order)

1. **Wire the scene engine to the brain** — make `recall_scene` express scenes as
   the standard CCs the brain emits, so a scene is a brain action, not just a
   daemon action.
2. **Close the remaining session model gaps** — confirm the packed-`spec`
   codes for PC/PBEND/CHPRS.
3. **Prove the live loop** — brain (or footswitch) → standard mapping → scene /
   session change, verified on-device via the audio tap.

## References

- Measured AUM surface (auv3-probe):
  [aum-control-surface.md](https://github.com/teemow/auv3-probe/blob/main/docs/aum-control-surface.md)
- AUM MIDI-control convention + full CC map: `docs/research/aum.md`
- AUM session + MIDI-matrix formats: `docs/research/aum-session.md`,
  `docs/research/aum-midi-matrix.md`
- The agent loop (author→play→hear→tweak): `docs/research/agent-loop.md`
- Convention model + footswitch scene compile: `docs/design.md`
- Why audio is the feedback channel (no MIDI echo):
  `docs/research/auv3-feedback.md`
