# USB tools — first design

A design for exposing the **USB editor/readback protocols** of the rig's pedals
as MCP tools, alongside the existing BLE-MIDI control surface.

This is the productization of the USB research (`docs/research/usb.md` +
per-device notes). The protocols are already proven by the throwaway spike
`cmd/usb-probe`, which speaks every confirmed protocol below read-only. This doc
turns that spike into a designed subsystem: transports, device profiles, tools,
addressing, and a safety model.

## Why a separate tool family

The BLE control model (`control_<logical>`) renders one semantic control into
one fire-and-forget MIDI message (CC/PC/NRPN), validated by a YAML value spec.
The USB editor protocols are a **different shape**:

- **Request/reply**, not fire-and-forget — you send a read and wait for a data
  reply (RQ1→DT1, `0x36`→HID report, Morningstar request→TLV block).
- **Structured device memory** — addresses/parameters/patches/presets, not a
  flat list of CC knobs.
- **Reads are first-class** — this is the long-promised channel to *verify what
  actually landed* (today `verify_control` only does a BLE MIDI echo, see
  `internal/engine/feedback.go`) and to **back up / author full patches**.

So USB tools are modeled as their own family (`usb_*` generic + `<device>_*`
semantic), with their own binding kind, while reusing the engine's registry,
desired-state, and scene machinery.

## Per-device USB capability matrix

From `docs/research/usb.md` + the per-device notes. "Codec" = the framing the
spike already implements in `cmd/usb-probe`.

| Device | USB channel | Protocol / codec | Read | Write | Status |
|--------|-------------|------------------|------|-------|--------|
| **Boss SL-2** | USB-MIDI (+ **BLE for writes**) | Roland address SysEx (RQ1/DT1, model `00 00 00 00 1D`) | full patch + system (**USB only**) | full (temp patch + store to 88 slots) — **works over BLE too** | **confirmed R/W** |
| **Morningstar ML10X** | USB-MIDI | Morningstar SysEx editor (model `0x07`, TLV blocks) | full config (presets/loops/banks) | TBD (write opcodes not yet mapped) | **confirmed R** |
| **Source Audio EQ2** | vendor HID | Neuro (`0x36` dump 32 B / `0x77` select preset) | preset memory map (128 @ `0x080000`/stride `0x1000`) | preset *select* only (param-write TBD) | **confirmed R + select** |
| **Two Notes Opus** | vendor HID | Torpedo Remote (64-byte vendor pipe) | TBD | TBD | **TBD** |
| **Eventide H90** | — | none over USB (PC/CC/Clock only; mass storage = recovery firmware) | — | — | **unsupported (resolved)** |
| **Boss MD-200** | — (no USB port) | — | — | — | **n/a (BLE only)** |

Design consequence: the subsystem must support **two USB transports** (USB-MIDI
over ALSA rawmidi, and vendor HID over hidraw) and a **request/reply** primitive
neither current transport offers.

> **SL-2 update (confirmed 2026-06-02): DT1 *writes* also work over BLE/TRS.**
> The SL-2 parses Roland DT1 SysEx on its TRS MIDI IN (verified: `EXP_FUNC`
> written over BLE, read back changed over USB). So the SL-2's *write* side
> (`sl2_set_param`, `sl2_write_pattern`, `sl2_recall_pattern`) can be driven over
> the existing **BLE** binding — no laptop/USB host needed live. Only **reads**
> (`sl2_read_*`) require USB, because the SL-2 has TRS MIDI IN only (no MIDI OUT),
> making BLE editing **open-loop** (write absolute values, no readback). This
> blurs the "USB tools vs BLE control" split for the SL-2: deep *editing* is a
> BLE capability, deep *reading* is USB-only. See `docs/research/sl-2.md`.
>
> Temp-patch writes also apply to the **live sound immediately** with **no
> editor-comm handshake** — confirmed audibly (slicer PATTERN swept live over
> BLE) and by USB readback (PATTERN/FX_TYPE/STEP_NUMBER/TEMPO). So `sl2_set_param`
> over BLE is a real-time edit, not just a staged change. `PATCH_SELECT`
> (`0x7F000100`) recall over BLE is the next thing to validate for live preset
> switching.

## Layering

