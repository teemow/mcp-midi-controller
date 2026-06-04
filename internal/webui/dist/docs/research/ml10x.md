# Morningstar ML10X — Incoming MIDI control research

Research note for the device definition at
`internal/device/definitions/ml10x.yaml`. Scope: what the ML10X **responds to**
over MIDI so this MCP server can control/configure it. The ML10X is a
MIDI-controlled reorderable loop switcher (10 loops over 5 TRS send/return
ports). It is also a live foot controller, but its *outgoing* foot-controller
messages are out of scope here.

Primary source (all values below come from this page unless noted), the ML10X
User Manual "MIDI Implementation" section, **updated 22/12/2025**:
<https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>

## Banks & presets

- 4 banks, 128 presets each; preset numbers range **0-127**.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#banks-and-presets>

## MIDI ports & transport

- One 5-pin MIDI IN, one 5-pin MIDI THRU, and one USB-C port.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#midi-ports>
- This project reaches the device over **BLE-MIDI** (via the rig's WIDI hub on
  the DIN chain), so `transport: blemidi` in the definition. The manual itself
  documents the wired DIN / USB ports; the MIDI message semantics are identical
  regardless of physical transport.

## MIDI channel behaviour

- MIDI channel is set on-device: **Menu > Global Settings > Edit MIDI Channel**.
  The device acts on incoming messages matching its configured channel.
- The device can be set to **ignore all incoming MIDI** from the same menu, in
  which case none of the messages below have any effect.
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#setting-midi-channel>
- The channel is **not** stored in the device definition (per the project's
  binding model); it is supplied by the binding.

## Program Change (preset recall)

| Function | Message | Value | Notes |
|----------|---------|-------|-------|
| Recall preset | Program Change | PC 0-127 → Preset 0-127 | Recalls within the **current** bank. To recall a preset in another bank, send the `change_bank` CC **first**, then the PC. |

Source: <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>
(MIDI Implementation › Program Change messages).

> Note: ordering matters for cross-bank recall (bank-select CC before PC). The
> engine's scene recall already sends program-change-then-CC; for an explicit
> cross-bank jump, set `change_bank` then `preset`.

## Control Change messages

All rows below are from the manual's "Control Change Messages" table. Loop
engage/bypass/toggle messages **only affect Simple-mode presets** (in Advanced
mode, loops cannot be bypassed via CC). Source for every row:
<https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#3-control-change-messages>

| Function | CC# | Value (per manual) | Modelled `value` in YAML |
|----------|-----|--------------------|--------------------------|
| Change Bank | 0 | 0-3 | enum bank_1=0, bank_2=1, bank_3=2, bank_4=3 |
| Engage/Bypass all loops (Simple only) | 4 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Scroll Up | 5 | any | enum trigger=127 |
| Scroll Down | 6 | any | enum trigger=127 |
| Mute | 7 | any | enum trigger=127 |
| Unmute | 8 | any | enum trigger=127 |
| Toggle Mute/Unmute | 9 | any | enum trigger=127 |
| Engage/Bypass Loop A Tip (Simple only) | 10 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop A Ring (Simple only) | 11 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop B Tip (Simple only) | 12 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop B Ring (Simple only) | 13 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop C Tip (Simple only) | 14 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop C Ring (Simple only) | 15 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop D Tip (Simple only) | 16 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop D Ring (Simple only) | 17 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop E Tip (Simple only) | 18 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Engage/Bypass Loop E Ring (Simple only) | 19 | 0-63 Bypass, 64-127 Engage | enum bypass=0, engage=127 |
| Toggle Loop A Tip (Simple only) | 20 | 0-127 | enum trigger=127 |
| Toggle Loop A Ring (Simple only) | 21 | 0-127 | enum trigger=127 |
| Toggle Loop B Tip (Simple only) | 22 | 0-127 | enum trigger=127 |
| Toggle Loop B Ring (Simple only) | 23 | 0-127 | enum trigger=127 |
| Toggle Loop C Tip (Simple only) | 24 | 0-127 | enum trigger=127 |
| Toggle Loop C Ring (Simple only) | 25 | 0-127 | enum trigger=127 |
| Toggle Loop D Tip (Simple only) | 26 | 0-127 | enum trigger=127 |
| Toggle Loop D Ring (Simple only) | 27 | 0-127 | enum trigger=127 |
| Toggle Loop E Tip (Simple only) | 28 | 0-127 | enum trigger=127 |
| Toggle Loop E Ring (Simple only) | 29 | 0-127 | enum trigger=127 |

