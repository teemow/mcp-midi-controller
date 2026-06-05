# AUv3 plugins & the AUM CC convention

Research note for the AUv3 instruments/effects hosted in **AUM** on the iPad and
controlled by this server over **BLE-MIDI**. It covers how AUv3 + AUM MIDI
mapping actually works, the per-plugin convention CC maps we ship, which plugins
support native MIDI control, and the source URLs used.

## How AUv3 + AUM MIDI control mapping works

AUv3 (Audio Unit v3 Extension) plugins expose their parameters to the host as an
**AU parameter tree** — but they do **not** carry fixed, vendor-assigned MIDI CC
numbers the way hardware pedals do. A plugin parameter only responds to a CC if a
mapping is established somewhere:

1. **AUM MIDI control** — In AUM, each hosted node has a **MIDI Control** button
   that lists the plugin's writable AU parameters. Tapping a parameter opens a
   panel where you choose the MIDI **channel**, the **message type** (CC, NOTE,
   PC, PBEND, CHPRS) and the **number** (data byte 1, e.g. the CC number) it
   should respond to. AUM builds these control entries from the AU parameters
   that are flagged writable. This is the path that works for *every* AUv3.
2. **Plugin MIDI-learn** — Some plugins have their own MIDI-learn (touch a
   control, wiggle a CC). FabFilter Pro-Q is the notable example here.

Because the mapping is user-defined, **the CC number is an arbitrary convention**
that *we* invent and then mirror into AUM. The YAML definition is the **source of
truth**; AUM (or the plugin's MIDI-learn) is configured to match it.

Addressing is `(endpoint, channel)`: the iPad is one BLE-MIDI endpoint, and each
plugin instance is bound to its **own MIDI channel**. That is why reusing CC
numbers (we start every file at **CC 30**) across different plugin files is fine —
they are disambiguated by channel. Within a single file the CCs are unique and
contiguous.

### Why we start at CC 30

CCs 0-31 (and their LSB pairs 32-63) plus 64-69 (pedals), 120-127 (channel mode)
are commonly used by transports/controllers for bank select, modulation,
volume (CC 7), pan (CC 10), sustain (CC 64), etc. Starting our convention block
at **CC 30** keeps the macro knobs clear of the most common transport traffic
while staying simple and contiguous. (CC 30 itself is undefined/free in the MIDI
spec.)

## Native CC / MIDI-learn support per plugin

| Plugin | Native CC list? | MIDI-learn? | Notes |
|--------|-----------------|-------------|-------|
| Arturia iSEM | No fixed CC map | Yes (Core MIDI / MIDI-learn) | Map via iSEM MIDI-learn or AUM. No published default CC table. |
| Kai Aras Agonizer | No fixed CC map | Via host (AUM MIDI control) | Exposes AU params to the host; map in AUM. MPE-capable for pitch/pressure. |
| Korg iMS-20 | No fixed CC map | Via host (AUM MIDI control) | Map AU params in AUM. |
| FabFilter Pro-Q | No fixed CC map | **Yes — built-in interactive MIDI-Learn** | Only one here with first-class MIDI-Learn; can also follow the "active band". Still no default CC numbers — you assign them. |

> Because these CC maps are an invented convention, verifying that a definition
> is **correct** and covers the plugin's **maximum** functionality needs a
> dedicated feedback path (AUM does not echo MIDI). See
> `docs/research/auv3-feedback.md` for that design — chiefly an `auv3-probe`
> companion that dumps each plugin's `AUParameterTree`.

In all four cases the CC numbers in our YAML are a **chosen convention**, not a
vendor-fixed mapping. FabFilter Pro-Q is the only plugin that documents
first-class MIDI control of its own, so for Pro-Q the convention can be entered in
the plugin's MIDI-Learn instead of (or in addition to) AUM.

## Per-plugin convention CC maps

All maps use `value: range 0-127` unless noted. Each plugin is bound on its own
MIDI channel; `preset` is `program_change` (only meaningful if AUM maps Program
Change to preset recall for that node).

### Arturia iSEM — `internal/device/device-types/isem.yaml`

Physical model of the 1974 Oberheim SEM: dual oscillator (saw + variable-width
pulse w/ PWM), 12 dB/oct multi-mode filter (continuous LP -> notch -> HP, fixed
BP), two ADS envelopes, sine LFO, plus added sub-osc, noise, 2nd LFO, arp,
portamento and a mod matrix.

| Param | CC | Meaning |
|-------|----|---------|
| `preset` | PC | preset / Voice Programmer slot |
| `cutoff` | 30 | filter cutoff |
| `resonance` | 31 | filter resonance |
| `filter_mode` | 32 | continuous LP->notch->HP morph |
| `env_amount` | 33 | envelope depth to cutoff |
| `attack` | 34 | ADS attack |
| `decay` | 35 | ADS decay |
| `sustain` | 36 | ADS sustain |
| `lfo_rate` | 37 | sine LFO rate |
| `lfo_depth` | 38 | LFO depth |
| `osc2_detune` | 39 | osc 2 detune |
| `pulse_width` | 40 | pulse width / PWM |
| `sub_level` | 41 | sub-oscillator level |
| `volume` | 42 | master volume |

Source: <https://www.arturia.com/products/ios-instruments/isem/overview>

### Kai Aras Agonizer — `internal/device/device-types/agonizer.yaml`

Monophonic wavetable bass synth (with Jakob Haq): dual wavetable oscillators
(morph + cross-mod), sub + noise, "Mangle" pre-filter section (Drive, Bitcrush,
Lift, Push), VA ladder filter, the Wobulator (sequenced LFO), key/BPM-sync LFO,
AD + ADSR envelopes, tube master drive, 2 stereo FX (chorus, delay).

| Param | CC | Meaning |
|-------|----|---------|
| `preset` | PC | factory/user preset recall |
| `cutoff` | 30 | ladder filter cutoff |
| `resonance` | 31 | ladder filter resonance |
| `drive` | 32 | Mangle drive |
| `bitcrush` | 33 | Mangle bitcrush |
| `wavetable_morph` | 34 | wavetable morph/position |
| `osc_xmod` | 35 | oscillator cross-mod |
| `sub_level` | 36 | sub-osc level |
| `attack` | 37 | ADSR attack |
| `decay` | 38 | ADSR decay |
| `sustain` | 39 | ADSR sustain |
| `release` | 40 | ADSR release |
| `lfo_rate` | 41 | LFO rate |
| `master_drive` | 42 | tube master drive |
| `delay_mix` | 43 | delay wet/dry |

Sources: <https://numericalaudio.com/agonizer/iOS/> ·
<https://apps.apple.com/app/agonizer/id1583662383>

### Korg iMS-20 — `internal/device/device-types/ims20.yaml`

CMT recreation of the 1978 MS-20: 2 VCO, 2 self-oscillating VCF (HPF + LPF, each
cutoff + peak), 1 VCA, 2 EG, a Modulation Generator (MG), patch panel, and an
SQ-10-style sequencer.

| Param | CC | Meaning |
|-------|----|---------|
| `preset` | PC | program/patch recall |
| `lpf_cutoff` | 30 | low-pass cutoff |
| `lpf_peak` | 31 | low-pass peak/resonance |
| `hpf_cutoff` | 32 | high-pass cutoff |
| `hpf_peak` | 33 | high-pass peak/resonance |
| `eg2_attack` | 34 | EG2 attack |
| `eg2_decay` | 35 | EG2 decay |
| `eg2_sustain` | 36 | EG2 sustain |
| `eg2_release` | 37 | EG2 release |
| `mg_frequency` | 38 | MG rate |
| `mg_vcf_mod` | 39 | MG depth to filter cutoff |
| `vco_mod_intensity` | 40 | EG1/MG depth to VCO pitch |
| `portamento` | 41 | portamento/glide |
| `volume` | 42 | master volume / VCA |

Sources: <https://www.korg.com/products/software/ims20/> ·
<https://www.korguser.net/ims20/html/help/en/synth.html>

### FabFilter Pro-Q — `internal/device/device-types/fabfilter-pro-q.yaml`

Parametric EQ used as the representative FabFilter plugin. Each band: Frequency
(5 Hz - 30 kHz), Gain (-30..+30 dB, Bell/Shelf only), Q. Global Output Gain
(-inf..+36 dB) and Gain Scale. Wire values 0-127 map linearly onto each
parameter's range (e.g. gain 0 = -30 dB, ~64 = 0 dB, 127 = +30 dB).

| Param | CC | Meaning |
|-------|----|---------|
| `preset` | PC | preset recall |
| `band1_freq` | 30 | band 1 frequency |
| `band1_gain` | 31 | band 1 gain |
| `band1_q` | 32 | band 1 Q |
| `band2_freq` | 33 | band 2 frequency |
| `band2_gain` | 34 | band 2 gain |
| `band2_q` | 35 | band 2 Q |
| `band3_freq` | 36 | band 3 frequency |
| `band3_gain` | 37 | band 3 gain |
| `band3_q` | 38 | band 3 Q |
| `output_gain` | 39 | global output gain |
| `gain_scale` | 40 | gain scale (all Bell/Shelf bands) |
| `bypass` | 41 | global bypass (enum: active=0, bypassed=127) |

Pro-Q's **active-band** MIDI-Learn mode (one set of controls drives whichever band
is selected) is an alternative to per-band mapping; our convention models three
explicit bands because per-band is what survives outside the open UI and matches
desired-state/scenes cleanly.

