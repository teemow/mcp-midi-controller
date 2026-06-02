# Source Audio EQ2 — MIDI implementation research

Research note backing `internal/device/definitions/eq-2.yaml`. The EQ2
(Programmable EQ, model SA270) is a Source Audio **One Series** pedal with
**full MIDI implementation**: 128 presets recallable by Program Change, and
effectively every DSP parameter reachable by Control Change. Unlike the Boss
pedals, the EQ2's CC map is **remappable** (global, via the Neuro editor), so
the default CC numbers are a starting point rather than a fixed contract.

## Sources

- **EQ2 User Guide (PDF)** — Source Audio (`SA270`, v4.7):
  <https://sourceaudio.net/uploads/1/1/5/1/115104065/eq2manualv4_7.pdf>
- **EQ2 product page** (features, links to manual + MIDI Implementation Guide):
  <https://sourceaudio.net/products/eq2>
- **EQ2 User Guide mirror** (full text used for the MIDI section below):
  <https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html>
- **Source Audio MIDI / Neuro FAQ** (channel + MIDI-map editing, clock sync):
  <https://sourceaudio.net/pages/faq>
- **Retailer feature summary** (cross-check of CC 103/104 preset recall):
  <https://www.pitbullaudio.com/source-audio-eq2-programmable-eq-guitar-effects-pedal-w-midi-in-thru.html>

> **The per-parameter default CC table is published only in the separate "EQ2
> Programmable EQ MIDI Implementation Guide"** (linked at the bottom of the EQ2
> product page), which is a download and not reproduced in the user guide.
> The confirmed facts below come from the user guide and product pages; the full
> default CC→parameter list is marked `# TODO` in the YAML and is best captured
> per-rig via **MIDI-learn** or read from the Implementation Guide.

## Transport / MIDI ports

- The EQ2 has **5-pin DIN MIDI IN and OUT/THRU** plus a **mini-USB** port
  (class-compliant USB-MIDI). It also accepts MIDI through a **Neuro Hub**
  connected to the Control Input.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- In this project it is reached over the DIN chain behind a BLE-MIDI hub, so it
  is a `blemidi` device addressed by `(endpoint, channel)`.

## Preset recall (Program Change + CC)

- The EQ2 stores **128 presets**, **all 128 recallable via MIDI Program
  Change** (PC). On the pedal itself only 4 (or 8 in Preset Extension Mode) are
  reachable by footswitch; MIDI reaches all 128. Modeled as `program_change`
  `0-127`.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- Presets are also recallable via **CC#103 and CC#104** (per the product
  feature list). The exact semantics of the pair (e.g. select vs. bank, or
  up/down) are **not** documented in the user guide — `# TODO: confirm` against
  the Implementation Guide. PC is the clean, unambiguous recall path and is what
  the YAML models.
  [feature summary](https://www.pitbullaudio.com/source-audio-eq2-programmable-eq-guitar-effects-pedal-w-midi-in-thru.html)
- Tip from the manual: to recall a preset **with the effect bypassed**, save the
  preset in its bypassed state; recalling loads the stored settings with the
  effect off until engaged. (Useful for "queue up but don't engage" scenes.)

## Parameter control (CC) — remappable, default map in the guide

- "Many of the EQ2's parameters (even those not assigned to a control knob) are
  directly accessible via MIDI CC." The pedal ships **pre-mapped to a default
  set of CC numbers**; the full list + ranges is in the **MIDI Implementation
  Guide** download.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- **CC mapping is global and user-remappable** via the **Neuro Desktop Editor**
  (`Device → Edit Device MIDI Map`): any of the 128 CC numbers can be assigned
  to any parameter. Custom maps are **not per-preset** — they apply globally.
  [faq](https://sourceaudio.net/pages/faq)
- Consequence for this project: the EQ2 is the canonical case for the design's
  **MIDI-learn / generic-CC** story. Rather than hard-coding the vendor default
  numbers (which a user may have remapped), the recommended path is to capture
  the actual CC per parameter with `learn_start`/`learn_capture`, or to transcribe
  the Implementation Guide into the YAML once and keep it as the source of truth
  that the Neuro map must match (mirrors the AUM convention model).

### Parameters reachable over CC (from the user guide; CC#s TBD)

These are the editable parameters the guide describes as MIDI/Neuro-accessible.
CC numbers are intentionally **omitted** until confirmed from the Implementation
Guide; each is a `# TODO` in the YAML.

- **Bypass / engage** (Universal Bypass on/off).
- **Output level** (master, ±, with up to +12 dB boost; unity at mid).
- **Per-channel gain** (Ch1/Ch2, −18…+18 dB) — relevant in Split Mode.
- **Per-band, ×10 bands, ×2 channels:** band **Level** (boost/cut, ±18 dB),
  band **Frequency** (20 Hz–20 kHz), band **Q** (≈0.1…10).
- **Shelf type** for bands 1 and 10 (peaking vs. low/high shelf).
- **Input high-pass filter** per channel (10–80 Hz).
- **Split Mode** on/off (independent Ch2 EQ) and **channel swap**.
- **Routing mode** (mono/stereo/effects-loop variants).
- **Noise gate** (threshold) and **limiter** — optional, configured in Neuro.
- **Tuner** on/off.

## MIDI channel & clock

- **Default receive channel is 1.** The channel is configurable in the Neuro
  editors (1-16). In this project the channel comes from the **binding**, which
  must match the EQ2's configured channel.
  [manual](https://www.manualslib.com/manual/1847200/Source-Audio-Eq2-Programmable-Eq.html)
- **MIDI clock**: One Series pedals only sync to clock if the active preset has
  "Sync to MIDI Clock"/Tap enabled — and **EQ-type effects have no time-based
  parameters**, so MIDI clock is effectively irrelevant for the EQ2. Not
  modeled.
  [faq](https://sourceaudio.net/pages/faq)

## Neuro Hub "Scenes" (out of scope)

- A **Neuro Hub** can store multi-pedal **Scenes** (up to 128) recalled over
  MIDI. That is a hub-level feature layered over several Source Audio pedals; it
  is **not** an EQ2 MIDI capability per se and is out of scope for the single
  device definition. This server's own scene model supersedes it.

## Summary for the YAML

- `transport: blemidi`; channel from the binding (device default = 1).
- `preset` as `program_change` `0-127` (all 128 presets) — the reliable recall
  path; `settle_ms` set conservatively (preset load) and tuned on hardware.
- Note CC#103/CC#104 as an alternate preset-recall path (`# TODO` semantics).
- Per-parameter controls (bypass, output, per-band level/freq/Q, etc.) are
  enumerated as `# TODO` CC controls: the default numbers live in the MIDI
  Implementation Guide and are **remappable**, so MIDI-learn is the intended way
  to bind them per rig.
