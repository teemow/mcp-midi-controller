# CME WIDI — configuration protocol (reverse-engineered from the WIDI App)

Research note on **how CME WIDI BLE-MIDI dongles are configured** (WIDI Master,
WIDI Jack, WIDI Bud Pro, WIDI Uhost, **WIDI Thru6 BT**, WIDI Core, WIDI K.O.II,
WIDIFLEX, plus OEM rebrands like Xvive MD1, Kurzweil AirMIDI, Korg BM-1). The
project uses a WIDI Thru6 as the multi-port BLE-MIDI hub (see `docs/design.md`),
so being able to read/write its settings from the daemon is directly relevant.

> **Verified live** against a real **WIDI Thru6 BT** (devID `0x12`) over the
> project's own `blemidi` transport (PipeWire/ALSA-seq data plane) — all
> `READ_SETTINGS`/`READ_STATUS` round-trips below were observed on the box. Only
> **reads** were issued; no writes, so the hub's settings/pairing were not
> changed.

## Headline result

**Configuration is entirely in-band MIDI SysEx**, sent over the **standard
BLE-MIDI GATT characteristic** — the same service/characteristic the design's
`blemidi` transport already uses:

- Service `03B80E5A-EDE8-4B33-A751-6CE34EC4C700`
- Characteristic `7772E5DB-3868-4112-A1A9-F2669D106BF3`

There is **no separate config GATT service**. (There is a separate TI OAD service
`F000FFC0-…`/`F000FFD0-…` and a CME bootloader UUID `0000FFC0-…` used only for
firmware update — out of scope here.) So a "configure WIDI" capability is just
**SysEx writes/reads on the characteristic the daemon already owns** — no new
transport plumbing needed.

## SysEx frame

All WIDI config messages share one envelope:

```
F0  <H1 H2 H3 H4>  <devID>  <cmd>  <data…>  <checksum>  F7
```


| Byte(s)    | Meaning                                                         |
| ---------- | --------------------------------------------------------------- |
| `F0`       | SysEx start (SOX)                                               |
| `H1..H4`   | 4-byte OEM header (manufacturer ID, see below)                  |
| `devID`    | per-product device id (see product table)                       |
| `cmd`      | command id, low nibble `cmd & 0x0F`; **replies set bit `0x40`** |
| `data…`    | command payload (see per-command sections)                      |
| `checksum` | `(cmd + Σ data) & 0x7F`                                         |
| `F7`       | SysEx end (EOX)                                                 |


The app identifies an inbound buffer as "a CME SysEx" when `len ≥ 6`, `buf[0] == F0`, bytes 1..4 match a known OEM header, **and `buf[6] & 0x40 == 0x40`** (the
reply bit). Requests sent by the host do **not** set `0x40`.

### OEM headers (`H1 H2 H3 H4`)


| OEM                                | Header bytes (hex) |
| ---------------------------------- | ------------------ |
| **CME** (all WIDI + most rebrands) | `00 20 63 0F`      |
| Korg                               | `42 30 00 01`      |
| TE (Teenage Engineering)           | `00 20 76 61`      |


`00 20 63` is CME's MIDI manufacturer SysEx ID; `0F` is a family/product-group
byte. For every WIDI device in this project the header is `00 20 63 0F`.

### Product `devID` (byte 5)

From the app's product table (`D1.a`). All use the CME header except where noted:


| Product           | devID (dec / hex)        |
| ----------------- | ------------------------ |
| WIDI Master       | 9 / `09`                 |
| WIDI Uhost        | 10 / `0A`                |
| WIDI Jack         | 11 / `0B`                |
| Xvive MD1         | 12 / `0C`                |
| Kurzweil AirMIDI  | 13 / `0D`                |
| WIDI Bud Pro      | 14 / `0E`                |
| WIDI Core         | 15 / `0F`                |
| **WIDI Thru6 BT** | **18 / `12`**            |
| MIDI Thru5 WC     | 24 / `18`                |
| WIDIFLEX USB      | 25 / `19`                |
| WIDIFLEX          | 27 / `1B`                |
| WIDI K.O.II       | 28 / `1C`                |
| Xkey Air 25 / 37  | 45 / `2D`, 46 / `2E`     |
| LIMEX BT          | 44 / `2C`                |
| Korg BM-1         | 112 / `70` (Korg header) |


