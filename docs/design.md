# mcp-midi-controller — Design

An MCP server, written in Go, that acts as a **conversational rig-setup,
sound-design and scene-management** layer for a MIDI/OSC rig. It is **not** a
real-time/live controller — an LLM-driven MCP server has request/response
latency and cannot sit in the live foot-control path. The Morningstar ML10X
remains the live foot controller; this server is for *configuring* the rig,
recalling/building scenes, designing sounds, and documenting/extending the rig
conversationally.

## Goals

- Control a heterogeneous rig from a conversation: pedals, a digital mixer, an
  iPad host, and software plugins.
- Be **extendable without writing Go**: adding a device/synth is writing a YAML
  file (or having an agent author it via MIDI-learn).
- Own the messy parts (BLE discovery + pairing) so the user does not have to
  fiddle with `bluetoothctl`.
- Keep the rig **versionable as code** (one git-trackable config dir).

## Non-goals (v1)

- Live, low-latency performance control (expression sweeps, tap-tempo timing,
  per-beat switching).
- Cross-platform BLE (macOS/Windows). Linux-first; the transport interface
  leaves a clean seam for a CoreBluetooth/WinRT backend later.

## Target device classes (v1)

The device classes below are the v1 targets — a generic catalog of what the
server models, not a description of any one installation. The concrete inventory
of a particular rig (which units, which endpoints, which channels) is
installation-specific; a documented example is kept in `docs/private/`
(gitignored).

