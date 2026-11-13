# Filterstation2, Perforator, RoughRider3 and Humanoid

Four effects a song used for the first time, for a French-house track of
sixteen tracks (123 BPM, seven stems, see [NuSaw](nusaw.md#in-a-song)):
a filtered disco loop, a pump without a sidechain, a compressor on the sum,
and a talk box and a robot vocal. What each is, what a song reaches of it, and
what the rig showed.

| Effect | Probe id | Presets | Parameters | In the song |
|--------|----------|---------|------------|-------------|
| Filterstation2 (Audio Damage) | `aumf-_flt2-_au_da` | 20 | 25, all 0..1, no labels | the electric piano's filter, ramped open and shut |
| Perforator (Bram Bos) | `aufx-perf-_bram` | 35 | 12, all 0..127 | the pump on the NuSaw pad |
| RoughRider3 (Audio Damage) | `aufx-_r_r30-_au_da` | 10 | 11, 0..1 | "Hard Pump" on the master, so on the sum of the stems in the mix |
| Humanoid (BABY Audio) | `aumf-_hmn1-_b4by` | 281 | 62, 0..1 | the talk box and the robot vocal of the chorus |

## Filterstation2

Two multimode filters with an LFO, an envelope follower, saturation, "Hack",
feedback, FM and a routing knob between them. Per filter: `Flt n Freq`,
`Flt n Q`, `Flt n Type`, `Flt n LFO`, `Flt n Env`; then `LFO Sync`,
`LFO Rate`, `LFO Phase`, `LFO Shape`, `Env Attack`, `Env Decay`,
`Saturation`, `Hack`, `Feedback`, `FM`, `Routing`, `Input Lvl`, `Output Lvl`,
`Sidechain`, `Bypass`.

- **A sweep moves both filters.** "01 - Basic Parallel Lowpass" runs the two
  filters side by side, so a ramp of `Flt 1 Freq` alone would leave the
  second one open (not tried). The song ramps `Flt 1 Freq` and `Flt 2 Freq`
  together: 0.12 to 0.4 over the intro, 0.4 to 0.8 under the
  kick's entry, 0.8 down to 0.3 and back in the break, 0.8 to 0.1 in the
  outro. Read at the piano's stem in the mix, per section: -24.0 dBFS rms
  in the intro (0.12 to 0.4), -18.8 as the filter opens (0.4 to 0.8), -19.9
  open, -25.8 in the break that closes it to 0.3 and opens it again, -20.5
  in the outro that closes it to 0.1.
- **The envelope follower is no talk box.** "02 - Basic Envelope Follower"
  after a legato synth lead opened on each note's attack and closed at once:
  the lead alone read -32.8 dBFS rms with peaks at -2.8, a click and a muffled
  rest. Humanoid took that part (below).

## Perforator

A gate mask: a row of steps per channel (`STEPS` of `STEP RESOLUTION`, Lock
L/R), each open step an envelope (`ATTACK`, `DECAY`, `CONTOUR` as "Slope",
`SHUFFLE`), applied to the level or to a low-pass (`FILTER/AMP BALANCE`,
`FILTER CUTOFF`), with `DRY/WET` and `OUTPUT GAIN`. The twelve parameters run
0..127. **The pattern of open steps is no parameter**: it lives in the
plugin's state, so a song gets it from a preset.

- **"Sidechain Effect"** is eight steps of a 32nd, the first four shut: the
  pad ducks on every beat and swells back on the off-beat, the French-house
  pump without a sidechain. It follows the host's tempo. Loaded through AUM's
  Preset Load (a Program Change), it took and the plugin stayed up.
- **It fails at initialization, nearly always.** When a session loads, AUM
  draws Perforator's node with the red striped ring of a plugin that did not
  come up: after each of the seven loads of sessions that hosted it on the
  rig in one night. Twice the ring was grey again a minute later; the other
  times it stayed failed until it was reloaded, once noticed only during a
  pass. A **reload** of the node (`aum_node reload`, or
  RELOAD in its menu) brings it back, **at Init**: all sixteen steps open,
  nothing gated, until the preset is loaded again. A song's sequencer sends
  the opening presets at the start of the transport, so a reload before a
  pass is enough; a reload during one leaves the rest of the pass ungated.
- **Keep it out of a stem.** `record_song_stems` loads a stem's session and
  records at once; the load's check notes a failed node and records anyway,
  and nothing can reload it in between. So the pad was recorded dry as a stem
  of its own, and Perforator sits on the pad's file track in the mix song:
  `load_song`, `aum_node reload` on the failed node, then `play_song`, whose
  sequencer loads the preset at the transport's start. It stayed up through a
  measurement and the take. Since then the load reloads a failed node itself
  (its reload step, up to three times) and `record_song_stems` records no
  stem over one that stays failed ([songs.md](../songs.md#loading)).
- **How much it takes.** The pad's track in the mix read -32.3 dBFS rms where
  the gate ran and -25.4 in the break, where a scene bypassed it and the pad
  plays other chords: about 7 dB, so the pad's track is 5 dB up in the mix.

## RoughRider3

A compressor with `Sensitivity`, `Ratio`, `Attack`, `Release`, `Makeup`,
`Mix`, `Input Lvl`, `Output Lvl`, `SC HPF`, `SC Active` (Off/On) and
`Full Bandwidth` (Off/On). The song puts "OG - 02 - Hard Pump" at `Mix` 0.45
(parallel) before the limiter on the master. In a song of stems the master's
effects are not in any stem: they run on the sum in the mix, which is where a
pumping compressor belongs, every stem ducking under the kick together.

With `Mix` 0.45 and the limiter's `Gain` at 4 dB every section of the mix
read -9 to -10 dBFS rms: the filtered intro as loud as the last chorus, the
build gone. At `Mix` 0.35 and `Gain` 1 the take read -12.1 dBFS mean, peak
-0.95.

## Humanoid

A voice processor: it takes audio in and gives it a pitch, a formant and a
robot's timbre. Its presets come in six groups: TUNER (61), VOCO (39, a
vocoder), HARMONY (28), SYNTH (16), DRYISH (18), INSTRUMENT (18), and a set
from its first year that includes "BADHABIT - TALKBOX" and several vocoders.
With `midi: true` on the effect it hears the track's notes.

**Where the pitch comes from** is a switch of four positions in its PITCH
panel, **scale, note, midi, lock**, and it is **not a parameter**: a preset
brings it.

| Mode | The pitch is | What the rig showed |
|------|--------------|---------------------|
| scale | the input's, pulled to the nearest note of a key | "BADHABIT - TALKBOX" is in C major: a J6 lead's C#5 came out D5, its F#4 as F4 |
| note | one fixed note | "97 DAFT FUNK" sang G2 for every note played (E5, A4, C5) |
| midi | the notes it hears | not tried: no preset was found in this mode |
| lock | (not looked at) | |

`3. Scale Bypass` (labels OFF/ON) is a parameter: **ON** lets the input's pitch
through in scale mode. With it, a synth that already plays the right notes
keeps them and Humanoid adds the voice: the talk box's C#5, F#4 and E5 came
out at those pitches. Whether it changes anything in note mode was not tried.

What the song does with it:

- **Talk box**: J6 "MP Wah Quack Wow" (a wah that opens on every note: the
  mouth) into "BADHABIT - TALKBOX" with Scale Bypass ON.
- **Robot vocal**: the chorus hook, first written for a NuSaw supersaw that
  was too strong a lead, into "FACTORY - 1. TUNER - SINGING DROID" with Scale
  Bypass ON. That preset sings an octave below its input, so the hook sounds
  around E4 to A4, a man's range; the talk box doubles it an octave above in
  the last chorus.

## Not known

- Filterstation2's `Routing`, `Type` and `Hack` steps, and what `Flt n Freq`
  is in Hz.
- What makes Perforator fail at initialization. A session of four strips
  failed as often as one of twelve.
- A Humanoid preset in midi mode, which would let a vocoder's carrier play the
  song's notes over any modulator.
