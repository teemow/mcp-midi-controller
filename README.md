# mcp-midi-controller

A [Model Context Protocol](https://modelcontextprotocol.io) server, written in
Go, that turns a MIDI/OSC rig into something you can **set up, sound-design and
manage scenes for conversationally**.

It is a *rig-setup and scene-management* layer, **not** a real-time/live
controller — an LLM-driven server has request/response latency and does not
belong in the live foot-control path. Use your footswitch (e.g. a Morningstar
ML10X) to play live; use this to *build* the sounds and scenes it recalls.

> Status: **early scaffold**. The architecture is settled (see
> [`docs/design.md`](docs/design.md)); implementations are stubs with `TODO`s.

## What it does

- Controls a heterogeneous rig from one conversation:
  - **Pedals** over BLE-MIDI (Boss MD-200, Eventide H90, Two Notes Opus,
    Morningstar ML10X) — typically behind a BLE-MIDI hub (e.g. a CME WIDI
    Thru6).
  - **Behringer X32** over OSC/UDP (on WiFi).
  - **AUM on iPad** and its **AUv3 plugins/synths** (Battalion, iSem, Agonizer,
    Korg iMS-20, FabFilter, …) over BLE-MIDI.
- **Owns BLE discovery + pairing** itself (via BlueZ over D-Bus) — no manual
  `bluetoothctl`.
- **Extendable without writing Go**: add a device by dropping a YAML definition,
  or have an agent author one via **MIDI-learn**.
- **Scenes**: snapshot and recall sounds across the whole rig; scenes are
  partial and layerable.
- **Rig-as-code**: your definitions, bindings and scenes live in one
  git-trackable directory.

## How it is wired

```
MCP client (Cursor/Claude)  ──HTTP 127.0.0.1──▶  mcp-midi-controller daemon
                                                  ├─ engine (registry, bindings,
                                                  │          desired-state, scenes)
                                                  └─ transports
                                                     ├─ blemidi  (BlueZ D-Bus + BLE-MIDI GATT)
                                                     ├─ osc      (X32)
                                                     └─ usbmidi  (gomidi, bonus)
```

A device is a **YAML definition** (its controls, with CC / PC / NRPN / SysEx /
OSC addressing) bound to a transport **endpoint + MIDI channel**. Each bound
logical device gets its own generated MCP tool (`control_<name>`) whose schema
enumerates that device's controls and validates values before anything hits the
wire.

See [`docs/design.md`](docs/design.md) for the full design and rationale.

## Requirements

- Linux with **BlueZ** (BLE is Linux-first via the BlueZ D-Bus API).
- Go 1.26+.
- For the `usbmidi` bonus backend: ALSA dev headers (rtmidi/CGO).

## Repository layout

```
cmd/mcp-midi-controller/   daemon entrypoint
internal/
  config/                  XDG paths + config.yaml
  device/                  YAML definition model, loader, validation, bundled defs
    definitions/           bundled device definitions (go:embed)
  transport/               Transport interface + backends (blemidi, osc, usbmidi)
  engine/                  registry, bindings, desired-state, scene orchestration
  scene/                   scene model + persistence
  mcpserver/               MCP layer (official go-sdk): tool generation + handlers
docs/design.md             full design
```

## Configuration (rig-as-code)

```
$XDG_CONFIG_HOME/mcp-midi-controller/   # git init this
  config.yaml
  devices/*.yaml                        # your definitions (override bundled by filename)
  bindings.yaml                         # endpoints+channels → devices
  scenes/*.yaml
$XDG_STATE_HOME/mcp-midi-controller/    # volatile (desired-state cache, logs)
```

## License

MIT
