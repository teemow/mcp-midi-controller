# Two Notes Opus — MIDI research note

Research for the device definition `internal/device/definitions/opus.yaml`.

## Sources

- Primary — **MIDI Chart**: <https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual#midi_chart>
  (User's Manual → Specifications → "2. MIDI Chart"). This is the authoritative
  per-CC table and the source for every CC#, range and enum mapping below.
- **MIDI behaviour** (PC/CC receive, channel/omni): User's Manual → Setup manager
  → "5. MIDI": <https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual>
- **Signal chain & parameter descriptions** (EQ bands, Enhancer, Reverb, TSM amps,
  preset count): User's Manual → "Configuring Your Tone With OPUS" and "Creating a
  Preset": <https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual>

All values below come from the single MIDI Chart URL unless noted otherwise.

## Preset recall (Program Change)

- The Opus "has been specified to accept both preset change commands (Program
  Change or PC) and parameter change commands (Control Change or CC)" — Setup
  manager / MIDI section.
- There are **99 internal preset slots** ("99 memory slots for presets in OPUS",
  Creating a Preset → Saving The Preset; also Specifications → Memory).
- Program Change recalls a preset. PC value 0 → preset #1 (consistent with the
  CC#68 "Preset" mapping `0 = Preset #1`). Modeled as PC range **0–98**.
- **No Bank Select (CC0/CC32) is documented.** Note CC#0 is *not* Bank Select on
  the Opus — it is the "Preset mode" parameter (DynIR Engine vs IR loader). Do not
  send Bank Select to this device.
- Presets can also be driven over CC: **CC#68** select (0–99), **CC#70** preset
  down, **CC#71** preset up.

## Default MIDI channel behaviour

- The receive channel is configured on the unit (Setup manager → MIDI). The user
  can also set it to receive **all channels (omni)** — "you can choose to receive
  all channels, useful if you don't know which channel the commands are
  originating from".
- PC and/or CC reception can each be enabled/disabled (e.g. CC receive can be set
  Off if only using preset switching). The manual does not publish a fixed
  factory-default channel number, so the channel is left to the binding
  (`(endpoint, channel)`), as per this project's model.

## Full control table

On/off and mode switches use **0/1** wire values (not 0/127). CC#, naming, ranges
and enum mappings are taken verbatim from the MIDI Chart.