```
            ┌────────────────────── MCP tools ──────────────────────┐
            │  generic:  usb_identify / usb_read / usb_write / usb_dump      │
            │  semantic: sl2_* / ml10x_* / eq2_* / opus_* (generated)        │
            └───────────────────────────┬───────────────────────────┘
                                         │ operate on
                              ┌──────────▼──────────┐
                              │   USB device profile │  (per device: protocol kind,
                              │   + address/param map│   identity, map, encodings)
                              └──────────┬──────────┘
                                         │ codec frames (RQ1/DT1, 0x36, TLV…)
                       ┌─────────────────▼─────────────────┐
                       │       request/reply session        │  (send + await matching reply,
                       │   (timeout, drain, correlation)     │   handshake, retries)
                       └───────┬───────────────────┬────────┘
                               ▼                   ▼
                        ┌────────────┐      ┌────────────┐
                        │  usbmidi   │      │   usbhid   │
                        │ ALSA rawmidi│      │  hidraw    │
                        │ (gomidi)   │      │ (unix r/w) │
                        └────────────┘      └────────────┘
```

### 1. Transports (code-level extension point)

- **`usbmidi`** — finish the existing stub (`internal/transport/usbmidi`). Wrap
  gomidi: `FindOutPort`/`FindInPort` by name substring, `Send(raw)`, and a
  `Listen` that emits inbound SysEx frames. (The spike already does exactly this.)
- **`usbhid`** *(new)* — hidraw `open`/`read`/`write`/`poll` keyed by VID:PID
  (or `/dev/hidrawN`), reusing the spike's `cmd/usb-probe` EQ2 code. No cgo.
- Both stay behind the existing `transport.Transport` interface for
  discovery/connect; the request/reply layer sits on top.

### 2. Request/reply session

The editor protocols are transactional, which `Transport.Send` (fire-and-forget)
does not express. Add a thin **session** helper (generalizing the spike's
`session` type):

```go
// Reply collection within a wait window, correlated to a request.
type USBSession interface {
    Send(req []byte) error
    Request(req []byte, match func(reply []byte) bool, timeout time.Duration) ([]byte, error)
    Handshake(ctx) error   // protocol-specific (e.g. SL-2 editor-comm mode ON)
}
```

Per-protocol implementations supply the framing (checksums, nibbling, identity
gate, editor-mode enable). All of these already exist in `cmd/usb-probe` and move
into `internal/usbcodec/<protocol>`.

### 3. USB device profile

A device's USB capability is described by a **profile** that pairs a protocol
codec (Go) with a declarative **address/parameter map** (YAML, where tabular):

```yaml
# internal/device/definitions/sl-2.yaml  (new `usb:` section, alongside controls:)
usb:
  protocol: roland-address-sysex      # codec id (Go): framing + checksum + handshake
  identity: { mfg: 0x41, model: "00 00 00 00 1D", device: 0x10 }
  addr_bytes: 4
  size_bytes: 4
  handshake: editor-comm-mode          # DT1 0x7F000001 = 01 before writes
  # patch/parameter map (subset; full map derived from BTS, see sl-2.md)
  regions:
    system:   { base: 0x10000000 }
    temp:     { base: 0x20000000 }
    patches:  { base: 0x20100000, count: 88, stride: 0x00100000 }  # 7-bit-safe stride
  params:
    - { name: midi_channel, region: system, addr: 0x08, enc: int1x7, min: 0, max: 10 }
    - { name: tempo,        region: system, addr: 0x00, enc: int4x4, min: 400, max: 3000 }
    - { name: patch_name,   region: temp,   addr: 0x00, enc: ascii16 }
    - { name: slicer1_pattern, region: temp, addr: 0x1000, enc: int1x7, min: 0, max: 50 }
    # … generated from address_map.js
```

Profile kinds, one Go codec each:

| `protocol` | Devices | Notes |
|------------|---------|-------|
| `roland-address-sysex` | SL-2 (later other Boss/Roland) | nibble/`_7bitize`, Roland checksum, editor-comm handshake |
| `morningstar-sysex` | ML10X | `F0 00 21 24 07 …`, XOR checksum, TLV decode |
| `neuro-hid` | EQ2 (C4 sibling) | `0x36`/`0x77`, 38-byte report, 24-bit address |
| `torpedo-hid` | Opus | 64-byte vendor pipe (codec TBD) |

The **map is data**; the **codec is code**. New same-family devices (another Boss
compact pedal) are mostly a new YAML map + reused codec.

### 4. Encodings

USB values aren't all 7-bit CC bytes. The map's `enc` selects a codec
(from the SL-2 work / `constant.js`):

- `int1x7` — 1 byte, 0–127.
- `int1xN` — 1 byte, N significant bits.
- `int2x4` / `int4x4` — value split into 4-bit nibbles across 2 / 4 bytes
  (e.g. SL-2 tempo 1220 = `00 04 0C 04`).