### Modelling decisions

- **"any" value triggers** (scroll, mute/unmute/toggle-mute, loop toggles): the
  manual accepts any value 0-127, so the YAML uses an `enum` with a single
  `trigger: 127` label. Any non-momentary value would also work; 127 is the
  conventional momentary trigger and keeps these controls discoverable.
- **Engage/bypass loops & "all loops"**: modelled as `enum {bypass: 0,
  engage: 127}`. The manual splits the value range at 64 (0-63 bypass / 64-127
  engage); the two enum labels pick representative values on each side.
- **Change Bank**: modelled as a 4-entry enum (bank_1..bank_4 → 0..3). Labels
  are 1-based for human readability while wire values stay 0-based per the
  manual. Banks are numbered 0-3 on the wire.

## Out of scope / not modelled

- **ML10X Message Type (SysEx)** — a dedicated Morningstar-controller message
  type (Set/Engage/Bypass/Toggle Selected Loops, Scroll Up/Down, Select Preset)
  that engages loops without CC and addresses a per-device **Device ID** (or
  Omni). Not modelled here. The manual documents the *functions* but **not the
  SysEx byte layout**, and what is reverse-engineered in the "USB readback"
  section below is the **editor read** protocol (op1 `0x00`/`0x01`/`0x06`,
  read op2s `0x00 0x01 0x12 0x13 0x15 0x16 0x17 0x18`), **not** these controller
  *write* messages. To model these precisely the following must be confirmed
  (do **not** guess them into a committed control):
  - the **op1/op2** opcode pair for each of the six message types;
  - the **10-loop selection bitmask** encoding (which byte(s), bit order, and
    how "selected vs unselected" is represented for Set vs Engage/Bypass/Toggle);
  - the **Device-ID byte position** (and the Omni encoding);
  - for Select Preset, how **bank + preset** are packed.
  - **Engine constraint:** `renderSysEx`'s `%v` substitutes a single 0–127 byte,
    so a 10-bit loop mask cannot be expressed with one `%v` — it would need two
    literal-derived bytes or a small engine extension (e.g. a multi-byte / masked
    value token). Evaluate before modelling.

  The safe path is a **capture**: emit each ML10X Message Type from the
  Morningstar editor / an MC-series controller while sniffing the wire, diff
  against the known `F0 00 21 24 07 00 …` framing + XOR&0x7F checksum, then model
  precisely. The single-loop CC controls (CC 4, 10–29) already cover most needs;
  the real adds would be **Set Loops** (atomic 10-loop mask) and **Select Preset**
  (bank+preset in one message, vs `change_bank` then PC).
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#ml10x-message-type-for-morningstar-midi-controllers>
- **No expression / continuous-controller input** is documented for the ML10X
  in the manual's MIDI Implementation table (it is a loop switcher, not an
  expression target), so no expression control is included.

## Caveats

- Loop engage/bypass/toggle (CC 4, 10-29) **only work on Simple-mode presets**.
  In Advanced mode, loops cannot be bypassed via CC (subject to a beta firmware
  noted in the manual).
  <https://help.morningstar.io/en/article/ml10x-user-manual-262ann/#advanced-presets>
- Cross-bank preset recall requires `change_bank` **before** the PC.
- If the device's MIDI channel is set to ignore incoming MIDI, no message takes
  effect.
- Table reflects firmware/manual revision dated **22/12/2025**; re-verify
  against the live manual if firmware changes.

## USB readback — Morningstar editor protocol (USB-MIDI SysEx)

Research for the USB readback phase (see `docs/research/usb.md`): how to read the
ML10X's real config back so a later phase can verify what BLE-MIDI writes landed.
This is the **editor protocol**, distinct from the CC/PC control surface above.

> **Reverse-engineered from a live capture** of the Morningstar web editor's
> "read from device" against the rig's ML10X, plus an independent round-trip via
> `cmd/usb-probe`. Read-only (no write/save opcodes were issued). The
> installation-specific values that came back (loop/preset names) live in
> `docs/private/rig.md`; only the generic protocol is here.

