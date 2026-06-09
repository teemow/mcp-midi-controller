# Moog Model D (AUv3) — live sound-engineering session findings

Findings from the first full closed-loop sound session against the Moog Model D
AUv3 (`aumu/mmdx/Moog`, probe id `mmdx`, 70 writable params) hosted in AUM on
the iPad, driven entirely through the rig: `author_aum_session` (X32 hardware
profile) → ProbeMidiBrain (hands) → banked convention CCs on ch1 →
ProbeAudioTap (ears) → `probe_sound` analysis. Session date 2026-06-09.

## Session recipe that works

- `author_aum_session` with `hardware: "x32"` so the master strip routes to the
  X32 main out over the X-USB card — the default `builtin` profile authors a
  device-independent session that is **inaudible on the X32 rig** (and the tap
  sits post-fader, so a mis-routed/mis-leveled chain can read silence too).
- Channels: MIDI strip (brain) / audio strip (Model D + post-fader tap) /
  master (bus 0 → HW out 0). Route brain → synth node **and** →
  `BuiltIn:MIDI Control` — the second wire is what makes the ~80 banked
  parameter CCs work; without it notes can still play while every knob is dead.
- **Matrix port-name pitfall:** the brain's matrix source key must use its real
  port name (`Node:Chan0:Slot0:ProbeMidiBrain`), not the generic `MIDI OUT` —
  AUM silently drops unresolvable connection keys. See
  `aum-midi-matrix.md` ("Endpoint keys") for the full finding.

## Gain staging (the first thing to fix)

The Model D's default state is **far too hot**: three oscillators at ~0.5 gain
into the ladder filter with master volume ~0.78 pins the channel at **0 dBFS**
with a square-like crest factor (~2.5 dB) — it clips/saturates internally, so
peak stays at 0 dBFS while volume moves only reshape the spectrum (lower
volume → fewer high harmonics, lower centroid, higher HNR). Trim
`syn_volume_k` (convention CC 71) to **45–60** (≈0.35–0.47) for healthy
−9…−12 dBFS peaks at velocity ~100–115. Verify on the tap, not by ear.

## CC quantization: tune/detune params are too coarse for CC moves

`vco1_semi_k` / `vco2_semi_k` span ±0.72 over 127 CC steps — **one CC step is
several tens of cents** (measured: two steps off center put osc2 ~95 cents
sharp, an unmusical warble the tap flagged as ~40 beat-onsets during a held
note). Consequences:

- The factory default state already carries a musical micro-detune
  (±5–10 cents). For "fatter", move **one** CC step at most (64→65), or leave
  tuning alone and change waves/ranges instead.
- For precise detune values, author the param into the session/default state
  (`set_auv3_default_state`) instead of sending a quantized CC.

## Sound-design map (what moved what, measured)

All on the banked ch1 convention CCs of the authored session (CC numbers are
per-session — read them from `get_aum_session`, here CC30–99):

| Move | Controls | Measured effect (tap) |
|------|----------|----------------------|
| Dry foundation | delay off (89=0), osc-mod off (38=0) | kills the slow AM wobble of the default patch |
| 3-saw stack + sub | waves 41/44/47=51 (saw), osc3 range 45=25 (−1 oct) | even+odd harmonic series; 32 Hz sub under a 65 Hz C2 |
| Classic bass pluck | cutoff 62≈50, emphasis 63≈45, contour amount 64≈90, filter ADS 65=0/66≈55/67≈30 | centroid 3.6 kHz → ~540 Hz; punchy attack, warm sustain |
| Acid squelch | emphasis 63≈105, filter sustain 67=0, decay 66≈75, cutoff 62≈35 | resonant bite per note; sustain collapses to near-pure sub (centroid ~49 Hz) |
| Wide & dirty | osc2 wave 44≈102 (wide pulse), ±1-step detunes | tap shows slow amplitude beating (chorus movement); mono out — stereo width must come from downstream FX |
| Lead | cutoff 62≈75, filter sustain 67≈75, loudness sustain 70=127, short glide 34≈18, delay on 89=127 mix 90≈40 | centroid ~5 kHz, delay repeats visible in the tap waveform |

## Glide needs legato — and that needs timed sequences

The Model D is mono/legato by default: glide only sounds when a new note-on
arrives **while the previous note is still held**. `play_notes` (note-on → hold
→ note-off) can never produce that overlap, so glide demos required a manual
chain of `send_midi` noteOn/noteOff calls with sleeps in between — workable but
slow and timing-imprecise over many round trips. This (plus phrase-length
probes generally) motivated the **`sequence`** parameter on `play_notes` and
`probe_sound`: timed steps of notes (with per-step `at_ms`/`duration_ms`, so
overlap is expressible) and CCs (e.g. open the filter mid-phrase), executed in
one call and — for `probe_sound` — captured/analysed as one segment.

## Misc observations

- The Model D AU transposes notes down an octave relative to raw MIDI in this
  default state (MIDI 48 sounded as C2 ≈ 65.4 Hz); account for it when
  asserting pitch.
- Factory presets are PC-recallable (164 presets, see the probe dump); the
  session's `AuPresetCtrl` can also pin one at author time.
- The tap's onset counter is a good detune/wobble detector: a steady held note
  with beating registers periodic onsets (~1–2 Hz for one-step detune).