Sources: <https://www.fabfilter.com/products> ·
<https://www.fabfilter.com/help/pro-q/using/midilearn>

## AUM mapping cheat-sheet (planned feature)

The server's authoring tools should be able to **export, per plugin, the channel +
CC list** the user must enter into AUM (or the plugin's own MIDI-Learn). Because
the YAML is the source of truth, the cheat-sheet is mechanical to generate:

- For a bound logical device, emit one row per control: `param | type | number`,
  prefixed with the binding's **MIDI channel**.
- For `cc` controls the number is the CC; for `program_change` the message type is
  PC; enum controls list their wire values.
- Format suggestions: a printable table or a JSON/CSV the user can tick off while
  configuring AUM's MIDI Control panel for that node.

Example shape (FabFilter Pro-Q on, say, channel 6):

```
plugin: FabFilter Pro-Q   channel: 6
  band1_freq    CC 30
  band1_gain    CC 31
  band1_q       CC 32
  ...
  bypass        CC 41   (active=0, bypassed=127)
  preset        PC
```

This turns "set up AUM to match the server" into a copy-the-list task and removes
the guesswork that makes AUv3 control fiddly.

## Source URLs

- AUM help / MIDI control: <https://kymatica.com/aum/help>
- Arturia iSEM: <https://www.arturia.com/products/ios-instruments/isem/overview>
- Kai Aras Agonizer: <https://numericalaudio.com/agonizer/iOS/> ·
  <https://apps.apple.com/app/agonizer/id1583662383>
- Korg iMS-20: <https://www.korg.com/products/software/ims20/> ·
  <https://www.korguser.net/ims20/html/help/en/synth.html>
- FabFilter products: <https://www.fabfilter.com/products>
- FabFilter Pro-Q MIDI-Learn: <https://www.fabfilter.com/help/pro-q/using/midilearn>