## Commands (`cmd`)


| `cmd` | Name             | Payload                                      |
| ----- | ---------------- | -------------------------------------------- |
| 0     | `ACTION`         | `[actionId]`                                 |
| 1     | `READ_STATUS`    | `[statusId]`                                 |
| 2     | `READ_SETTINGS`  | `[regId, regLen]`                            |
| 3     | `WRITE_SETTINGS` | `[regId, count, <nibble-encoded bytes…>]`    |
| 4     | `EMC_TEST`       | (factory/EMC)                                |
| 5     | `ICSP_PIC`       | firmware/in-circuit programming (bootloader) |


### Nibble encoding (the important quirk)

SysEx data bytes must stay 7-bit, so payload bytes are **split into two nibbles,
low nibble first**:

```
byte b  ->  [ b & 0x0F , (b >> 4) & 0x0F ]      // "assignByteToBufferNibbles"
```

So 1 logical byte = 2 SysEx bytes. Reading reverses it: `low | (high << 4)`.
(There are also 5-nibble "word", 9-nibble "double word", and 3-nibble int16
encoders used for flash addresses/serial during firmware update — not needed for
settings.) Strings (device name) are encoded the same way, one char = 2 nibbles.

### `ACTION` (cmd 0) — device actions

`actionId` enum (`SYSX_CMD_ACTION`):


| id  | Action                               |
| --- | ------------------------------------ |
| 0   | DISCONNECT                           |
| 1   | REBOOT_BOOTLOADER                    |
| 2   | ACTIVATE                             |
| 3   | **RESET_FACTORY_DEFAULT**            |
| 4   | REBOOT_NORMAL                        |
| 5   | **ERASE_ALL_BONDS** (clear pairings) |
| 6   | UNACTIVATE                           |
| 7   | LOOP                                 |


Note: `ACTIVATE`/`UNACTIVATE` use a special checksum (an 8-byte token summed
in instead of the normal payload checksum) — licensing/activation, not normal
config.

### `READ_STATUS` (cmd 1) — read-only telemetry

`statusId` enum (`SYSX_CMD_STATUS` / `v1.h`):


| id  | Status                                                    |
| --- | --------------------------------------------------------- |
| 0   | SERIAL (serial number)                                    |
| 1   | BLE_ROLE (central/peripheral, actual)                     |
| 2   | BLE_STATE                                                 |
| 3   | BLE_CONNECTED_ADR (peer MAC)                              |
| 4   | CONNECTED_RSSI                                            |
| 5   | CONNECTION_INTERVAL                                       |
| 6   | MTU_SIZE                                                  |
| 7   | BLE_PHY (1M / 2M / coded)                                 |
| 8   | USB_IC (USB chip status — Bud Pro / Uhost / WIDIFLEX USB) |


Device firmware/hardware version + serial are also derivable from a status read
(`extractWidiInfo`: fw at nibble offset 0, hw at 8, 64-bit serial at 16).

### `READ_SETTINGS` (cmd 2) / `WRITE_SETTINGS` (cmd 3) — the config registers

This is the real configuration surface. Register enum (`v1.g`), with `regLen` =
number of logical bytes the register holds:


