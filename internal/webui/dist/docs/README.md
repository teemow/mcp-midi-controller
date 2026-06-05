# mcp-midi-controller

A [Model Context Protocol](https://modelcontextprotocol.io) server, written in
Go, that turns a MIDI/OSC rig into something you can **set up, sound-design and
manage scenes for conversationally**.

It is a *rig-setup and scene-management* layer, **not** a real-time/live
controller — an LLM-driven server has request/response latency and does not
belong in the live foot-control path. Use your footswitch (e.g. a Morningstar
ML10X) to play live; use this to *build* the sounds and scenes it recalls.

> Status: **functional**. The core is implemented end-to-end — engine, BLE-MIDI /
> OSC / USB transports, dynamic per-device tools with per-control value schemas,
> scenes (save/recall), device authoring + MIDI-learn, desired-state persistence,
> and the systemd-managed loopback daemon (see [`docs/design.md`](docs/design.md)).
> What remains is per-control **hardware validation** on the live rig (the harness
> is `scripts/validate.sh`).

## What it does

- Controls a heterogeneous rig from one conversation:
  - **Pedals** over BLE-MIDI (Boss MD-200, Eventide H90, Two Notes Opus,
    Morningstar ML10X) — typically behind a BLE-MIDI hub (e.g. a CME WIDI
    Thru6).
  - **Behringer X32** over OSC/UDP (on WiFi).
  - **AUM on iPad** and its **AUv3 plugins/synths** (Battalion, iSem, Agonizer,
    Korg iMS-20, FabFilter, …) over BLE-MIDI. Plugin definitions are verified
    with the companion **[auv3-probe](https://github.com/teemow/auv3-probe)**
    iPad app, which dumps each plugin's `AUParameterTree` to the daemon's
    built-in **probe receiver** (a LAN listener separate from the loopback MCP
    endpoint). Agents then see what's available and configurable via the
    `list_auv3_probes` / `get_auv3_probe` tools and scaffold definitions with
    `import_auv3_probe`.
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
cmd/usb-probe/             read-only USB readback spike (validation oracle)
cmd/auv3-probe/            standalone AUv3 dump receiver (same listener is now
                           built into the daemon; this is for running it apart)
internal/
  auv3receiver/            the AUv3 probe receiver (LAN listener, write-only;
                           staged dumps feed list/get/import_auv3_probe)
  audiotap/                ProbeAudioTap audio-stream WebSocket receiver +
                           in-memory level/window store (feeds get_audio_tap)
init/                      systemd user unit
scripts/                   validate.sh (hardware-validation harness) + capture tooling
.cursor/mcp.json           Cursor MCP client config (points at the loopback daemon)
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

## Running as a daemon (systemd user service)

The server is a long-lived daemon (hardware connections, inbound listening and
desired-state are long-lived), so run it as a **systemd user service**. A unit
is provided at [`init/mcp-midi-controller.service`](init/mcp-midi-controller.service).

Deployment is a single idempotent command — use it for the first install **and**
to roll out a new build:

```bash
make deploy        # == scripts/deploy.sh
```

That [`scripts/deploy.sh`](scripts/deploy.sh) builds the embedded SPA, installs
the binary into `~/.go/bin` (the path the unit's `ExecStart` references),
installs/refreshes the user unit, `daemon-reload`s, enables lingering (so the
daemon survives logout on a headless rig host), then enables and (re)starts the
service. Re-running it is always safe.

Operate the running service with the helper targets:

```bash
make status        # systemctl --user status mcp-midi-controller.service
make restart       # restart to pick up a manually-built binary
make logs          # journalctl --user -u mcp-midi-controller.service -f
```

> **Consuming a release instead of building from source.** CI auto-tags `main`
> and GoReleaser publishes `linux_amd64` / `linux_arm64` binaries (with the SPA
> already embedded). On a host without Go/Node, drop the released binary into
> `~/.go/bin/mcp-midi-controller`, copy `init/mcp-midi-controller.service` into
> `~/.config/systemd/user/`, then `systemctl --user daemon-reload && systemctl
> --user enable --now mcp-midi-controller.service`.

The daemon binds loopback only; [`.cursor/mcp.json`](.cursor/mcp.json) points
Cursor at it (`http://127.0.0.1:7799/`). If you change `listen_addr` in
`config.yaml`, update that URL to match.

In addition to the loopback MCP endpoint, the daemon runs the **iPad receiver**
on a separate LAN address (`auv3_receiver_addr` in `config.yaml`, default
`:7800`; set to `""` to disable). This is intentionally LAN-reachable so the
[auv3-probe](https://github.com/teemow/auv3-probe) iPad app can reach it. One
listener carries three surfaces, none of which touch hardware:

- **AUv3 probe** dumps POSTed by the probe app (staged as JSON for the
  `list_auv3_probes` / `get_auv3_probe` tools).
- **AUM sessions** ferried in/out for the `aum` tools.
- **Audio tap** — a `GET /audio-stream` **WebSocket** that terminates the
  ProbeAudioTap AUv3's stream (decimated mono PCM + RMS/peak features). It keeps
  the latest levels and a short rolling window **in memory only** (audio is a
  private rig signal, never written to disk) and exposes them read-only through
  the `get_audio_tap` / `get_audio_clip` MCP tools — the agent's "ears". A tap
  connecting or dropping is broadcast as an `audio-tap` log notification.
- **MIDI control** — a `GET /midi-control` **WebSocket** the ProbeMidiBrain AUv3
  dials in to (`internal/midicontrol`). The daemon pushes note/CC/PC/transport
  command frames the brain re-emits as MIDI — the agent's "hands", driven by the
  `play_notes` / `send_midi` / `set_transport` tools (LAN primary, BLE fallback).
  Brain connect/disconnect is broadcast as a `midi-control` log notification.

If the daemon host runs a default-deny firewall, allow that port from your LAN.
See [`docs/research/auv3-feedback.md`](docs/research/auv3-feedback.md) and, for
the full author → load → play → hear → tweak loop,
[`docs/research/agent-loop.md`](docs/research/agent-loop.md).

## Web UI (signalwave)

The daemon embeds **signalwave**, an in-browser control app, served on the same
loopback listener as the MCP endpoint. Open:

```
http://127.0.0.1:7799/app/
```

(adjust the host/port to match `listen_addr`). It is a real in-browser MCP
client — it talks to the daemon's `/` MCP endpoint over streamable-HTTP (same
origin, no extra config) — with tabs for devices/bindings, definitions and
authoring, schema-driven device control, WIDI, scenes, USB, the iPad
(AUM/AUv3) surface, a live activity feed of inbound MIDI and probe/session
arrivals, a generic tool tester, and the bundled docs. The `/` endpoint stays
pure MCP, so existing clients (Cursor) are unaffected.

The SPA source lives in [`web/`](web) (Vite + React + TypeScript + Tailwind) and
is built into `internal/webui/dist`, which is committed and consumed by
`go:embed` so the Go binary builds (and `go install` works) from a clean
checkout without Node. After changing anything under `web/`, rebuild and commit
the embed dir:

```bash
make web   # == cd web && npm ci && npm run build
```

## License

MIT