- `int2x7` — 14-bit across two 7-bit bytes.
- `ascii<N>` — fixed-length ASCII (patch/preset names).
- `bytes<N>` — opaque blob (full patch/preset dump).

`ofs` (signed offset, e.g. pitch ±12 stored as 0–24) is carried per-param like
the BLE value spec.

## Addressing & bindings

USB devices are addressed by **USB endpoint**, not `(endpoint, channel)`:

- USB-MIDI: an ALSA rawmidi port name substring (e.g. `"SL-2"`, `"ML10X"`).
- HID: a `VID:PID` (e.g. `29A4:0400`) or explicit `/dev/hidrawN`.

A pedal is **one logical device** that can expose **two surfaces at once** — a
BLE/OSC control surface and an addressed USB editor/readback surface — under a
single name. The `Binding` carries the control fields (`endpoint`, `channel`)
plus an optional `usb:` block:

```yaml
# bindings.yaml — one logical device, both surfaces.
- logical: sl2
  endpoint: "10:2E:AB:DA:AC:66"   # BLE control endpoint (WIDI hub)
  channel: 5                      # control surface (CC/PC)
  device: sl-2
  usb:                            # USB editor/readback surface of the same pedal
    transport: usbmidi            # usbmidi | usbhid
    endpoint: "SL-2"              # ALSA rawmidi port substring (or VID:PID for usbhid)
    # writable: true              # opt this surface in to gated write tools
- logical: eq2
  endpoint: "10:2E:AB:DA:AC:66"
  channel: 0
  device: eq-2
  usb:
    transport: usbhid
    endpoint: "29A4:0400"         # VID:PID
```

`control_<logical>` is generated from the control surface and the USB tool
family (`usb_*`, `<logical>_*`) from the `usb:` surface — both under the one
`logical`. A USB-only device omits `endpoint`/`channel` and keeps just `usb:`.
At the MCP layer, `bind_device` merges surfaces: call it once per transport
(default = control, `usbmidi`/`usbhid` = USB) and both accrue onto the same
logical. `channel`/`endpoint` on the USB call configure the USB surface; the
control surface is preserved (and vice-versa).

(Legacy bindings whose top-level `transport` was `usbmidi`/`usbhid` — the old
"separate `sl2-usb` logical" model — are migrated to a `usb:` surface on load.)

## Tools

### Generic (escape hatch / discovery), per USB binding

| Tool | Action | Safety |
|------|--------|--------|
| `usb_identify` | Identity request → manufacturer/model/firmware | read |
| `usb_read` | `{region|addr, size}` → decoded + raw bytes | read |
| `usb_dump` | dump a whole region/preset/patch as a blob | read |
| `usb_write` | `{addr, data}` raw write | **write (gated)** |

These mirror `send_raw`: powerful, untracked, behind the write gate.

### Semantic (generated from the profile), per device

**SL-2** (`sl-2`, full R/W):

| Tool | Action |
|------|--------|
| `sl2_read_system` | tempo, output, **MIDI channel**, etc. (decoded) |
| `sl2_read_patch` | read the temp patch (or a stored slot) → structured params |
| `sl2_list_patterns` | names of the 88 stored patterns |
| `sl2_get_param` / `sl2_set_param` | one named param in the temp patch (`set` = write, gated) |
| `sl2_write_pattern` | store the temp patch into slot N (`PATCH_WRITE`, gated) |
| `sl2_recall_pattern` | `PATCH_SELECT` slot N into temp (gated; changes the live sound) |

**ML10X** (`ml10x`, read now; write later):

| Tool | Action |
|------|--------|
| `ml10x_read_config` | full editor read (presets/loops/banks) → structured |
| `ml10x_get_preset` | one preset's config |
| `ml10x_write_*` | TBD — write opcodes not yet reverse-engineered |

**EQ2** (`eq-2`, read + select):

| Tool | Action |
|------|--------|
| `eq2_list_presets` | the 128 preset names |
| `eq2_read_preset` | one preset block (bands, frequencies) decoded |
| `eq2_select_preset` | select preset N (`0x77`, gated — changes the live sound) |
| `eq2_set_param` | TBD — param-write not yet mapped |

**Opus** (`opus`): tools TBD pending the Torpedo Remote codec (placeholder so the
profile slot exists).

**H90**: **no USB tools** — documented as unsupported; control stays on BLE
(PC/CC) and preset files are host-side in H90 Control.

## Integration with state / scenes / verify