| regId | Register                   | len | Meaning / values                             |
| ----- | -------------------------- | --- | -------------------------------------------- |
| 0     | `BLE_NAME`                 | 32  | **Device name** (string, ≤15 chars written)  |
| 1     | `TX_POWER`                 | 1   | **BLE transmit power**, 15 steps (see below) |
| 2     | `BLE_PHY_SWITCH`           | 1   | PHY preference (1M/2M/coded)                 |
| 3     | `SCAN_DURATION`            | 1   | central scan window                          |
| 4     | `ADVERTISE_DURATION`       | 1   | peripheral advertise window                  |
| 5     | `POWER_SAVING`             | 1   | power-saving on/off                          |
| 6     | `FORCE_BLE_ROLE`           | 1   | **BLE role**: 0 = AUTO, 1 = PERIPHERAL       |
| 7     | `CONNECT_ADDRESS_1`        | 6   | **Group peer 1** BLE MAC (6 bytes)           |
| 8     | `CONNECT_ADDRESS_2`        | 6   | Group peer 2 MAC                             |
| 9     | `CONNECT_ADDRESS_3`        | 6   | Group peer 3 MAC                             |
| 10    | `CONNECT_ADDRESS_4`        | 6   | Group peer 4 MAC                             |
| 11    | `PREFER_LATENCY_JITTER`    | 1   | 0 = prefer latency, 1 = prefer jitter        |
| 12    | `INTERNAL_CLOCK_TEMPO_BPM` | 1   | internal MIDI-clock tempo (BPM)              |
| 13    | `INTERNAL_CLOCK_TEMPO_MS`  | 1   | internal clock tempo (ms)                    |
| 14    | `MIDI_IN_THRU`             | 1   | MIDI IN→THRU echo on/off                     |


**Write payload shape** (cmd 3): `[regId, count, <count logical bytes, each as 2 nibbles>]`. Examples observed in the app:

- **single byte** (`writeByteSettings`): `[regId, 0x01, low, high]`
- **name** (`writeStringRegister`): `[regId(0), strLen, <chars×2 nibbles>]`
- **6-byte MAC** (`writeReverseByteArraySettings`): `[regId, 0x06, <bytes reversed, each 2 nibbles>]` — the MAC is written **byte-reversed** (endianness
flip between display order and wire order).

**Read payload** (cmd 2): `[regId, regLen]`; the reply (with the `0x40` bit set
in `cmd`) carries `[regId, len, <nibble data>]`.

### Wireless MIDI groups = the four `CONNECT_ADDRESS` registers

The marquee "wireless MIDI group" feature (1-to-4 split / 4-to-1 merge) is
nothing more than **writing up to four peer BLE MAC addresses** into registers
7–10, plus setting the BLE role (reg 6) and latency/jitter preference (reg 11).

- **Clearing a group**: the app writes `FF FF FF FF FF FF` (all-ones, 6 bytes)
into each of registers 7,8,9,10 (`writeReverseByteArraySettings(reg, [255×6])`).
- **Building a group**: write the partner devices' MACs into the slots.
- Group membership is stored in the device's **default memory**, so it
re-pairs on power-up (matches CME's docs).

### `TX_POWER` value table

`regId 1` is an index into 15 levels (`Settings.TxPower`):

```
0:-20  1:-18  2:-15  3:-12  4:-10  5:-9  6:-6  7:-5
8:-3   9:0   10:+1  11:+2  12:+3  13:+4  14:+5   (dBm)
```

## Worked example

Read the device **name** from a WIDI Thru6 (devID 18 = `0x12`), register 0
(`BLE_NAME`, len 32):

```
F0 00 20 63 0F 12 02 00 20 <checksum> F7
            └OEM┘  │  │  │  └ regLen=32 (0x20)
                   │  │  └ regId=0 (BLE_NAME)
                   │  └ cmd=2 (READ_SETTINGS)
                   └ devID=0x12 (WIDI Thru6)
checksum = (0x02 + 0x00 + 0x20) & 0x7F = 0x22
```

Set BLE role to PERIPHERAL (reg 6, value 1) on the same device:

```
F0 00 20 63 0F 12 03 06 01 01 00 <checksum> F7
                   │  │  │  └──┴ value 1 -> nibbles 01 00
                   │  │  └ count=1
                   │  └ regId=6 (FORCE_BLE_ROLE)
                   └ cmd=3 (WRITE_SETTINGS)
checksum = (0x03 + 0x06 + 0x01 + 0x01 + 0x00) & 0x7F = 0x0B
```

## Verified live (WIDI Thru6 BT, devID 0x12)

Probed via the project's `blemidi` transport (PipeWire bridges the dongle to an
ALSA-seq client named after its BLE name). Reads only. Observed reply bytes
confirm — the concrete endpoint addresses/names for this rig live in
`docs/private/` (gitignored), per the public-repo rule:

