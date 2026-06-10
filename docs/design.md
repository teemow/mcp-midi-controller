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

## Target device categories (v1)

The categories below are the v1 targets — a generic catalog of what the
server models, not a description of any one installation. The concrete inventory
of a particular rig (which units, which endpoints, which channels) is
installation-specific; a documented example is kept in `docs/private/`
(gitignored).

| Device | Category | Transport | Notes | Reference |
|--------|-------|-----------|-------|-----------|
| Boss MD-200 | pedal | BLE-MIDI | Pure CC. Full CC map known (see below). | — (CC map inlined below) |
| Boss SL-2 | pedal | BLE-MIDI (TRS) | 3 CCs only (CC16/80/81) + MIDI-clock sync. No PC; pattern/type not MIDI-addressable. | [SL-2 MIDI](https://static.roland.com/manuals/sl-2/eng/33861479.html) |
| Source Audio EQ2 | pedal | BLE-MIDI | 128 presets via PC; all params via CC (remappable default map). | [EQ2 manual](https://sourceaudio.net/products/eq2) |
| Eventide H90 | pedal | BLE-MIDI | Program change (presets) + CC + SysEx. | [H90 global/MIDI](https://cdn.eventideaudio.com/manuals/h90/1.7.1/content/appendix/global.html) |
| Two Notes Opus | pedal | BLE-MIDI | CC + program change. | [Opus MIDI chart](https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual#midi_chart) |
| Morningstar ML10X | controller/hub | BLE-MIDI | CC. Also the live foot controller. | [ML10X CC messages](https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages) |
| Behringer X32 | mixer | OSC/UDP | OSC (`/ch/01/mix/fader`), not MIDI. On WiFi. | [X32 MIDI table](https://behringer.world/wiki/doku.php?id=x32_midi_table) |
| AUM (iPad) | host | auv3midi | Mixer/transport/routing via AUM MIDI control. | [AUM help](https://kymatica.com/aum/help) |
| AUv3 plugins/synths | software | auv3midi | Battalion, iSem, Agonizer, iMS-20, FabFilter, … | see plugin list below |

### Boss MD-200 — default CC map

The MD-200 has no public manual link here; the default CC numbers are recorded
directly (also bundled in `internal/device/device-types/md-200.yaml`):

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
- …and other AUv3 instruments/effects (add a YAML device type per plugin).

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
                         ┌───────────────────────────────────────────────┐
   MCP client            │            mcp-midi-controller daemon          │
 (Cursor / Claude) ──────┤  (systemd user unit, streamable-HTTP,          │
   HTTP 127.0.0.1        │   127.0.0.1 only)                              │
                         │                                                │
                         │  ┌──────────┐   generates    ┌─────────────┐   │
                         │  │ mcpserver │◀─────────────▶│  engine     │   │
                         │  │ (go-sdk)  │  one tool      │ type registry│  │
                         │  └──────────┘  per device     │ devices     │   │
                         │                               │ state       │   │
                         │                               │ scenes      │   │
                         │                               └─────┬───────┘   │
                         │      ┌─────────┬─────────┬──────────┼───────┐   │
                         │      ▼         ▼         ▼          ▼       ▼   │
                         │ ┌────────┐ ┌────────┐ ┌──────┐ ┌─────┐ ┌────────┐
                         │ │ blemidi │ │ usbmidi│ │usbhid│ │ osc │ │auv3midi│
                         │ │(BlueZ)  │ │ (ALSA) │ │(hid) │ │(X32)│ │ (AUM)  │
                         │ └───┬─────┘ └───┬────┘ └──┬───┘ └──┬──┘ └───┬────┘
                         └─────┼───────────┼─────────┼────────┼────────┼─────┘
                               ▼           ▼         ▼        ▼        ▼
                         BLE-MIDI peripherals      USB        X32   iPad / AUM
                       (hub → pedals, iPad)      editors          (mixer + AUv3)
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
- **`usbmidi`** — ALSA rawmidi (SysEx editor protocols, e.g. Boss/Roland).
- **`usbhid`** — hidraw vendor reports (HID editor protocols, e.g. Source Audio Neuro, Two Notes Torpedo).
- **`auv3midi`** — the LAN control channel into AUM on the iPad. CC/PC/note/transport
  commands are delivered to AUM's MIDI control matrix, which routes them to the hosted
  AUv3 plugin parameters. This is how every software device (the AUM mixer and each
  plugin node) is controlled. No pairing.

### The three concepts

The whole system is built from exactly three concepts. Everything else
(transports, codecs, probe dumps, AUM internals) is plumbing that never appears in
the vocabulary.

1. **Device type** — *what a kind of gear is.* Its parameters, how each parameter
   is addressed (CC#, program change, OSC address, an addressed memory location),
   and the **transport it speaks**. Device types ship with the app; you can add
   your own. (Go type: `device.DeviceType`.)
2. **Device** — *one device in your rig.* A device **of** a device type, plus
   **where it is** (endpoint + channel). (Go type: `engine.Device`.)
3. **Scene** — *parameter settings across all your devices.*

### Device types (the extension mechanism)

A device type is a **declarative YAML file** — no Go code. It doubles as the
**validation schema** for that device's generated MCP tool. It declares the
**transport it speaks** and a list of controls; each `Control` has a semantic
name, a wire `type`, an address, and a value spec:

```yaml
id: md-200
name: Boss MD-200
manufacturer: Boss
transport: blemidi    # the transport this kind of gear speaks
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
`dB` allowed via `unit`), `float`/`int`, and `string` (free text payloads such as
OSC scribble-strip names).

The **transport is a property of the device type** (a BLE pedal speaks `blemidi`,
an AUv3 plugin speaks `auv3midi`, the X32 speaks `osc`). The **channel and
endpoint are not** — those are supplied by the device instance, so one device type
(e.g. H90) can be used by several devices on different channels.

A device type may address its parameters over **more than one transport**: a Boss
SL-2 exposes live controls over `blemidi` and deep memory (slicer patterns) over a
`usbmidi` editor protocol. The type records which parameter travels over which
transport; the engine routes each parameter to the matching connection on the
device. This is internal — to the user it is one device type with one set of
parameters.

Device types ship inside the binary via `go:embed` (source of truth:
`internal/device/device-types/*.yaml`). User device types in
`$XDG_CONFIG_HOME/mcp-midi-controller/device-types/*.yaml` **override bundled ones
by `id`** (not by filename — the loader keys the registry on the `id:` field, so a
user file with a bundled id replaces it whatever the file is named) and add new
ones.

#### `generic-midi` fallback

A built-in device type whose controls are **parametric** — the CC/NRPN/program
number is supplied at call time. Using `generic-midi` for any unmodeled
endpoint+channel makes it controllable immediately (by raw number), while still
flowing through desired-state and scenes (unlike `send_raw`, which is untracked).

### Devices (the rig)

A **device** is a device type + **where it is**, addressed by its `name`. The name
generates the device's control tool (`control_<name>`) and is the key scenes and
desired-state use. The common single-transport device is flat:

```yaml
# devices.yaml — illustrative only. Real per-rig devices (actual endpoint
# names + channel assignments) live in the user's config dir and are not
# committed; see docs/private/ for a documented example.
- name: h90           # device name -> generates tool control_h90
  type: h90           # device type id
  endpoint: "<ble-midi-hub>"   # transport endpoint id (a BLE-MIDI hub)
  channel: 1
- name: md200
  type: md-200
  endpoint: "<ble-midi-hub>"
  channel: 2
```

A device that reaches its parameters over more than one transport carries a
`connections` map (transport → where), instead of the flat `endpoint`/`channel`:

```yaml
- name: sl2
  type: sl-2
  connections:
    blemidi: { endpoint: "<ble-midi-hub>", channel: 5 }   # live controls
    usbmidi: { endpoint: "SL-2", writable: true }         # deep memory editor
```

The flat form is shorthand for a one-entry `connections` map. The engine sends
each parameter over the connection its device type assigned. How a device reaches
its parameters is therefore **internal** — you see one device with one set of
parameters regardless of how many transports it speaks.

Devices persist so the daemon restores the rig on restart. Adding or removing a
device generates/removes its tool at runtime and emits
`notifications/tools/list_changed`.

### MCP tools

Generated **per device** (`control_<name>`): the tool's input schema accepts a
**batch** of `{control, value}`. Each batch item is a `oneOf` of per-control
objects derived from the device type — `control` is pinned to a `const` name and
`value` carries that control's own value schema (integer range, enum labels + wire
ints, float bounds + unit, string, or the parametric `{number, value}` shape) — so
the model sees valid ranges/enums up front. The **value** is still validated
in-handler against the value spec as the authoritative safety net, returning
`CallToolResult{IsError:true}` with an RFC-6901 JSON-pointer path on failure
(SEP-1303). Tool count = number of devices + the globals below.

Global tools:

- `list_devices` — the devices in your rig and, with the `available` flag, the
  **device types you could add** (the catalog of known gear, bound or not).
- `describe_device` — the controls, types, ranges/enums of one device (or device
  type). `list_devices`, `describe_device` and `read_state` emit
  `structuredContent` (JSON) alongside the human text so the web client / agents
  get structured data, not just prose.
- `bind_device` / `unbind_device` — add or remove a device in the rig (→
  `list_changed`).
- `discover_devices` — one aggregated view of everything you could add: transport
  endpoints (BLE/USB/OSC), the AUv3 device-type catalog, and the nodes of a loaded
  AUM session — each annotated with where it was found and a suggested device type.
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
  over the BLE-MIDI characteristic. Addressed by endpoint + product (not a device),
  request/reply, and deliberately **outside** the scene/desired-state path.
- Authoring a device type: `create_device_type` / `add_control` /
  `save_device_type`.
- MIDI-learn: `learn_start` / `learn_capture` (reads the inbound notify channel
  to capture the CC/NRPN the user moves).

### State & scenes

- The server keeps an **authoritative desired-state**: per device, the
  last value sent per control. Updated on every control set and **persisted as
  JSON** under the state dir (`$XDG_STATE_HOME/mcp-midi-controller/desired-state.json`)
  so it survives a daemon restart; optionally reconciled from inbound MIDI
  (hand-tweaks on hardware).
- A **scene** is a named snapshot of parameter settings **across all your
  devices** — only the controls that have been set (so scenes are small, partial,
  and **layerable**). For preset-based devices (H90/Opus) it stores the **program
  number plus CC overrides**. A device parameter that is not reachable as a
  CC/PC — such as a Boss SL-2 slicer pattern captured over its USB editor — is
  stored as an ordinary parameter value of that device (an opaque blob); the device
  realizes it over the right transport on recall. A scene never names a transport.
- **Recall** replays **program-change before CC**, with a per-device
  `settle_ms` delay, and supports **additive** (apply over current state) and
  **exact** (reset to scene) modes.
- Scenes are human-readable files in `$XDG_CONFIG_HOME/mcp-midi-controller/scenes/`.
- **Compile + push to a footswitch (design-time):** because recall ordering and
  settle are resolved here, a scene can be *compiled* into a flat, already-ordered
  event list and pushed into a standalone BLE-MIDI footswitch (HTTP over WiFi),
  which then replays it live with no laptop in the path. The footswitch is a
  faithful player: it does not re-derive recall semantics. See
  `export_scene_to_footswitch`. Each event keeps its device's MIDI channel, so
  the **routing host** the footswitch is connected to live (e.g. AUM on the iPad)
  must fan the replayed messages out to the gear by channel — the footswitch does
  not address the pedal hub directly. Verified end-to-end against real hardware
  (push → store → inbound-trigger → BLE replay → host relay → MIDI hub → pedal
  recalled its program). Note: this is also why a per-device **channel** must be
  correct (0-based wire channel); a wrong channel silently routes to the wrong
  pedal even though the push/replay path is fine.

### AUv3 plugins & AUM — the convention model

> **Vision (the bigger prize).** The same convention model, applied to AUM
> *itself*, turns the in-host `ProbeMidiBrain` into a near-complete remote for
> AUM — transport, tempo, mixer, **any node parameter**, and **session load** are
> all reachable (measured), but **only** for what a session maps. So the brain's
> control power reduces to deep **session understanding** + a **standard mapping
> authored into every session** so the brain can change scenes. The full vision,
> the measured proof, and the open gaps are in
> [aum-brain-control.md](aum-brain-control.md).

An **AUM session is the rig *inside the iPad*** — the mirror of the hardware rig
this server controls. Its mixer strips and the AUv3 plugins hosted on them are
**devices**, controlled over the `auv3midi` transport (the LAN channel into AUM).

- **AUv3 plugins are device types** (`transport: auv3midi`). Unlike hardware
  pedals, their parameters have no manufacturer-assigned CC#s — a parameter only
  responds to a CC if it is mapped inside AUM. So the device type's
  `param → (channel, CC)` mapping is a **convention the server defines**, and the
  AUM session is configured to match. Device types for plugins are generated
  directly from an `auv3-probe` parameter dump (see the probe tooling below).
- **AUM session nodes are devices — derived from the file, not the convention.**
  `import_aum_session` runs `DeriveRig` (`internal/aum/rig.go`): every *enabled*
  mapping in the session's MIDI control matrix becomes one control carrying the
  mapping's **exact message type (CC/Note/PC), number and per-control pinned
  MIDI channel** — so a banked ("golden") session spanning channels 2–16 is fully
  addressable through a single device binding. The rig is one **session device**
  (strip level/mute/solo/rec, transport incl. tempo/metronome, system actions,
  built-in routing knobs, tap toggles) plus one **device per hosted AUv3 node**;
  staged probe dumps optionally enrich controls with display names, enum labels
  and AU ranges. Devices are id-scoped per session, so a re-import replaces the
  previous session's devices. Mappings the model cannot express (PBEND/CHPRS)
  are reported, never silently dropped.
- **The import runs automatically** (`aum_auto_import`, default on): when the
  iPad downloads a staged `.aumproj` (the surest signal it is about to be
  loaded) the daemon records it as its **current session** (persisted in the
  state dir), imports the rig, broadcasts an `aum-rig` notification, and pushes
  a **`controlSurface` manifest** to the in-host brain over the `/midi-control`
  WebSocket — per device, per control: a widget kind (`fader | toggle | trigger
  | enum | preset`) plus the exact wire message (`{type, channel, number}`).
  A brain (re)connect re-runs the import and re-pushes the manifest. The brain
  caches the manifest in its AU `fullState` and renders it as an on-device
  control surface that emits into AUM locally — it keeps working with the
  daemon offline. Older brains silently drop the unknown frame type, so the
  push is backward-compatible.
- The authoring tools emit an **AUM mapping cheat-sheet** (per device type:
  channel + CC list) so configuring a session by hand stays mechanical.
- MIDI-learn (capture inbound CC) is the primary path for **hardware**; for
  plugins it is unnecessary. AUM does **not** echo parameter changes as MIDI
  (input-only), so plugin control is **open-loop** and device types are verified
  at authoring time against the probe dump, not by a live echo — see
  `docs/research/auv3-feedback.md`.

## Deployment

- A **persistent systemd user daemon** exposing MCP over **streamable-HTTP bound
  to `127.0.0.1`** (never a wide bind). Hardware connections, inbound listening,
  and desired-state are long-lived by nature, so they should survive editor
  sessions.
- Install: `go install ./cmd/mcp-midi-controller` (lands in `$GOBIN` / `~/.go/bin`),
  then the provided user unit `init/mcp-midi-controller.service`
  (`systemctl --user enable --now …`). See the README for the exact steps.
- **Startup is serve-first**: the loopback MCP endpoint binds and starts serving
  immediately; restoring devices is synchronous (cheap), but inbound BLE
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
  device-types/*.yaml                     #   custom/learned device types (override bundled by id)
  devices.yaml                            #   your devices (type + where it is) → the rig
  scenes/*.yaml                           #   saved scenes
$XDG_STATE_HOME/mcp-midi-controller/      # volatile — not versioned
  desired-state.json                      #   last applied state (resume on restart)
  *.log
```

Bundled device types: `internal/device/device-types/*.yaml` (embedded in the binary).

## Build order (risk-first)

1. **BLE spike (throwaway)** — BlueZ D-Bus discover → pair (`Agent1`) → connect →
   write the BLE-MIDI characteristic and receive notifies, proven against the
   **MD-200** (flip On/Off CC 28, sweep Rate CC 17, read back an inbound CC).
   De-risks the whole project.
2. **Engine library** — transport interface + `blemidi`; YAML loader (embed +
   user dir); device model; desired-state; control rendering (CC/PC/NRPN/SysEx).
3. **MCP daemon** — go-sdk over loopback streamable-HTTP (systemd user unit);
   globals; dynamic per-device tools + `list_changed`; in-handler validation.
4. **Scenes** — save/recall/list; PC→CC ordering + settle delay; additive/exact.
5. **Authoring + MIDI-learn** — device-type authoring tools + inbound capture.
6. **OSC transport (X32)** — a second, non-MIDI backend (keeps the abstraction honest).
7. **USB editor/readback tools** — USB-MIDI + vendor-HID transports exposing the
   pedals' deep editor protocols (read device state, author SL-2 patterns, verify
   what BLE writes landed). First design: `docs/usb-tools.md`. Implemented: the
   `usbmidi`/`usbhid` transports, the four protocol codecs (`internal/usbcodec`:
   roland / morningstar / neuro / torpedo), the value encodings, the
   request/reply session + engine USB API (`internal/engine/usb.go`), the USB
   transport connection, the generic `usb_*` and semantic per-device tools
   (`internal/mcpserver/usb_tools.go` + `usb_device_tools.go`), the two-key write
   gate, and USB-backed `verify_control`. The device **profiles** that drive it
   are authored in the bundled device types: `sl-2` (roland address SysEx, full
   read + gated write), `eq-2` (neuro HID, 128-preset read + select), `ml10x`
   (morningstar editor, bank read), and an `opus` torpedo-HID monitor-only
   placeholder; the `h90` has no USB editor protocol. **Patch-level scenes** are
   supported: a scene may carry a captured USB memory blob as an opaque parameter
   value of a device (`capture_usb_patch`) that `recall_scene` writes back over USB
   (gated) — the only way to capture the SL-2's pattern/type. Remaining: hardware
   re-verification of writes and the not-yet-decoded parameters (ML10X write
   opcodes, EQ2 per-parameter byte offsets, Opus value scaling).
8. **AUv3 feedback (`auv3-probe`)** — an off-daemon iPad utility that dumps each
   plugin's `AUParameterTree`, the source from which the plugin's device type is
   generated and verified to cover the maximum functionality (AUM doesn't echo
   MIDI, so this replaces the BLE echo for plugins). Design:
   `docs/research/auv3-feedback.md`. Implemented:
   the iPad app lives in its own repo
   ([github.com/teemow/auv3-probe](https://github.com/teemow/auv3-probe)) and
POSTs dumps to the daemon's built-in **probe receiver** (a LAN listener
  separate from the loopback MCP endpoint, `internal/auv3receiver`, config
  `auv3_receiver_addr`, default `:7800`). Staged dumps are exposed to agents
  via `list_auv3_probes` / `get_auv3_probe` and turned into device types via
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
9. **AUM session read/write (`internal/aum`)** — a Go library that **reads,
   edits, and authors** AUM session (`.aumproj`) and standalone mapping
   (`.aum_midimap`) files, and an MCP surface over it. The format is an Apple
   `NSKeyedArchiver` binary plist; the verified schema is in
   `docs/research/aum-session.md`. The writer is a **graph round-trip** (decode
   the `NSKeyedArchiver` object graph via `howett.net/plist`, mutate targeted
   objects, re-encode), so fidelity is checked by semantic graph equality plus
   an on-device "AUM still opens it" acceptance test — never byte equality.
   Mapping a parameter edits AUM's existing disabled placeholder leaf in place
   (`specState` v13 / `spec` v8/10) rather than adding objects; authoring a
   session from scratch clones an embedded minimal template and mutates it, and
   `BuildSession` rejects specs that would deserialize but crash AUM's audio
   render thread (an audio channel head with no audio source) — the
   load-vs-render crash classes and their fixes are documented in
   `docs/research/aum-session.md`. The
   library layers as: `archive.go` (the generic graph codec), `session.go` +
   `spec.go` + `midimap.go` (the typed read model + the packed `spec`/`specState`
   codec + the flat `SessionMap` JSON), `edit.go` + `export.go` (round-trip edits
   + `.aum_midimap` export), and `build.go` + `template.go` (template-clone
   authoring). Like the AUv3 probe, the iPad app POSTs `.aumproj` bytes to an
   **off-MCP LAN receiver** (`internal/aumreceiver`, mounted on the same shared
   LAN listener as the probe receiver — `auv3_receiver_addr`, default `:7800` —
   not the loopback MCP endpoint): `POST /aum-session` stages an upload, `GET
   /aum-session` is the manifest, `GET /aum-session/{file}` downloads a
   generated/edited file back to AUM. The daemon stages files under the state
   dir (`config.AUMSessionsDir()`, gitignored — sessions are private rig
   snapshots, never committed) and exposes them via MCP tools in
   `internal/mcpserver/aum_tools.go`: `list_aum_sessions`, `get_aum_session`
   (full flat layout as `structuredContent`), `diff_aum_session` (compare a
   session's channel-control mappings against the server AUM mixer CC
   convention), `import_aum_session` (match each hosted node to a staged
   `auv3-probe` dump by component tuple and create one device per node — `{name,
   type, channel}` — on its session-derived channel, plus a session-derived AUM
   mixer device; falls back to proposing devices when a channel can't be inferred),
   `author_aum_session` / `edit_aum_session` (generate/mutate → stage
   for download), and `export_aum_midimap`. The iPad app stays a thin byte-ferry
   — **Go owns all serialization** — so no Swift `NSKeyedArchiver` work is
   needed. Open format gap: the **Program Change / Pitch Bend / Channel
   Pressure** `type` codes are still unknown (the corpus is unmapped (v13) or
   CC/Note only (v10)), so `export_aum_midimap` for those message types is
   blocked until one enabled sample is captured; CC/Note work now.
10. **Audio tap (`internal/audiotap`)** — the agent's "ears". The auv3-probe
    **ProbeAudioTap** AUv3 (`aufx`) is inserted on an AUM audio channel and
    streams **full-rate, interleaved stereo** PCM plus RMS/peak features over a
    WebSocket to the daemon (`GET /audio-stream[?name=<tap>]`, mounted on the
    same shared LAN listener as the probe + session receivers —
    `auv3_receiver_addr`, default `:7800` — not the loopback MCP endpoint). The
    receiver terminates the
    [contract](https://github.com/teemow/auv3-probe/blob/main/docs/auv3-extension.md)
    (one TEXT `format` message giving the real channel count + host rate, BINARY
    little-endian `Float32` interleaved PCM, ~10 Hz TEXT `features`) into an
    **in-memory** store: the latest levels plus a rolling PCM window (~10 s,
    capped). **Multiple named taps stream concurrently** — one per AUM channel
    you tap — kept apart by a `Registry` of per-tap stores keyed by the
    `?name=` query parameter (the author tools embed this name in the tap's
    config; un-named producers fall back to their remote address so they still
    don't clobber each other). The audio MCP tools (`get_audio_tap`,
    `get_audio_clip`, `probe_sound`, `capture_audio_snapshot`) take an optional
    tap `name` and default to the most-recently-active tap. The live window lives only in RAM (a private, volatile rig signal);
    the only thing written to disk is the per-probe segment WAV below, under the
    volatile state dir (`audio-clips/`, gitignored + retention-capped), never
    committed. The store backs the read-only `get_audio_tap`
    MCP tool (connection state, last + window-derived RMS/peak, a short
    peak-envelope waveform, and age metadata as `structuredContent`); a tap
    connecting or dropping is broadcast as an `audio-tap` log notification.
    Long-lived sockets clear the shared listener's read/write deadlines
    (`http.ResponseController`) before upgrading so they are not dropped at the
    60 s timeout. The window also carries **trusted Go-computed analysis** so the
    agent never has to DSP base64 PCM: frequency-domain **spectral** features
    (`internal/audiotap/spectral.go` — centroid, flatness, log bands) and a
    **musical analysis** block (`internal/audiotap/analysis.go` — detected pitch
    `f0`/note/cents/confidence via McLeod NSDF autocorrelation, harmonic partials
    + harmonic-to-noise ratio, loudness/crest dBFS, and spectral-flux onsets),
    surfaced in both `get_audio_tap`'s `structuredContent` and its human text.
    The **sound-engineer iteration tools** build on this: `get_audio_clip`
    (full-rate interleaved PCM as base64 `f32le` + sample rate + channel count
    for an agent that wants the raw signal), `probe_sound` (one call: optionally
    set device controls / raw CCs, then **serialize** an isolated capture —
    settle → mark epoch → note-on → hold → mark epoch → note-off → extract +
    analyse exactly that segment — so back-to-back probes never contaminate each
    other; it returns the analysis, a delta vs the previous probe, and a
    `wav_path` to the captured stereo segment on disk), and the A/B pair
    `capture_audio_snapshot {label}` / `compare_audio {a,b}` (an in-memory,
    label-keyed snapshot store returning signed `b-a` deltas — loudness dBFS,
    pitch cents, spectral centroid/flatness, HNR, partial/onset counts — so a
    tweak's effect "louder / brighter / more harmonic / detuned" is deterministic;
    `internal/mcpserver/{audio,probe,compare}_tools.go`). These analysis/iterate/
    compare features have a **mandatory live test loop** as their acceptance gate
    (`scripts/sound-loop.sh` + `docs/research/sound-engineer-test-loop.md`): the
    synthetic Go tests in `internal/audiotap` are the fast inner correctness loop,
    but a feature is "done" only when the live loop passes against the real
    iPad/AUM/synth rig over the LAN.

All listed devices are v1; beyond the transports most of the remaining work is
**YAML device types + MIDI-learn**, not core code.

## Key references

### Devices

- **Eventide H90** — global/MIDI implementation:
  <https://cdn.eventideaudio.com/manuals/h90/1.7.1/content/appendix/global.html>
- **Boss MD-200** — default CC map inlined above (no public manual link); also in
  `internal/device/device-types/md-200.yaml`.
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

- Verifying plugin device types (no AUM MIDI echo; `auv3-probe` →
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