### Headline correction — it's USB-MIDI, not Web Serial / not CDC

The ML10X exposes **two** USB data channels (see the descriptor inventory in
`docs/research/usb.md`): a **CDC-ACM serial** port (`/dev/ttyACM0`) and a
**USB-MIDI** interface. Despite the CDC port existing, the editor's config
read/write rides the **USB-MIDI interface** (bulk EP `0x04` OUT / `0x84` IN,
cable 0), carrying SysEx packed into USB-MIDI event packets. Throughout a full
editor "read from device", the **CDC serial endpoints stayed silent**. So:

- The editor uses **Web MIDI**, not Web Serial, for the ML10X.
- Readback tooling should talk to the ALSA rawmidi port (e.g. `hw:4,0,0`), the
  same musical-MIDI interface — *not* `/dev/ttyACM0`.
- This is **not** the documented MC-series "external SysEx API"
  (`F0 00 21 24 <model> 00 70 …`, op1 `0x70`); the ML10X uses its own framing
  (op1 `0x00`) and a model id (`0x07`) absent from the MC table. The MC framing
  is silently ignored by the ML10X (confirmed: a model-id sweep with op1 `0x70`
  over both USB-MIDI and CDC got no reply).

### Frame format (confirmed)

```
F0 00 21 24 07 00 <op1> <op2> <payload…> <cksum> F7
   └ 00 21 24 ┘  │  │     │
   Morningstar   │  │     └ op2: specific opcode / block subtype
   mfg id        │  └ op1 message class: 00=request, 01=status, 06=data block
                 └ model id 0x07 = ML10X  (byte after = 0x00, reserved)
cksum = XOR(every byte from F0 up to but not incl. cksum) & 0x7F
```

The checksum is the **same XOR&0x7F** as Morningstar's MC external SysEx API; it
verified on every captured frame. A **read request** is the fixed 18-byte form
`F0 00 21 24 07 00 00 <op2> 00 00 00 00 00 00 00 00 <cksum> F7` (op1=`0x00`,
eight zero payload bytes).

### Read opcodes (op2) used by the editor's full read

A full "read from device" issues this set of read-only requests; the device
streams the whole bank/preset config back as a burst of reply frames:

```
0x00 0x01 0x12 0x13 0x15 0x16 0x17 0x18
```