| Device | Class | Transport | Notes | Reference |
|--------|-------|-----------|-------|-----------|
| Boss MD-200 | pedal | BLE-MIDI | Pure CC. Full CC map known (see below). | — (CC map inlined below) |
| Boss SL-2 | pedal | BLE-MIDI (TRS) | 3 CCs only (CC16/80/81) + MIDI-clock sync. No PC; pattern/type not MIDI-addressable. | [SL-2 MIDI](https://static.roland.com/manuals/sl-2/eng/33861479.html) |
| Source Audio EQ2 | pedal | BLE-MIDI | 128 presets via PC; all params via CC (remappable default map). | [EQ2 manual](https://sourceaudio.net/products/eq2) |
| Eventide H90 | pedal | BLE-MIDI | Program change (presets) + CC + SysEx. | [H90 global/MIDI](https://cdn.eventideaudio.com/manuals/h90/1.7.1/content/appendix/global.html) |
| Two Notes Opus | pedal | BLE-MIDI | CC + program change. | [Opus MIDI chart](https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual#midi_chart) |
| Morningstar ML10X | controller/hub | BLE-MIDI | CC. Also the live foot controller. | [ML10X CC messages](https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages) |
| Behringer X32 | mixer | OSC/UDP | OSC (`/ch/01/mix/fader`), not MIDI. On WiFi. | [X32 MIDI table](https://behringer.world/wiki/doku.php?id=x32_midi_table) |
| AUM (iPad) | host | BLE-MIDI | Mixer/transport/routing via AUM MIDI control. | [AUM help](https://kymatica.com/aum/help) |
| AUv3 plugins/synths | software | BLE-MIDI (via AUM) | Battalion, iSem, Agonizer, iMS-20, FabFilter, … | see plugin list below |

### Boss MD-200 — default CC map

The MD-200 has no public manual link here; the default CC numbers are recorded
directly (also bundled in `internal/device/definitions/md-200.yaml`):

| Control | CC# | Control | CC# |
|---------|-----|---------|-----|
| Rate knob | 17 | On/Off switch | 28 |
| Depth knob | 18 | Memory/Tap switch | 82 |
| E. Level knob | 19 | Ctl-1 | 80 |
| Param1 knob | 20 | Ctl-2 | 81 |
| Param2 knob | 21 | Expression | 16 |
| Param3 knob | 22 | Effect On/Off | 27 |

### AUv3 plugins / synths (hosted in AUM)

Controlled via the AUM CC convention (see "AUv3 plugins & AUM" below). CC maps
are user-defined in AUM, not fixed by the vendor, so these links are product
references rather than MIDI specs:

- **Unfiltered Audio Battalion** — <https://www.unfilteredaudio.com/products/battalion>
- **Arturia iSEM** — <https://www.arturia.com/products/ios-instruments/isem/overview>
- **Kai Aras Agonizer** — <https://apps.apple.com/app/agonizer/id1583662383>
- **Korg iMS-20** — <https://www.korg.com/products/software/ims20/>
- **FabFilter plugins** — <https://www.fabfilter.com/products>
- …and other AUv3 instruments/effects (add a YAML definition per plugin).

A typical pedalboard hangs off a multi-port **BLE-MIDI hub** (e.g. a CME WIDI
Thru6): a *single* BLE endpoint fans out to all pedals on the DIN chain,
distinguished only by **MIDI channel**. An iPad is reachable via its own
BLE-MIDI dongle (another endpoint). Hence the core addressing unit is
**`(endpoint, channel)`**, not `endpoint` alone. The concrete endpoint names and
per-device channel assignments are **installation-specific** and live in the
user's config dir (and, for documentation, in `docs/private/` — gitignored), not
in this design doc.

## Architecture

```
                         ┌──────────────────────────────────────────┐
   MCP client            │            mcp-midi-controller daemon      │
 (Cursor / Claude) ──────┤  (systemd user unit, streamable-HTTP,     │
   HTTP 127.0.0.1        │   127.0.0.1 only)                          │
                         │                                            │
                         │  ┌──────────┐   generates   ┌───────────┐ │
                         │  │ mcpserver │◀─────────────▶│  engine   │ │
                         │  │ (go-sdk)  │  tools per     │ registry  │ │
                         │  └──────────┘  bound device   │ bindings  │ │
                         │                                │ state     │ │
                         │                                │ scenes    │ │
                         │                                └─────┬─────┘ │
                         │                                      │       │
                         │                       ┌──────────────┼─────┐ │
                         │                       ▼              ▼     ▼ │
                         │                  ┌────────┐   ┌────────┐ ┌────────┐
                         │                  │ blemidi │   │  osc   │ │ usbmidi│
                         │                  │(BlueZ   │   │ (X32)  │ │(gomidi)│
                         │                  │ D-Bus)  │   └────────┘ │ bonus  │
                         │                  └────┬────┘              └────────┘
                         └───────────────────────┼─────────────────────────────┘
                                                 ▼
                                     BLE-MIDI peripherals
                                 (BLE-MIDI hub → pedals, iPad)
```

The **engine is a library**; the daemon is a thin process on top. A stdio MCP
adapter can be added later without touching the engine.

### Transport interface

```go
type Transport interface {
    ID() string
    Discover(ctx) ([]Endpoint, error)
    Pair(ctx, endpointID) error           // blemidi owns this; osc/usbmidi no-op
    Connect(ctx, endpointID) error
    Disconnect(ctx, endpointID) error
    Send(ctx, endpointID, Event) error
    Listen(ctx, endpointID) (<-chan Event, error) // inbound: MIDI-learn / reconcile
}
```

Backends:

- **`blemidi`** — owns BLE **discovery + pairing + connection** via **BlueZ over
  D-Bus** (`Adapter1.StartDiscovery`, `Device1.Pair`, an `Agent1` for the PIN),
  then reads/writes/notifies the **BLE-MIDI GATT** characteristic directly.
  Service `03B80E5A-EDE8-4B33-A751-6CE34EC4C700`, characteristic
  `7772E5DB-3868-4112-A1A9-F2669D106BF3`. Implements the BLE-MIDI 13-bit
  timestamp framing. Linux-only (BlueZ); this is the deliberate Linux-first
  choice. We do **not** depend on the external BlueALSA daemon — owning the GATT
  keeps pairing and the data path in one place, and gives us the inbound notify
  channel that powers MIDI-learn.
- **`osc`** — UDP OSC to the X32 (`host:port`). No pairing.
- **`usbmidi`** *(bonus)* — `gitlab.com/gomidi/midi/v2` over ALSA (`rtmididrv`).

### Device definitions (the extension mechanism)

A device is described by a **declarative YAML definition** — no Go code. The
definition doubles as the **validation schema** for that device's generated MCP
tool. A `Control` has a semantic name, a wire `type`, an address, and a value
spec:

```yaml
id: md-200
name: Boss MD-200
manufacturer: Boss
transport: blemidi
settle_ms: 0          # delay after a program change before CC is accepted
controls:
  - name: rate
    description: Rate knob
    type: cc           # cc | program_change | nrpn | sysex | osc | note_on | note_off
    cc: 17
    value: { type: range, min: 0, max: 127 }
  - name: on_off
    type: cc
    cc: 28
    value: { type: enum, values: { off: 0, on: 127 } }
```

Value specs: `range` (min/max), `enum` (label→wire value, with human-units like
`dB` allowed via `unit`), `float`/`int`, and `string` (free text payloads such
as OSC scribble-strip names). The **channel is not in the
definition** — it is supplied by the *binding*, so one definition (e.g. H90) can
be bound on different channels.

Bundled definitions ship inside the binary via `go:embed` (source of truth:
`internal/device/definitions/*.yaml`). User definitions in
`$XDG_CONFIG_HOME/mcp-midi-controller/devices/*.yaml` **override bundled ones by
definition `id`** (not by filename — the loader keys the registry on the `id:`
field, so a user file with a bundled id replaces it whatever the file is named)
and add new ones.

#### `generic-midi` fallback

A built-in definition whose controls are **parametric** — the CC/NRPN/program
number is supplied at call time. Binding any unmodeled endpoint+channel to
`generic-midi` makes it controllable immediately (by raw number), while still
flowing through desired-state and scenes (unlike `send_raw`, which is untracked).

### Bindings & logical devices

```yaml
# bindings.yaml — illustrative only. Real per-rig bindings (actual endpoint
# names + channel assignments) live in the user's config dir and are not
# committed; see docs/private/ for a documented example.
- logical: h90        # logical device name -> generates tool control_h90
  endpoint: "<ble-midi-hub>"   # transport endpoint id (a BLE-MIDI hub)
  channel: 1
  device: h90         # definition id
- logical: md200
  endpoint: "<ble-midi-hub>"
  channel: 2
  device: md-200
```

A **binding** = `(endpoint, channel, definition)` → a named **logical device**.
Bindings persist so the daemon restores the rig on restart. Binding/unbinding
generates/removes the per-device tool at runtime and emits
`notifications/tools/list_changed`.

### MCP tools

Generated **per logical device** (`control_<logical>`): the tool's input schema
accepts a **batch** of `{control, value}`. Each batch item is a `oneOf` of
per-control objects derived from the YAML — `control` is pinned to a `const`
name and `value` carries that control's own value schema (integer range, enum
labels + wire ints, float bounds + unit, string, or the parametric
`{number, value}` shape) — so the model sees valid ranges/enums up front. The
**value** is still validated in-handler against the YAML value spec as the
authoritative safety net, returning `CallToolResult{IsError:true}` with an
RFC-6901 JSON-pointer path on failure (SEP-1303). Tool count = number of bound
devices + the globals below.

Global tools:

- `list_devices` — bound logical devices + their definitions.
- `describe_device` — controls, types, ranges/enums for one device.
- `list_bindings` / `list_definitions` / `get_definition` — machine-readable
  rig-reasoning views: the bindings (logical→device/endpoint/channel/transport),
  every loaded definition (bundled + user dir, bound or not), and one
  definition's full control detail. These plus `list_devices`, `describe_device`
  and `read_state` emit `structuredContent` (JSON) alongside the human text so
  the web client / agents get structured data, not just prose.
- `bind_device` / `unbind_device` — manage bindings (→ `list_changed`).
- `discover_endpoints` / `pair_endpoint` — BLE discovery + pairing.
- `save_scene` / `recall_scene` / `list_scenes`.
- `export_scene_to_footswitch` — compile a saved scene into a standalone
  BLE-MIDI footswitch's on-device JSON schema (program-change before CC, with
  per-device settle baked into the event order) and optionally HTTP-POST it to
  the device, so the footswitch can replay the scene live with no laptop.
- `send_raw` — raw MIDI bytes / OSC address escape hatch (untracked).
- WIDI dongle config: `widi_read_config` / `widi_write_setting` / `widi_set_group`
  / `widi_clear_group` — read/write a CME WIDI dongle's persistent flash settings
  (BLE role, TX power, MIDI-thru, wireless groups) via the `internal/widi` library
  over the BLE-MIDI characteristic. Addressed by endpoint + product (not a binding),
  request/reply, and deliberately **outside** the scene/desired-state path.
- Authoring: `create_device_definition` / `add_control` / `save_device_definition`.
- MIDI-learn: `learn_start` / `learn_capture` (reads the inbound notify channel
  to capture the CC/NRPN the user moves).

### State & scenes

- The server keeps an **authoritative desired-state**: per logical device, the
  last value sent per control. Updated on every control set and **persisted as
  JSON** under the state dir (`$XDG_STATE_HOME/mcp-midi-controller/desired-state.json`)
  so it survives a daemon restart; optionally reconciled from inbound MIDI
  (hand-tweaks on hardware).
- A **scene** is a named snapshot of **only the controls that have been set**
  (so scenes are small, partial, and **layerable**). For preset-based devices
  (H90/Opus) it stores the **program number plus CC overrides**.
- **Recall** replays **program-change before CC**, with a per-device
  `settle_ms` delay, and supports **additive** (apply over current state) and
  **exact** (reset to scene) modes.
- Scenes are human-readable files in `$XDG_CONFIG_HOME/mcp-midi-controller/scenes/`.
- **Compile + push to a footswitch (design-time):** because recall ordering and
  settle are resolved here, a scene can be *compiled* into a flat, already-ordered
  event list and pushed into a standalone BLE-MIDI footswitch (HTTP over WiFi),
  which then replays it live with no laptop in the path. The footswitch is a
  faithful player: it does not re-derive recall semantics. See
  `export_scene_to_footswitch`. Each event keeps its binding's MIDI channel, so
  the **routing host** the footswitch is connected to live (e.g. AUM on the iPad)
  must fan the replayed messages out to the gear by channel — the footswitch does
  not address the pedal hub directly. Verified end-to-end against real hardware
  (push → store → inbound-trigger → BLE replay → host relay → MIDI hub → pedal
  recalled its program). Note: this is also why a per-device **binding channel**
  must be correct (0-based wire channel); a wrong channel silently routes to the
  wrong pedal even though the push/replay path is fine.

### AUv3 plugins & AUM — the convention model

Hardware pedals have **fixed, manufacturer-assigned CC#s**. AUv3 plugins do
**not**: a parameter only responds to a CC if *you* map it inside AUM (AUM's MIDI
control matrix or the plugin's MIDI-learn). So for plugins the CC numbers are an
**arbitrary convention the server invents**, and AUM must be configured to match.

- The server's **YAML is the source of truth** for each plugin's
  `param → (channel, CC)` convention.
- The authoring tools emit an **AUM mapping cheat-sheet** (per plugin: channel +
  CC list) so configuring AUM is mechanical.
- MIDI-learn (capture inbound CC) is the primary path for **hardware**; for
  **plugins** it is optional. AUM does **not** echo parameter changes as MIDI
  (input-only), so plugin control is **open-loop** and definitions are verified
  at authoring time, not by a live echo — see `docs/research/auv3-feedback.md`.
- AUM itself and each plugin are ordinary **logical devices** bound to the
  iPad's BLE-MIDI endpoint on **distinct MIDI channels**.

## Deployment

- A **persistent systemd user daemon** exposing MCP over **streamable-HTTP bound
  to `127.0.0.1`** (never a wide bind). Hardware connections, inbound listening,
  and desired-state are long-lived by nature, so they should survive editor
  sessions.
- Install: `go install ./cmd/mcp-midi-controller` (lands in `$GOBIN` / `~/.go/bin`),
  then the provided user unit `init/mcp-midi-controller.service`
  (`systemctl --user enable --now …`). See the README for the exact steps.
- **Startup is serve-first**: the loopback MCP endpoint binds and starts serving
  immediately; restoring bindings is synchronous (cheap), but inbound BLE
  listening is kicked off in the **background** so an unreachable/powered-off
  endpoint can never gate the daemon from coming up. Unreachable endpoints are
  retried on demand by verify/learn/probe.
- **Cursor** (and other MCP clients) connect via `.cursor/mcp.json`, which points
  at the daemon's loopback URL (`http://127.0.0.1:7799/`).
- SDK: **`github.com/modelcontextprotocol/go-sdk`** (official, stable) —
  built-in input-schema validation before the handler, `StreamableHTTPHandler`,
  dynamic tools + `list_changed`. Generated tools set `Tool.InputSchema`
  explicitly so runtime per-control value schemas (ranges/enums) work.

## Storage layout

```
$XDG_CONFIG_HOME/mcp-midi-controller/     # rig-as-code — git init here
  config.yaml                             #   daemon listen addr, defaults
  devices/*.yaml                          #   custom/learned definitions (override bundled by definition id)
  bindings.yaml                           #   endpoints+channels → devices
  scenes/*.yaml                           #   saved scenes
$XDG_STATE_HOME/mcp-midi-controller/      # volatile — not versioned
  desired-state.json                      #   last applied state (resume on restart)
  *.log
```

Bundled definitions: `internal/device/definitions/*.yaml` (embedded in the binary).

## Build order (risk-first)

1. **BLE spike (throwaway)** — BlueZ D-Bus discover → pair (`Agent1`) → connect →
   write the BLE-MIDI characteristic and receive notifies, proven against the
   **MD-200** (flip On/Off CC 28, sweep Rate CC 17, read back an inbound CC).
   De-risks the whole project.
2. **Engine library** — transport interface + `blemidi`; YAML loader (embed +
   user dir); binding model; desired-state; control rendering (CC/PC/NRPN/SysEx).
3. **MCP daemon** — go-sdk over loopback streamable-HTTP (systemd user unit);
   globals; dynamic per-device tools + `list_changed`; in-handler validation.
4. **Scenes** — save/recall/list; PC→CC ordering + settle delay; additive/exact.
5. **Authoring + MIDI-learn** — definition authoring tools + inbound capture.
6. **OSC transport (X32)** — a second, non-MIDI backend (keeps the abstraction honest).
7. **USB editor/readback tools** — USB-MIDI + vendor-HID transports exposing the
   pedals' deep editor protocols (read device state, author SL-2 patterns, verify
   what BLE writes landed). First design: `docs/usb-tools.md`.
8. **AUv3 feedback (`auv3-probe`)** — an off-daemon iPad utility that dumps each
   plugin's `AUParameterTree` to verify the plugin definitions are correct and
   cover the maximum functionality (AUM doesn't echo MIDI, so this replaces the
   BLE echo for plugins). Design: `docs/research/auv3-feedback.md`. Implemented:
   the iPad app lives in its own repo
   ([github.com/teemow/auv3-probe](https://github.com/teemow/auv3-probe)) and
POSTs dumps to the daemon's built-in **probe receiver** (a LAN listener
  separate from the loopback MCP endpoint, `internal/auv3receiver`, config
  `auv3_receiver_addr`, default `:7800`). Staged dumps are exposed to agents
  via `list_auv3_probes` / `get_auv3_probe` and turned into definitions via
  `import_auv3_probe`. The standalone `cmd/auv3-probe` still exists for running
  the receiver apart from the daemon. The receiver also accepts a per-run
  **diagnostics report** (`POST /auv3-probe/diagnostics`, stored under
  `_diagnostics/`) recording every probe outcome — including plugins that fail
  to instantiate and so produce no dump — and **stages empty-parameter dumps**
  rather than rejecting them (a plugin with no AUM-mappable params is valid
  diagnostic data). Dumps carry richer per-param metadata (parameter-group,
  flag bitfield/decoded flags, and `dependentParameters` so meta/macro controls
  are recognised — the draft builder and AUM cheat-sheet flag a macro so its
  derived params are not separately mapped) plus unit-level metadata (factory
  **and user** presets with their numbers — recallable by Program Change so an
  agent can build preset-recall scenes — human manufacturer/version, channel
  capabilities, latency/tail time); non-finite AU values are clamped to finite
  sentinels for transport. User-preset names are installation-specific, so they
  only ever live in the gitignored state dir / user config — never in committed
  artifacts.

All listed devices are v1; beyond the two transports (BLE-MIDI + OSC) most of the
remaining work is **YAML definitions + MIDI-learn**, not core code.

## Key references

### Devices

- **Eventide H90** — global/MIDI implementation:
  <https://cdn.eventideaudio.com/manuals/h90/1.7.1/content/appendix/global.html>
- **Boss MD-200** — default CC map inlined above (no public manual link); also in
  `internal/device/definitions/md-200.yaml`.
- **Boss SL-2** — "Controlling This Unit from an External MIDI Device" (3 CCs +
  MIDI clock; no PC): <https://static.roland.com/manuals/sl-2/eng/33861479.html>
  (research note: `docs/research/sl-2.md`).
- **Source Audio EQ2** — user guide + MIDI Implementation Guide (PC presets, CC
  per parameter, remappable in Neuro): <https://sourceaudio.net/products/eq2>
  (research note: `docs/research/eq-2.md`).
- **Morningstar ML10X** — Control Change messages:
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>
- **Two Notes Opus** — MIDI chart:
  <https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual#midi_chart>
- **Behringer X32** — MIDI table (and OSC protocol):
  <https://behringer.world/wiki/doku.php?id=x32_midi_table>
  (research note: `docs/research/x32.md` — verified live against an **X32 RACK**,
  FW 4.13, OSC/UDP 10023)
- **AUM (iPad)** — help / MIDI control:
  <https://kymatica.com/aum/help>

### AUv3 plugins / synths (controlled via the AUM CC convention)

- Verifying plugin definitions (no AUM MIDI echo; `auv3-probe` →
  `AUParameterTree` dump): `docs/research/auv3-feedback.md`
- Unfiltered Audio Battalion — <https://www.unfilteredaudio.com/products/battalion>
- Arturia iSEM — <https://www.arturia.com/products/ios-instruments/isem/overview>
- Kai Aras Agonizer — <https://apps.apple.com/app/agonizer/id1583662383>
- Korg iMS-20 — <https://www.korg.com/products/software/ims20/>
- FabFilter plugins — <https://www.fabfilter.com/products>

### Protocols & libraries

- **BLE-MIDI spec**: service `03B80E5A-EDE8-4B33-A751-6CE34EC4C700`, char
  `7772E5DB-3868-4112-A1A9-F2669D106BF3`, 13-bit timestamp framing —
  <https://www.midi.org/specifications/midi-transports-specifications/midi-over-bluetooth-low-energy-ble-midi>
- **Go BLE**: `tinygo.org/x/bluetooth` does GATT but **not pairing** → we use
  BlueZ D-Bus directly for the pairing-owning requirement —
  <https://github.com/tinygo-org/bluetooth>
- **BlueZ D-Bus API** (adapter/device/agent/GATT) —
  <https://github.com/bluez/bluez/tree/master/doc>
- **gomidi** (USB/ALSA bonus transport) — <https://gitlab.com/gomidi/midi>
- **MCP go-sdk** — <https://github.com/modelcontextprotocol/go-sdk>
