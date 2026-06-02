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