**Framing & reply marker — confirmed.** `F0 00 20 63 0F 12 <cmd> <data> <ck> F7`
with `ck = (cmd + Σdata) & 0x7F` is accepted; the device echoes the command with
**bit `0x40` set** (`0x42` = READ_SETTINGS reply, `0x41` = READ_STATUS reply).

**READ_SETTINGS reply shape — confirmed:** `… <regId> <count> <2·count nibble bytes, low-first> <checksum> F7`. The `count` byte is the number of **logical
bytes**; the wire carries **two nibbles per byte**.


| Register                    | Reply (raw)     | Decoded                                |
| --------------------------- | --------------- | -------------------------------------- |
| TX_POWER (1)                | `…01 01 0E 00…` | `0x0E` = 14 → **+5 dBm** (max)         |
| POWER_SAVING (5)            | `…05 01 01 00…` | `1` = on                               |
| FORCE_BLE_ROLE (6)          | `…06 01 00 00…` | `0` = **AUTO**                         |
| CONNECT_ADDRESS_1..4 (7–10) | `…06 0F·12…`    | `FF FF FF FF FF FF` = **no group set** |
| PREFER_LATENCY_JITTER (11)  | `…0B 01 00 00…` | `0` = **prefer latency**               |
| MIDI_IN_THRU (14)           | `…0E 01 01 00…` | `1` = on                               |


So the four `CONNECT_ADDRESS` slots reading `FF×6` confirms both the **group
mechanism** and that `**FF×6` is the empty/cleared sentinel**.

**Error replies — confirmed.** The 10-byte error form decodes exactly:
observed `0x7C` SYSEX_PARAM_OUT_OF_RANGE, `0x7F` SYSX_UNKNOWN_PARAMETER, `0x7E`
SYSEX_WRONG_CHECKSUM.

**Encoding nuance (important).** The simple low-nibble/high-nibble byte split is
only used by the enum/byte registers (TX power, role, on/off flags). The
duration `_RC` registers return a **second byte > 0x0F** (e.g. SCAN_DURATION →
`02 3E`, ADVERTISE_DURATION → `00 7D`), i.e. they use the app's wider **7-bit
"word"** encoding, not 4-bit nibbles. Decode those with the word/int16 readers,
not the byte-nibble reader.

**Per-product support (Thru6).** Not every register exists on every product:

- `BLE_NAME` read (reg 0, len 32) → **OUT_OF_RANGE**. The app reads the name from
the **BLE GAP device name** (the advertised name) and only *writes* it via
reg 0 — so don't rely on reading reg 0 to get the name.
- `BLE_PHY_SWITCH` (reg 2), `INTERNAL_CLOCK_TEMPO_BPM/MS` (reg 12/13) →
**UNKNOWN_PARAMETER** (not supported on the Thru6).
- `READ_STATUS SERIAL` (id 0) returns the fw/hw/serial nibble blob; the other
status ids tried (BLE_ROLE/STATE/PHY) returned a (misleading) WRONG_CHECKSUM on
this firmware. Treat status-id support as product/firmware-specific.

**Writes — confirmed** (on a **WIDI Master**, devID `0x09`, a non-critical test
device). A reversible `MIDI_IN_THRU` toggle round-tripped cleanly:

```
READ  MIDI_IN_THRU            -> 0 (off)
WRITE MIDI_IN_THRU = 1   F0 00 20 63 0F 09 03 0E 01 01 00 13 F7
  WRITE-ACK reply            F0 00 20 63 0F 09 43 0E 01 00 52 F7   (cmd 0x43)
READ  MIDI_IN_THRU            -> 1 (on)      ✓ write applied
WRITE MIDI_IN_THRU = 0   F0 00 20 63 0F 09 03 0E 01 00 00 12 F7   (restore)
READ  MIDI_IN_THRU            -> 0 (off)      ✓ restored
```