Single-stepping each opcode through `cmd/usb-probe` (one request at a time, so
the device can't pipeline) split them into **two distinct kinds** — this is the
key follow-up result, and explains why a passive capture can't attribute the
blocks 1:1:

| op2 | Kind | Reply (isolated request) |
|-----|------|--------------------------|
| `0x00`, `0x01` | **handshake / identify** | STATUS frame (op1=`0x01`, op2 `0x00`/`0x04`); a reply with model `0x07` confirms an ML10X. |
| `0x16` | **addressable read** (bank-selected) | one **`06/00` preset block** for the selected bank — see the selector below. |
| `0x12`, `0x13`, `0x15`, `0x17`, `0x18` | **session-only bulk read** | **no reply** to an isolated request (even with a bank byte, even after the `0x00`/`0x01` handshake). |

The bulk blocks (`06/01` loop-name table, `06/02` 128-preset-name table, and the
`op2=0x07` iteration stream) are emitted **only inside a stateful editor read
session** driven by these `0x12…0x18` opcodes; they are **not independently
addressable** with a single request. So a verification reader can rely on `0x16`
(deterministic, bank-addressed) but reproducing the full bulk dump needs the
editor's session handshake (still to be isolated — see "Still open").

### Request payload — bank selector (confirmed)

The 8 payload bytes are a parameter block. For the addressable read `op2=0x16`:

- **byte 0 (frame index 8) = bank index**. Values `0x00–0x03` each return that
  bank's preset block; `0x04–0x07` get **no reply** — i.e. the device has exactly
  **4 banks**, which pins byte 0 as the bank selector (a preset index would span
  0–127).
- **byte 1 has no observable effect** (preset within a bank is *not* selected
  this way; per-preset content comes from the bulk session's `06/02` table +
  `0x07` stream).
- The returned `06/00` block depends on the preset's mode: a **simple** preset
  (e.g. bank 0's active preset) carries name + flags + the 16 message slots; an
  **advanced/empty** preset additionally carries the **routing-node graph** (ids
  `0x00–0x0B`, each a 3-byte `<node> 7F 00` link record) plus extra flags
  (`0x10–0x12`).

### Reply blocks — TLV structure (confirmed)

Data blocks (op1=`0x06`) carry a sequence of **TLV records**:

```
7F <field-id> <len> <value…>     (repeated; len is one byte, value is len bytes)
```

Three data-block shapes were observed (field *values* are rig-specific →
`docs/private/`; the *layout* is generic):

| Block (op1/op2) | ~len | Contents |
|-----------------|------|----------|
| `06 / 01` | ~281 | **Loop/port name table** (bulk session): ids `0x00–0x09` = the 10 loop port long names (16 bytes, space-padded ASCII); ids `0x30–0x39` = the matching 4-char short labels; ids `0x20–0x22` = small bank/device settings (1–2 bytes). |
| `06 / 00` | 131–209 | **One preset definition** (the `op2=0x16` reply): id `0x20` = preset long name (12 bytes ASCII); ids `0x30–0x3F` = the 16 message slots (3 bytes each); flag fields (`0x02`, `0x03`, `0x14`). For an **advanced** preset, also ids `0x00–0x0B` = routing-node graph (`<node> 7F 00`) and `0x10–0x12` flags (hence the larger length). |
| `06 / 02` | ~1928 | **Bank preset-name table** (bulk session): 128 records `0x00–0x7F`, each a 12-byte preset name — i.e. all 128 preset names in a bank. |

Status frames (op1=`0x01`) are 8-byte payloads: op2 `0x04/0x05/0x06/0x00`
bracket a dump (start/handshake/end markers), and a run of op2=`0x07` frames with
an **incrementing index** streams during the dump (inferred: a per-preset
progress/iterator marker; 128 of them per bank).

### The "full-config read"

There is **no single full-dump opcode**: a complete read = issue the eight read
requests and reassemble the streamed blocks. For verification purposes the
useful confirmed reads are:

- **identify / presence**: `op2=0x00` or `0x01` → STATUS reply with model `0x07`.
  Deterministic, no session needed.
- **a bank's active preset** (long name + 16 message slots, + routing graph for
  advanced presets): `op2=0x16` with byte 0 = bank `0x00–0x03` → block `06/00`.
  Deterministic, no session needed.
- **loop names** (`06/01`), **all 128 preset names in a bank** (`06/02`), and the
  per-preset iteration stream (`op2=0x07`): only inside the editor's **bulk read
  session** (the `0x12…0x18` opcodes); not reproducible with a single request.

A full backup repeats the burst per bank (the ML10X has 4 banks × 128 presets).

### Still open / not verified

- The **bulk-session handshake**: which exact request (and payload) gates the
  `0x12/0x13/0x15/0x17/0x18` opcodes into streaming `06/01`/`06/02`/`0x07`. They
  are silent to isolated requests and to the `0x00`/`0x01` handshake alone, so
  the editor sets some additional state first. Isolate it by capturing a
  **single, minimal** editor action (e.g. "read one bank") instead of a full
  backup. Until then, use `0x16` (bank-addressed) for deterministic readback.
- How a **specific preset 1–127** within a bank is read directly (byte 1 of the
  `0x16` payload does not do it; likely the bulk `0x07` iteration only).
- The 3-byte message-slot encoding (block `06/00`, ids `0x30–0x3F`), the routing
  node-link encoding (ids `0x00–0x0B`), and the small settings fields
  (`0x20–0x22` in block `06/01`).
- **Writes**: assumed to be op1=`0x00` with a write op2 + a save flag (mirroring
  the MC API's `7F`=save), but **not tested** — strictly out of scope for the
  read-only readback phase.

> Tooling: `cmd/usb-probe` (the USB counterpart of `cmd/widi-probe`) speaks this
> protocol over ALSA rawmidi. `--port ML10X` does the identify pass; `--full`
> sweeps every read opcode; `--op 0x16 --payload "<bank>"` does a single isolated
> request with a custom payload (how the bank selector above was found). The
> ML10X's USB-MIDI port is exclusive-open, so the web editor must be disconnected
> first.