| CC# | Block | Parameter | Range | Mapping | YAML control |
|-----|-------|-----------|-------|---------|--------------|
| —   | Preset | Program Change (preset recall) | 0–98 | PC 0 = preset #1 | `preset` |
| 0   | Preset mode | Cab source mode | 0–1 | 0 = DynIR Engine; 1 = IR loader | `preset_mode` |
| 1   | Noise Gate | On/Off | 0–1 | 0 = Off; 1 = On | `noise_gate` |
| 2   | Noise Gate | Mode | 0–1 | 0 = Soft; 1 = Hard | `noise_gate_mode` |
| 3   | Noise Gate | Threshold | 0–80 | 0 = -80 dB; 80 = 0 dB | `noise_gate_threshold` |
| 4   | Preamp | On/Off | 0–1 | 0 = Off; 1 = On | `preamp` |
| 5   | Preamp | Model | 0–9 | 0 Foundry; 1 Foxy; 2 Albion; 3 NiftyFifty; 4 Peggy; 5 Tanger; 6 Eldorado; 7 Aviator; 8 Gemini; 9 Flatback | `preamp_model` |
| 6   | Preamp | Gain | 0–127 | 0–100% | `preamp_gain` |
| 7   | Preamp | Treble | 0–127 | 0–100% | `preamp_treble` |
| 8   | Preamp | Middle | 0–127 | 0–100% | `preamp_middle` |
| 9   | Preamp | Bass | 0–127 | 0–100% | `preamp_bass` |
| 10  | Power Amp | On/Off | 0–1 | 0 = Off; 1 = On | `power_amp` |
| 11  | Power Amp | Model | 0–7 | 0 = SE 6L6; 1 = SE EL34; 2 = SE EL84 … (chart truncated) | `power_amp_model` |
| 12  | Power Amp | Volume | 0–127 | 0–100% | `power_amp_volume` |
| 13  | Power Amp | Contour (PP models only) | 0–127 | 0–100%; 50% = bypassed | `power_amp_contour` |
| 14  | Power Amp | Depth | 0–127 | 0–100% | `power_amp_depth` |
| 15  | Power Amp | Type | 0–1 | 0 = Triode; 1 = Pentode | `power_amp_type` |
| 16  | Miking (DynIR only) | On/Off | 0–1 | 0 = Off; 1 = On | `miking` |
| 17  | Miking | Virtual Cabinet | 0–x | 0 = DynIR #0 … | `virtual_cabinet` |
| 18  | Miking | Mic A: Model | 0–7 | 0 = Mic #1 … | `mic_a_model` |
| 19  | Miking | Mic B: Model | 0–7 | 0 = Mic #1 … | `mic_b_model` |
| 20  | Miking (both modes) | Mic A: Level | 0–127 | 0–100% | `mic_a_level` |
| 21  | Miking | Mic A: Bypass | 0–1 | 0 = Off; 1 = On | `mic_a_bypass` |
| 22  | Miking | Mic A: Mute | 0–1 | 0 = Off; 1 = On | `mic_a_mute` |
| 23  | Miking | Mic A: Phase | 0–1 | 0 = Normal; 1 = Invert | `mic_a_phase` |
| 24  | Miking | Mic B: Level | 0–127 | 0–100% | `mic_b_level` |
| 25  | Miking | Mic B: Bypass | 0–1 | 0 = Off; 1 = On | `mic_b_bypass` |
| 26  | Miking | Mic B: Mute | 0–1 | 0 = Off; 1 = On | `mic_b_mute` |
| 27  | Miking | Mic B: Phase | 0–1 | 0 = Off/Normal; 1 = On/Invert | `mic_b_phase` |
| 28  | Miking (DynIR only) | Mic A: Axis | 0–127 | 0–100% | `mic_a_axis` |
| 29  | Miking (DynIR only) | Mic A: Distance | 0–127 | 0–100% | `mic_a_distance` |
| 30  | Miking (DynIR only) | Mic A: Position | 0–1 | 0 = Front; 1 = Back | `mic_a_position` |
| 31  | Miking (DynIR only) | Mic B: Axis | 0–127 | 0–100% | `mic_b_axis` |
| 32  | Miking (DynIR only) | Mic B: Distance | 0–127 | 0–100% | `mic_b_distance` |
| 33  | Miking (DynIR only) | Mic B: Position | 0–1 | 0 = Front; 1 = Back | `mic_b_position` |
| 34  | IR Loader | IR File A | 0–x | 0 = File #0 … | `ir_file_a` |
| 35  | IR Loader | IR File B | 0–x | 0 = File #0 … | `ir_file_b` |
| 36  | IR Loader | IR Folder A | 0–3 | 0 = User 0 … 3 = User 3 | `ir_folder_a` |
| 37  | IR Loader | IR Folder B | 0–3 | 0 = User 0 … 3 = User 3 | `ir_folder_b` |
| 38  | EQ | On/Off | 0–1 | 0 = Off; 1 = On | `eq` |
| 39  | EQ | Mode | 0–2 | 0 = Guitar; 1 = Bass; 2 = Custom | `eq_mode` |
| 40  | EQ (Custom only) | Freq: Low Cut | 0–127 | maps to Hz | `eq_freq_low_cut` |
| 41  | EQ | Gain: Low | 0–30 | 0 = -15 dB; 15 = 0 dB; 30 = +15 dB | `eq_gain_low` |
| 42  | EQ (Custom only) | Freq: Low | 0–127 | maps to Hz | `eq_freq_low` |
| 43  | EQ | Gain: Low Mid | 0–30 | 0 = -15 dB; 15 = 0 dB; 30 = +15 dB | `eq_gain_low_mid` |
| 44  | EQ (Custom only) | Freq: Low Mid | 0–127 | maps to Hz | `eq_freq_low_mid` |
| 45  | EQ | Gain: Mid | 0–30 | 0 = -15 dB; 15 = 0 dB; 30 = +15 dB | `eq_gain_mid` |
| 46  | EQ (Custom only) | Freq: Mid | 0–127 | maps to Hz | `eq_freq_mid` |
| 47  | EQ | Gain: High Mid | 0–30 | 0 = -15 dB; 15 = 0 dB; 30 = +15 dB | `eq_gain_high_mid` |
| 48  | EQ (Custom only) | Freq: High Mid | 0–127 | maps to Hz | `eq_freq_high_mid` |
| 49  | EQ | Gain: High | 0–30 | 0 = -15 dB; 15 = 0 dB; 30 = +15 dB | `eq_gain_high` |
| 50  | EQ (Custom only) | Freq: High | 0–127 | maps to Hz | `eq_freq_high` |
| 51  | EQ (Custom only) | Freq: High Cut | 0–127 | maps to Hz | `eq_freq_high_cut` |
| 52  | Enhancer | On/Off | 0–1 | 0 = Off; 1 = On | `enhancer` |
| 53  | Enhancer | Instrument | 0–1 | 0 = Guitar; 1 = Bass | `enhancer_instrument` |
| 54  | Enhancer | Dry/Wet | 0–127 | 0–100% | `enhancer_dry_wet` |
| 55  | Enhancer | Body | 0–127 | 0–100% | `enhancer_body` |
| 56  | Enhancer | Thickness | 0–127 | 0–100% | `enhancer_thickness` |
| 57  | Enhancer | Brilliance | 0–127 | 0–100% | `enhancer_brilliance` |
| 58  | Reverb | On/Off | 0–1 | 0 = Off; 1 = On | `reverb` |
| 59  | Reverb | Preset | 0–12 | 0 = Studio A; 1 = Studio B; 2 = Basement … | `reverb_preset` |
| 60  | Reverb | Type | 0–1 | 0 = Room; 1 = Ambience | `reverb_type` |
| 61  | Reverb | Dry/Wet | 0–127 | 0–100% | `reverb_dry_wet` |
| 62  | Reverb | Size | 0–127 | 0–100% | `reverb_size` |
| 63  | Reverb | Echo | 0–127 | 0–100% | `reverb_echo` |
| 64  | Reverb | Color | 0–127 | 0–100% | `reverb_color` |
| 65  | Preset Level | Preset Level | 0–107 | (per-preset level) | `preset_level` |
| 66  | General | Master Volume | 0–100 | 0 = Mute; 1–100 = Level | `master_volume` |
| 67  | General | Master Mute | 0–1 | 0 = Off (no mute); 1 = On (mute) | `master_mute` |
| 68  | Preset | Preset select | 0–99 | 0 = Preset #1 … | `preset_select` |
| 69  | Tuner | On/Off | 0–1 | 0 = Off; 1 = On | `tuner` |
| 70  | Preset | Preset down | 0–127 | step previous | `preset_down` |
| 71  | Preset | Preset up | 0–127 | step next | `preset_up` |