So `WRITE_SETTINGS` for a single-byte register (`[regId, 01, low, high]`, ck =
`(3 + regId + 01 + low + high) & 0x7F`) is correct, and the device returns a
**WRITE-ACK** with the command echoed as `0x43` (`WRITE_SETTINGS | 0x40`),
`regId`, count `01`, then a status/checksum tail.

**Master vs Thru6 stored config** (same protocol, different defaults): the Master
read `FORCE_BLE_ROLE = 1` (PERIPHERAL) and `MIDI_IN_THRU = 0`; the Thru6 read
role `AUTO` and thru `on`.

**WIDI Jack (devID `0x0B`) — confirmed live** (reads only). The full
`READ_SETTINGS`/`READ_STATUS` sweep round-tripped exactly like the Thru6, which
verifies `devID 0x0B` and the shared register map on real Jack hardware:

- Same supported/unsupported set as the Thru6: `BLE_PHY_SWITCH` (reg 2) and
`INTERNAL_CLOCK_TEMPO_BPM/MS` (reg 12/13) return `UNKNOWN_PARAMETER`.
- Same `_RC` word encoding on the durations (`SCAN_DURATION` → `02 3E`,
`ADVERTISE_DURATION` → `00 7D`) and the same status-id firmware quirk
(`READ_STATUS` BLE_ROLE/STATE/PHY → `WRONG_CHECKSUM`; SERIAL returns the
fw/hw/serial blob).
- Stored config observed: `TX_POWER` +5 dBm, `FORCE_BLE_ROLE = 1` (PERIPHERAL,
like the Master — a leaf transmitter), `POWER_SAVING` on, `PREFER_LATENCY`,
`MIDI_IN_THRU` on, all four `CONNECT_ADDRESS` slots `FF×6` (no group).

**Still not verified:** `ACTION` commands (reboot/factory-reset/erase-bonds),
multi-byte writes (the `CONNECT_ADDRESS` group registers), and the exact
unit/encoding of the duration `_RC` registers. Group/role/`ACTION` writes change
pairing/behaviour — confirm on a sacrificial device.

> The probe used for this verification lives at `cmd/widi-probe/` (build-order
> step 1 "BLE spike"; `--write-test` runs the reversible toggle). Not part of the
> shipped daemon.

## Implications for the project