- **`verify_control` gains a real readback path.** When a logical device's USB
  surface maps the BLE control's parameter (the same logical, or any logical
  sharing the device id), `verify_control` reads the actual value over USB
  instead of (or in addition to) the BLE echo.
  This closes the open-loop gap called out in the research intro. (Many BLE
  controls have no USB counterpart — e.g. SL-2 CC#80 on/off is not a stored
  param — so this is best-effort per parameter.)
- **Patch-level scenes.** For SL-2 (and later ML10X), a scene can store a **full
  patch dump** (blob) and recall it by writing the temp patch + `write_pattern`.
  This is richer than the CC-snapshot scene and is the only way to capture the
  SL-2's pattern/type (not BLE-addressable). Stored under the existing
  `scenes/` dir; the blob is versioned as hex/base64 in the scene YAML.
- **Desired-state.** USB `set_param`/`write` update the same per-logical-device
  desired-state map, so USB and BLE edits reconcile in one place.

## Safety model

Reads are unconditionally safe. Writes change persistent device state (DT1,
`PATCH_WRITE`, preset select), so:

1. **Global write gate** — a daemon config flag `usb_allow_writes` (default
   `false`) and/or a per-binding `writable: true`. With writes disabled, only
   `usb_read`/`*_read_*`/`*_list_*`/`identify` are exposed.
2. **Dry-run** — write tools accept `{dry_run: true}` returning the exact bytes
   that *would* be sent (the spike is the reference for byte-accuracy).
3. **Handshake before write** — `roland-address-sysex` enters editor-comm mode
   first; failure aborts the write.
4. **No blind broadcast** — USB writes target the specific bound endpoint only.
5. **Recall vs store distinction** — `recall_pattern`/`select_preset` change the
   *live* sound (loud!), `write_pattern` mutates *stored* memory; both are gated
   and clearly labelled.

## Build order

> **Status (implemented).** Steps 1–7 are shipped: both transports, the four
> codecs + value encodings, the request/reply session and engine USB API, the
> USB binding kind, the generic `usb_*` and semantic per-binding tools, the
> two-key write gate, USB-backed `verify_control`, and **patch-level scenes**
> (`capture_usb_patch` → a `scene.USBPatch` blob that `recall_scene` writes back,
> gated). The device **profiles** are authored in the bundled definitions: SL-2
> (full R/W), EQ2 (read + select), ML10X (bank read), an Opus torpedo-HID
> monitor-only placeholder (step 8), and the H90 documented as no-USB. Open
> per-device work: ML10X write opcodes, EQ2 per-parameter byte offsets, Opus
> value scaling — and hardware re-verification of the write paths.

1. **Finish `usbmidi` transport** (stub → real) + the request/reply session;
   port the SL-2 codec out of `cmd/usb-probe` into `internal/usbcodec/roland`.
2. **SL-2 read tools** (`sl2_read_system`/`read_patch`/`list_patterns`/`get_param`)
   — read-only, highest value, fully confirmed.
3. **USB binding kind** + `list_devices`/`describe_device` integration.
4. **SL-2 write tools** behind the write gate (`set_param`/`write_pattern`),
   with dry-run; first real write test on hardware.
5. **`usbhid` transport** + `neuro-hid` codec → **EQ2 read tools** (`list/read
   presets`), then `select_preset` (gated).
6. **ML10X read tools** (`morningstar-sysex` codec; read confirmed).
7. **`verify_control` USB readback** + **patch-level scenes**.
8. **Opus** Torpedo Remote codec (after a Mac/iPad HID capture) → tools.

## Open questions

- **Profile authoring:** hand-write the SL-2 param map in YAML, or generate it
  from the decompiled `address_map.js` at build time? (Lean: generate once into
  YAML, hand-curate names.)
- **Map granularity:** expose every one of the SL-2's ~hundreds of params, or a
  curated subset + raw `usb_read/write` for the rest? (Lean: curated semantic
  set + generic escape hatch.)
- **Concurrency:** USB-MIDI and BLE to the *same* pedal at once — any contention?
  (SL-2 has only USB; ML10X/EQ2 have both. Needs a quick live check.)
- **Endpoint stability:** ALSA card numbers shift across reboots; bind by **port
  name substring** / VID:PID (as the spike does), never the card index.

## References

- Research: `docs/research/usb.md` (capture tooling + descriptor inventory),
  `docs/research/sl-2.md` (Roland map + write flow), `docs/research/ml10x.md`
  (Morningstar TLV), `docs/research/eq-2.md` (Neuro HID map), `docs/research/h90.md`
  (the negative result).
- Proven codecs: `cmd/usb-probe` (`--device sl-2|ml10x|eq2|h90`).
- Extracted SL-2 editor reference (gitignored): `docs/private/sl2-bts/`.