Every row above is sourced from the MIDI Chart:
<https://wiki.two-notes.com/doku.php?id=opus:opus_user_s_manual#midi_chart>

## Caveats & uncertainties

- **Preamp model count discrepancy.** The MIDI Chart enumerates **10** preamp
  models (CC#5, 0–9) in a particular order, while the prose ("The TSM Preamp"
  section) lists **12** named preamps (adds FlatBackV and Foundry Bass) with a
  different ordering. The YAML follows the *MIDI Chart* enumeration because that
  is what the device decodes off the wire. The two extra prose models may map to
  CC values 10/11 in newer firmware — unconfirmed.
- **Power amp model list is truncated** in the chart ("0 = SE 6L6; 1 = SE EL34;
  2 = SE EL84 …", range 0–7). Only the first three are published, so
  `power_amp_model` is modeled as an `int` 0–7 with a `# TODO: confirm`. The prose
  documents 4 tube types (6L6, EL34, EL84, KT88) × {SE, PP} which would yield 8
  combinations, consistent with the 0–7 range, but the exact index→combo mapping
  beyond index 2 is not published.
- **Variable-length lists.** `virtual_cabinet` (CC#17), `ir_file_a/b` (CC#34/35)
  are documented as "0–x" — the upper bound depends on how many cabinets/IRs are
  loaded into the unit. `virtual_cabinet` is bounded to 0–63 (64 DynIR slots per
  Specifications → Memory) and the IR files to 0–127 as a safe MIDI ceiling; both
  carry `# TODO: confirm` because the true max is content-dependent.
- **EQ gain range mismatch.** Prose says Guitar/Bass EQ modes have a ±20 dB range
  and Custom bands are ±20 dB, but the MIDI Chart's gain CCs (41/43/45/47/49) map
  **0–30 → -15 dB … +15 dB**. The YAML uses the MIDI Chart's 0–30 / ±15 dB mapping
  since that is the wire behaviour. The EQ frequency CCs are "specific mapping to
  Hz" with no published table, so they are kept as raw 0–127 ranges.
- **`reverb_preset` count.** Chart says 0–12 (13 values) = 12 spaces + 1 custom,
  matching the prose ("12 room reverbs and one full 'CUSTOM' reverb"). Only the
  first three space names are published.
- **No Bank Select / no SysEx / no NRPN** parameter control is documented for the
  Opus — all parameter access is plain CC, and presets are plain Program Change.
- **`settle_ms` is not from Two Notes.** Set to 50 ms as a conservative guess to
  let a recalled preset (which loads a cabinet IR) settle before CC overrides are
  applied; tune against real hardware.
- **On/off encoding.** Unlike some pedals (and the previous placeholder), Opus
  on/off and switch CCs use **0/1**, not 0/127. Sending 127 to a 0–1 control is
  out of the documented range.

## USB readback — Torpedo Remote HID protocol (state verification)

For the USB readback research (verifying what a BLE-MIDI write actually landed —
see `docs/research/usb.md`), the Opus is the only rig pedal that exposes **no
USB-MIDI at all**: its sole USB function is a vendor **HID** interface, the
channel the **Torpedo Remote** editor uses. All non-MIDI device state (cabinet
manager, IR loader, firmware) goes over this HID pipe.

### Transport (confirmed)

- Single USB interface, **HID** (class `03`/`00`/`00`), VID:PID **`0483:A334`**
  (STMicroelectronics — the Opus is an STM32-based device; manufacturer string
  "Two Notes Audio Engineering", product "OPUS"). Full Speed, self-powered.
- Two interrupt endpoints: **EP `0x01` OUT / `0x81` IN, 64-byte** reports,
  `bInterval` 1. A raw bidirectional 64-byte vendor pipe.
- On Linux it binds `usbhid` and appears as a `hidraw` node. The editor (Torpedo
  Remote desktop) is **Mac/Win only**; the wireless app (Android/iOS) talks to
  the same device family over **BLE**, not USB. So on Linux the HID interface is
  free for direct access.

### HID report descriptor (confirmed — dumped from the device)

`wDescriptorLength` = 36 bytes. `lsusb -v` prints it as `** UNAVAILABLE **` (the
kernel `usbhid` driver holds the interface and `lsusb` won't detach it); it was
read instead from sysfs (`/sys/.../<intf>/<hidbus>/report_descriptor`). Raw bytes:

```
06 00 FF 09 01 A1 01 19 02 29 41 15 00 25 7F 95 40 75 08 81 26
            19 42 29 81 15 00 25 7F 95 40 75 08 91 22 C0
```

Decoded:

| Item | Value |
|------|-------|
| Usage Page | `0xFF00` (vendor-defined) |
| Usage / Collection | Usage `0x01`, Application collection |
| Report ID | **none** (unnumbered reports) |
| Input report | usages `0x02..0x41`, Report Count 64, Report Size 8, Logical 0..127, flags `0x26` = Data, Var, **Relative** |
| Output report | usages `0x42..0x81`, Report Count 64, Report Size 8, Logical 0..127, flags `0x22` = Data, Var, **Absolute** |

So: **one 64-byte Input report and one 64-byte Output report, no report IDs, and
*no Feature reports*.** This corrects the plan's "feature-report readback layout"
wording — the Opus does **not** use HID FEATURE reports; readback rides the same
64-byte interrupt **Input** report. Concretely: write a request to EP `0x01`,
read the reply on EP `0x81` (over hidraw: a plain `write()`/`read()` of 64-byte
reports). The `Relative`/`Absolute` flags are cosmetic for a vendor pipe and
carry no semantic meaning here.

### Command layout (decompiled from Torpedo Remote — 2026-06-03)

First mapped from a live `rig-capture capture hid` snoop (Frida `Interceptor`
hooks on the IOKit `IOHIDDevice{Set,Get}Report[WithCallback]` calls), then
**confirmed and completed by static analysis** of the Torpedo Remote desktop
editor (a JUCE/C++ app, shipped **unstripped**) with Ghidra. Everything below is
**generic protocol structure** lifted from the editor's own dispatch and
parameter tables — no rig content (serials, preset/cabinet names, IR hashes) is
recorded here, per the public/private rule.

**Framing**

- Every report is exactly **64 bytes, zero-padded**. OUT = HID **Output** reports,
  IN = HID **Input** reports (the one-of-each pair the descriptor declares).
- **Byte 0 is the opcode.** Host→device commands carry their `CommandType` enum
  value in byte 0; the editor passes that same value as the HID `reportID`
  argument even though the descriptor declares *no* report IDs — it is **not** a
  HID report-ID prefix, the firmware just keys off byte 0.
- Device→host frames are matched by `Torpedo::processIncomingMessage` against an
  `IncomingMessageFactory` keyed on **byte 0 plus an optional byte-1 range**.
  When nothing matches, the host falls back to emitting a `0x7e` **Ping**, which
  is therefore also the idle poll/keepalive (empty payload — the dominant OUT).

**Message classes** (byte 0 — confirmed against a 30k-frame live capture)

| byte0 | dir | meaning |
|-------|-----|---------|
| `0x7e` | OUT | **Ping / poll** (empty payload; the idle keepalive — dominant OUT). |
| `0x05` | IN  | **Status heartbeat** — periodic 64-byte state snapshot (preset idx, levels/meters, serial fragment). |
| `0x01` | both | **Parameter read / report** — `[0x01][wireIndex][value…]` (see index maps). |
| `0x6f` | both | **Streaming / bulk** — high-rate packed payload (live drag / metering curve). |
| `0x6e` | OUT | **Load-by-name** — select a cabinet/IR/mic by null-padded ASCII name. |
| `0x04` | both | **Query/command envelope** — byte 1 carries the `CommandType` (table below). |
| `0x02` / `0x03` | IN | preset payload (titles + value block). |
| `0x0c` | both | periodic control / sync. |
| `0x70` | OUT | **UploadAudioData** (IR upload) start/ack. |
| `0x0b` | IN  | identification / handshake (startup; carries serial). |

**`CommandType` sub-codes** (byte 1 under the `0x04` envelope; resolved from the
editor's vtables / `Command<(CommandType)N>` templates)

| byte1 | Command (editor class) |
|-------|------------------------|
| `0x09` | `SpeakerFilenameQuery` (also first stage of `PresetNamesQuery`) |
| `0x3b` (59) | `SwitchingCommand` |
| `0x3d` (61) | `MD5Query` — IR/speaker content hash (see below) |
| `0x68` (104) / `0x6d` (109) | `MoveItemCommand` variants (reorder cabinets / presets) |
| `0x73` (115) | `PresetNamesQuery` |

  IN `0x04` replies use byte 1 `0x70` (ack), `0x31` (list entry), `0x0f` (name),
  `0x3d` (16-byte md5 digest). `IdentificationQuery` is `CommandType 1` and
  `Ping` is `CommandType 126` (`0x7e`, sent as its own byte-0 class). Additional
  `Command<(CommandType)N>` codes exist for N ∈ {`0x03`,`0x04`,`0x0a`,`0x0b`,
  `0x0e`,`0x27`,`0x38`,`0x3a`,`0x42`,`0x45`,`0x49`,`0x75`}, not all mapped yet.
  `SetParameterCommand` is a `ModeOutgoingMessage` (mode-flagged set) rather than a
  fixed `CommandType`.

**Parameter access model**

- A parameter set (`SetParameterCommand`) and the device's change notification
  (`ParameterChangedMessage`) share the layout **`[opcode] [wireIndex] [value]`**
  — byte 1 is the parameter's wire index, byte 2 the value.
- `wireIndex = channel * blockSize + hwIndex`. The Opus is single-channel, so
  `channel = 0` and `wireIndex = hwIndex` straight from the tables below. Indices
  at/above `channels * blockSize` select the **setup** table instead of the
  **parameter** table (the firmware keeps two `HardwareIndexDictionary` trees).
- `MD5Query` (`0x3d`) sends `[0x3d] 00 <subtype>` and the reply carries a
  **16-byte digest at byte offset 3** (the per-IR content hash the editor uses to
  identify cabinet files). Subtype is a small enum (0–6).

**Opus parameter index map** (`OpusParameterIndexDictionary`, `name → wireIndex`;
values are decimal). This is the byte 1 used in `[opcode][wireIndex][value]`:

| idx | name | idx | name | idx | name |
|----:|------|----:|------|----:|------|
| 0 | PARAM_SIMUAMP | 23 | PARAM_EQ_F4 | 46 | PARAM_SIMUHP_BYPASS |
| 1 | PARAM_SIMUAMP_MODELE | 24 | PARAM_PRESET_LEVEL | 47 | PARAM_SIMUHP_BYPASS_2 |
| 2 | PARAM_SIMUAMP_GAIN | 25 | PARAM_REVERB | 48 | PARAM_REVERB_SIZE |
| 3 | PARAM_SIMUAMP_PRESENCE | 26 | PARAM_REVERB_ROOM | 49 | PARAM_REVERB_ECHO |
| 4 | PARAM_SIMUAMP_DEPTH | 27 | PARAM_REVERB_DRYWET | 50 | PARAM_REVERB_COLOR |
| 5 | PARAM_SIMUAMP_CHARACTER | 28 | PARAM_PRESET_MODE | 51 | PARAM_REVERB_MODEL |
| 6 | PARAM_SIMUHP | 29 | PARAM_SIMUHP_LEVEL | 52 | PARAM_NOISEGATE |
| 7 | PARAM_SIMUHP_PREVIEW | 30 | PARAM_SIMUHP_PHASE | 53 | PARAM_NOISEGATE_MODE |
| 8 | PARAM_SIMUHP_SPEAKER | 31 | PARAM_SIMUHP_MUTE | 54 | PARAM_NOISEGATE_THRESHOLD |
| 9 | PARAM_SIMUHP_WAVE0 | 32 | PARAM_SIMUHP_MIC_2 | 55 | PARAM_ENHANCER |
| 10 | PARAM_SIMUHP_WAVE1 | 33 | PARAM_SIMUHP_DISTANCE_2 | 56 | PARAM_ENHANCER_INSTRUMENT |
| 11 | PARAM_SIMUHP_FOLDER_IR_0 | 34 | PARAM_SIMUHP_CENTER_2 | 57 | PARAM_ENHANCER_BODY |
| 12 | PARAM_SIMUHP_FOLDER_IR_1 | 35 | PARAM_SIMUHP_POSITION_2 | 58 | PARAM_ENHANCER_THICKNESS |
| 13 | PARAM_SIMUHP_MIC | 36 | PARAM_SIMUHP_LEVEL_2 | 59 | PARAM_ENHANCER_BRILLIANCE |
| 14 | PARAM_SIMUHP_DISTANCE | 37 | PARAM_SIMUHP_PHASE_2 | 60 | PARAM_ENHANCER_DRYWET |
| 15 | PARAM_SIMUHP_CENTER | 38 | PARAM_SIMUHP_MUTE_2 | 61 | PARAM_PREAMP |
| 16 | PARAM_SIMUHP_POSITION | 39 | PARAM_EQ_FREQ_LOW | 63 | PARAM_PREAMP_GAIN |
| 17 | PARAM_EQ | 40 | PARAM_EQ_FREQ_F0 | 64 | PARAM_PREAMP_TREBLE |
| 18 | PARAM_EQ_MODE | 41 | PARAM_EQ_FREQ_F1 | 65 | PARAM_PREAMP_MID |
| 19 | PARAM_EQ_F0 | 42 | PARAM_EQ_FREQ_F2 | 66 | PARAM_PREAMP_BASS |
| 20 | PARAM_EQ_F1 | 43 | PARAM_EQ_FREQ_F3 | | |
| 21 | PARAM_EQ_F2 | 44 | PARAM_EQ_FREQ_F4 | | |
| 22 | PARAM_EQ_F3 | 45 | PARAM_EQ_FREQ_HIGH | | |

(Index 62 is unused; `PARAM_PREAMP_MODEL` is editor-side only — no wire index.)

**Opus setup index map** (`OpusSetupIndexDictionary`, `name → wireIndex`):

| idx | name | idx | name |
|----:|------|----:|------|
| 0 | SETUP_LEVEL | 9 | SETUP_OPTIM |
| 1 | SETUP_MUTE | 10 | SETUP_INPUT_CHANNEL_SELECTION |
| 2 | SETUP_ON_OFF | 11 | SETUP_OUTPUT_JACK_ROUTING |
| 3 | SETUP_PRESET | 12 | SETUP_DISPLAY_BRIGHTNESS |
| 4 | SETUP_PRESET_SOURCE | 13 | SETUP_SCREENSAVER_TYPE |
| 5 | SETUP_FLAG_PRESET_MODIF | 14 | SETUP_SCREENSAVER_TIME |
| 6 | SETUP_MIDI_CC | 15 | SETUP_TUNER_FREQ |
| 7 | SETUP_MIDI_PC | 16 | SETUP_TUNER_MUTE |
| 8 | SETUP_MIDI_CHANNEL | 17 | SETUP_BLE_POWER |

(`SETUP_NOISEGATE_LEARN` is editor-side only — no wire index.)

**Opus info index map** (`OpusInfoIndexDictionary`, read-only telemetry):

`0` INFOS_DSP_VOLUME_IN · `1` INFOS_DSP_VOLUME_OUT · `3` INFOS_DSP_BINARY_VALUES ·
`4` INFO_DSP_ERROR · `5` INFOS_DSP_TUNER_NOTE · `6` INFOS_DSP_TUNER_DIFF ·
`7` INFOS_DSP_DEBUG_PROC_USAGE · `8` INFOS_DSP_NOISE_GATE. The input/output
clip and silence flags are computed client-side (negative indices, never on the
wire); `INFOS_TUNER_STATE` has no wire index.

**Encoding notes**

- Strings (cabinet/preset/IR names) are **ASCII, null-padded** at a fixed offset
  after the opcode bytes. Multi-byte numerics are little-endian.
- The editor selects the parameter-query *strategy* by firmware/type at runtime
  (`Studio` / `Legacy` (fw < 5.0) / `Unified`), so exact query framing can vary by
  firmware generation; the Opus uses the unified path.

**Status:** transport, report descriptor, **frame dispatch, message classes,
command opcodes, and the full Opus parameter / setup / info index maps are decoded**
from the editor binary **and cross-validated against a 30k-frame live capture**
(every observed `0x01` parameter index resolves to a mapped name; `0x04` byte-1
values are all known `CommandType`s). What remains is per-parameter **value
scaling/ranges** (raw byte ↔ engineering units), the `0x05` status field layout,
and a few unmapped `0x04` sub-codes. A round-trip readback is still not wired into
`verify_control` (verification stays on the MIDI-echo path for now — see
`docs/research/usb.md`). The decoded capture (with rig-specific values) is in
`docs/private/opus-capture-analysis.md`.

### Probe tool (read-only)

`cmd/usb-probe --device opus` speaks the HID pipe directly over hidraw (auto-detects
`0483:A334`; no cgo/hidapi). It is **read-only by design and never synthesises a
request**, because the command bytes are unknown and a wrong write could change
device state:

- default — **listen-only**: drains and prints any 64-byte Input reports the
  Opus emits on its own (e.g. a front-panel edit or a preset recall over MIDI);
  sends nothing.
- `--opus-raw "F0 .."` — replays exactly one operator-supplied captured frame
  (padded/truncated to 64 bytes) and dumps the reply, for replaying a request
  lifted from a Torpedo Remote capture.

hidraw nodes are root-only; grant per-session access with
`sudo setfacl -m u:$USER:rw /dev/hidrawN`.