- **No new transport work.** WIDI config rides the existing BLE-MIDI
characteristic. The daemon can read/write WIDI settings by emitting these
SysEx byte strings through the same write path used for CC/PC, and capturing
the `0x40`-flagged reply on the inbound notify channel (the same channel that
powers MIDI-learn).
- **Fits the YAML/SysEx model.** Most of this is expressible as a device
definition with `type: sysex` controls (name, tx_power, ble_role,
latency/jitter, midi_thru, internal clock). The nibble-split + checksum +
`F0 00 20 63 0F <devID> <cmd>` framing is the templated part — either a small
`widi`-aware helper or a parametric SysEx control template.
- **Group setup as a first-class action.** Writing the four `CONNECT_ADDRESS`
registers (+ role + latency/jitter) is the "make a wireless MIDI group"
operation; clearing them (FF×6) dissolves it. This pairs naturally with
`discover_endpoints` (the MACs to write are just the peers' BLE addresses).
- **Caveat:** the MAC is written **byte-reversed** and all multi-byte values are
**nibble-split low-first** — easy to get wrong; verify against a live Thru6
before trusting writes (especially group/role, which change pairing behaviour).
- Firmware update (`ICSP_PIC` cmd 5 + TI OAD service) and `ACTIVATE`/`UNACTIVATE`
licensing are intentionally **out of scope** for this server.

## Implementation — the `internal/widi` library + MCP tools

**Status: implemented.** WIDI config is handled by a dedicated, transport-agnostic
library and a set of MCP tools, *not* by the YAML control model. The draft
`internal/device/definitions/widi-master.yaml` has been **removed** — it was
fire-and-forget only, while WIDI config is fundamentally request/reply (reads,
read-back-verified writes) and includes multi-register, byte-reversed-MAC group
writes that the single-value `sysex` control template cannot express.

- `internal/widi` — pure protocol: SysEx framing + checksum, nibble codec,
  reply decoding (settings/write-ack/status/error), the product table (devID per
  product), the register/status enums, the TX-power table, and a friendly
  writable-settings vocabulary (`ble_role=peripheral`, `tx_power=+5`, …). Unit
  tested against the captured byte vectors in this doc.
- `internal/engine/widi.go` — request/reply orchestration over the BLE-MIDI
  transport the engine already owns. It reuses the **single** inbound listener +
  subscriber fan-out (`StartInbound`/`subscribe`) so it never opens a second
  `Listen` on an endpoint (the ALSA data plane allows only one) and coexists with
  a bound dongle's normal traffic. Matches replies by `(devID, cmd|0x40)`.
- `internal/mcpserver/widi_tools.go` — `widi_read_config`, `widi_write_setting`,
  `widi_set_group`, `widi_clear_group`, addressed by `endpoint` + `product`
  (or raw `devid`). Settings stay **out** of the scene/desired-state path.

### Historical note — the YAML-rendering alternative (not taken)

The original plan was to render WIDI writes from YAML `sysex` controls by
extending `renderSysEx` (which only substitutes a single value byte, `%v`,
0..127). That would have needed two things the template can't express today:

1. **Extend the sysex template** with nibble + checksum tokens (useful for any
  checksummed-SysEx device, not just WIDI):
  - `%n` → `value & 0x0F` (low nibble), `%N` → `(value >> 4) & 0x0F` (high nibble)
  - `[` → start-checksum marker (consumed, not emitted)
  - `%c` → `(sum of bytes emitted since "[") & 0x7F`
  - allow value 0..255 when the template nibble-splits it (the 0..127 cap is a
  single-byte-payload assumption).
   This makes `F0 00 20 63 0F 09 [ 03 0E 01 %n %N %c F7` render exactly the bytes
   the live write test used.
2. **Per-product devID.** Every WIDI product shares the register map but has its
  own devID (Master `0x09`, Jack `0x0B`, Bud Pro `0x0E`, Thru6 `0x12`, …),
   which sits at header byte 5. Options, cheapest first:
  - one definition file per product (devID baked into each template) — simplest,
  ~16 near-identical files;
  - a definition-level `device_id` field substituted into templates via a `%d`
  token — one `widi.yaml` for the whole family.

The remaining two pieces — both now **done** in `internal/engine/widi.go` +
`internal/mcpserver/widi_tools.go` — were:

1. **Read/round-trip helper** (`widiRequest` + `ReadWIDIConfig`). Config reads
   are request/reply: it sends the read and matches the reply on the engine's
   shared inbound stream by `(devID, cmd|0x40)` with a timeout. The reply shape
   is verified: `… regId count <2·count nibbles, low-first> checksum F7`; errors
   arrive as the 10-byte form (decoded by `widi.Decode`).
2. **Group tools** (`widi_set_group` / `widi_clear_group`). They write the four
   `CONNECT_ADDRESS` registers (byte-reversed 6-byte MACs; `FF×6` clears), plus
   optional role/preference. Peers come from `discover_endpoints`. Kept out of
   the plain control surface because it is a multi-register, pairing-changing
   action.

Settings are persistent flash config, semantically distinct from performance
state, so they live in their own read/write tools and stay **out of the scene
snapshot/recall path**.

## Sources

- **Live probe** — a WIDI Thru6 BT on the LAN's BLE, read via `cmd/widi-probe`
over the project's `blemidi` transport. The reply bytes in "Verified live"
were observed directly.
- CME WIDI App start guide — [https://www.cme-pro.com/cme-widi-app-online-start-guide/](https://www.cme-pro.com/cme-widi-app-online-start-guide/)
- CME WIDI support — [https://www.cme-pro.com/support-product/widi/](https://www.cme-pro.com/support-product/widi/)
- BLE-MIDI service/characteristic UUIDs — see `docs/design.md` "Protocols & libraries".

