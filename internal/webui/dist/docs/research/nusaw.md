# NuSaw

NuSaw is a polyphonic supersaw synth by Charles Vestal, released in July 2026
as an AUv3 and a standalone app. It recreates the JP-8000's supersaw
oscillator and builds a small synth around it. This note is what it takes to
use one in a [song](../songs.md): its parameters as the daemon sees them, its
factory presets as the rig measured them, and what worked in a song of
sixteen tracks.

The probe id is `aumu-_nu_sw-_cvst`: 56 factory presets and 119 writable
parameters.

## Sources

- Jakob Haq's video "NuSaw is a JP-8000 style Supersaw synth for iOS"
  (<https://www.youtube.com/watch?v=pXGskNmRw9Q>), marked *video* below.
- Synth Anatomy's announcement:
  <https://synthanatomy.com/2026/07/charles-vestal-nusaw-a-roland-jp-8000-style-supersaw-synth-for-ios-and-macos.html>.
- The developer's thread on the Loopy Pro forum:
  <https://forum.loopypro.com/discussion/68818/nusaw-supersaw-synth-auv3-standalone-released>.
- The parameter tree as the probe dumps it, and what a device showed.

## The synth

- **Oscillator.** One supersaw of 3 to 25 voices with tuning and spread, plus
  a sine sub-oscillator with its own level and tune (*video*). The video
  describes it as the JP-8000's algorithm, detune scaling, centre spread and
  key-tracked high-pass included, with free-running saws computed at twice the
  sample rate (*video*).
- **Filters.** Filter 1 is low-pass or band-pass at 12 or 24 dB per octave,
  followed by a high-pass of fixed slope (*video*). An ADSR for the amp and
  one for the filter.
- **Effects.** Drive (amount, tone), the JP-8000's Super Chorus (depth, mix),
  a ping-pong delay that syncs to the host, a reverb (decay, damping); each
  with its own on switch.
- **Modulation.** Three LFOs of seven waves (sine, triangle, ramp up, ramp
  down, square, sample and hold, drift), each syncable and re-triggerable,
  and a matrix of six slots from seven sources to 28 destinations, the
  delay, the reverb and the gate among them (*video*).
- **Motion.** An arpeggiator (up, down, up/down, played, random; rate,
  octaves, transpose, swing, gate and a Chance of each step) and a gate
  sequencer of 16 steps (rate, steps, depth, smoothing, swing, length).
- **Tempo.** As an AUv3 it follows the host's tempo and transport; its own
  BPM is greyed out (*video*). The parameter is still in the tree
  (`int_tempo`).

## Parameters

All 119 parameters run from 0 to 1 and carry no value labels, the switches
and the stepped ones included. A song writes the number; `"60%"` is the same.

| Group | Parameters |
|-------|------------|
| Oscillator | `Saws` (voice count: 3 to 25 voices over 0..1, *video*; 0.18 in the default patch), `Detune`, `Spread`, `Sub`, `Sub Tune`, `Octave` (0.5 in the default patch) |
| Filter | `Cutoff`, `Resonance`, `Filt Env Amt`, `Key Track`, `Cutoff Slope` (12/24 dB), `Filter Type` (low-pass/band-pass), `HP Filter`, `Vel>Cutoff` |
| Envelopes | `Attack`, `Decay`, `Sustain`, `Release`; `F Attack`, `F Decay`, `F Sustain`, `F Release` |
| Voice | `Volume`, `Vel Sens`, `Bend Range`, `Glide`, `Mono`, `Retrigger`, `Pan` |
| Effects | `Drive On`, `Drive`, `Drv Tone`; `Chorus On`, `Chorus`, `Chr Depth`; `Delay On`, `Delay`, `Dly Time`, `Dly Fback`, `Dly Tone`, `Delay Sync`, `Delay Pingpong`; `Reverb On`, `Reverb`, `Rev Decay`, `Rev Damp` |
| LFOs | per LFO: `On`, `Wave` (seven waves in steps of 1/6), `Sync`, `Rate`, `Rate Free`, `Retrig`, `Fade` |
| Matrix | `Mod 1..6 Src` (steps of 1/7), `Mod 1..6 Dst` (steps of 1/28), `Mod 1..6 Amt` |
| Gate | `Gate` (on), `Gate Rate`, `Gate Steps`, `Gate Depth`, `Gate Smooth`, `Gate Swing`, `Gate Length`, `Gate Step 1..16` |
| Arp | `Arp` (on), `Arp Mode`, `Arp Rate`, `Arp Octaves`, `Arp Gate`, `Arp Swing`, `Arp Chance`, `Arp Transpose`, `Arp Hold` |
| Clock | `Tempo`, `Hold` |

The step sizes of `Wave`, `Mod Src` and `Mod Dst` are read off the default
patch's values (0.1667, 0.1429 and 0.1786, i.e. 1/6, 1/7 and 5/28). Which
source and destination each step is, was not read from the plugin's window.

## The factory presets, measured

Every preset was played once in the sound lab (one note at velocity 100,
held 0.7 to 1.5 s, fader at unity, no effects after the plugin) and measured
at the tap with `audition_sound`. Basses were played at C2, everything else
at C4. *Transpose* is what makes a written note sound as written, which a
calibrated song applies by itself.

| Preset | Played | Sounds | Transpose | rms dBFS | Attack ms | Held note | Remarks |
|---|---|---|---|---|---|---|---|
| Acid | C2 | C2 | +0 | -22.0 | 15 | sustains | +26 cents; a strong F3 and F#3 beside it |
| Trance Bass | C2 | F#0 | (+18) | -16.9 | 15 | decays in 40 ms | several notes at once; its pitch wanders over 2320 cents: no steady note |
| Sub Bass | C2 | C2 | +0 | -13.2 | 5 | sustains | the sine sub alone, the loudest preset |
| Reese Bass | C2 | C1 | +12 | -19.0 | 10 | sustains | |
| Reese Growl | C2 | C1 | +12 | -20.3 | 20 | sustains | |
| Reese Sub | C2 | C1 | +12 | -18.8 | 0 | sustains | |
| Reese Wide | C2 | F6 | (-53) | -21.3 | 270 | sustains | C2 as loud beside it: the F6 is a partial, not its note |
| Wobble Bass | C2 | C1 | +12 | -17.9 | 180 | sustains | starts 40 cents sharp |
| Pluck Bass | C2 | C1 | +12 | -20.6 | 45 | decays in 80 ms | |
| Festival Lead | C4 | C4 | +0 | -18.8 | 5 | sustains | an octave below it at -4 dB |
| Trance Gate | C4 | C3 | +12 | -21.8 | 0 | decays in 50 ms | its gate sequencer chops the note |
| Sunrise Lead | C4 | C3 | +12 | -19.0 | 5 | sustains | glides up 36 cents into the note in 125 ms |
| Anthem | C4 | C3 | +12 | -17.8 | 10 | sustains | |
| Hoover | C4 | C3 | +12 | -18.2 | 5 | sustains | C#4 almost as loud: the hoover's detune |
| Hardstyle | C4 | C3 | +12 | -21.2 | 5 | decays in 90 ms | |
| Venom | C4 | C4 | +0 | -22.2 | 35 | sustains | an octave below it at -10 dB |
| Anomaly | C4 | C3 | +12 | -18.7 | 10 | sustains | |
| Tempest | C4 | none | | -19.5 | 645 | sustains | no steady pitch: a swell |
| Solaris | C4 | C3 | +12 | -22.1 | 25 | sustains | |
| Habit | C4 | C4 | +0 | -20.4 | 275 | sustains | |
| Brimstone | C4 | none | | -47.1 | 840 | decays in 720 ms | a texture, very quiet |
| Corsair | C4 | C3 | +12 | -20.0 | 10 | sustains | |
| Afterhours | C4 | C4 | +0 | -20.5 | 20 | sustains | an octave below it at -5 dB |
| Genesis | C4 | C3 | +12 | -22.4 | 5 | sustains | |
| Sirocco | C4 | C5 | -12 | -18.2 | 95 | sustains | |
| Elysian | C4 | C4 | +0 | -19.0 | 85 | decays in 650 ms | |
| Terra | C4 | C4 | +0 | -18.8 | 100 | sustains | |
| Meridian | C4 | C4 | +0 | -19.1 | 90 | sustains | |
| Adagio | C4 | C4 | +0 | -16.7 | 40 | sustains | |
| Big Stab | C4 | C3 | +12 | -33.8 | 5 | decays in 95 ms | |
| Filtered Stab | C4 | C4 | +0 | -32.8 | 5 | decays in 90 ms | |
| Chord Gate | C4 | C3 | +12 | -23.4 | 5 | decays in 110 ms | gated |
| Dark Pad | C4 | C3 | +12 | -24.0 | 800 | sustains | |
| Glass Pad | C4 | C4 | +0 | -23.4 | 835 | sustains | |
| Evolving Pad | C4 | none | | -26.6 | 1435 | sustains | no steady pitch |
| LFO Motion Pad | C4 | C4 | +0 | -22.6 | 1265 | sustains | +30 cents |
| Lagoon | C4 | C4 | +0 | -23.7 | 665 | sustains | |
| Horizon | C4 | C4 | +0 | -22.6 | 760 | sustains | |
| Indigo | C4 | C3 | +12 | -26.2 | 1130 | sustains | falls more than four octaves into the note over 1.2 s |
| Parallax | C4 | C4 | +0 | -24.8 | 815 | sustains | |
| Vapor | C4 | C4 | +0 | -28.1 | 1015 | sustains | |
| Airstream | C4 | C4 | +0 | -21.9 | 165 | sustains | |
| Halcyon | C4 | C4 | +0 | -21.2 | 105 | sustains | |
| Warm Strings | C4 | C4 | +0 | -24.5 | 1040 | sustains | |
| Bright Strings | C4 | C4 | +0 | -23.3 | 215 | sustains | |
| Cinematic Strings | C4 | C4 | +0 | -23.8 | 695 | sustains | |
| Velvet Strings | C4 | C4 | +0 | -23.8 | 740 | sustains | |
| Cantabile | C4 | none | | -24.4 | 1040 | sustains | no steady pitch |
| Fanfare | C4 | C4 | +0 | -22.6 | 95 | sustains | |
| Arp Pluck | C4 | C3 | +12 | -24.8 | 130 | decays in 35 ms | its arpeggiator is on: one key plays a run |
| Arp Sequence | C4 | C3 | +12 | -24.3 | 5 | decays in 40 ms | arpeggiator on |
| Rolling Arp | C4 | C3 | +12 | -23.8 | 5 | decays in 65 ms | arpeggiator on |
| Organ | C4 | C3 | +12 | -17.4 | 0 | sustains | |
| Electric Piano | C4 | C4 | +0 | -24.7 | 25 | decays in 645 ms | |
| Piano | C4 | C4 | +0 | -21.1 | 5 | decays in 1030 ms | |
| Init | C4 | C4 | +0 | -17.0 | 5 | sustains | |

What the table says:

- **The octave is the preset's.** 22 of the 56 sound an octave below the
  written note, one an octave above, 27 at pitch, and six have no note of
  their own or name a partial (below). Leads, basses, arps
  and the organ are mostly an octave low; pads, strings and keys at pitch.
  A calibrated song needs no transposition for any of them.
- **The supersaw shows up as partials a semitone off.** Several presets
  report a C#4 or a B3 "not a harmonic" of a C3 note: the detuned saws, not a
  second note.
- **Levels sit close together.** 51 presets lie between -16.7 and -26.6 dBFS
  rms. Sub Bass (-13.2) is the loudest; Vapor (-28), the two stabs (-33) and
  Brimstone (-47) the quietest.
- **The pads are slow.** Fifteen presets take 0.6 to 1.4 s to their level. A
  pad that is to pump under a gate (see below) wants Adagio, Halcyon, Terra or
  Meridian, which are there within 40 to 105 ms.
- **Four presets have no steady pitch:** Tempest, Brimstone, Evolving Pad and
  Cantabile are swells and textures; Trance Bass and Reese Wide name a partial,
  so their transposition is in brackets and `calibrate` leaves them alone.

## In a song

A French-house song of sixteen tracks in seven stems (123 BPM, 3:50) gave
NuSaw the parts a supersaw is for and other synths the rest: a funk bass, an electric piano, a guitar, an organ, a talk box and brass
come out better from instruments made for them. What NuSaw played:

| Part | Preset | Set by the song |
|------|--------|-----------------|
| sine sub under the bass | Sub Bass | nothing |
| the pumped pad | Adagio | nothing; recorded dry, Perforator on its track in the mix ([why](house-effects.md#perforator)) |
| disco strings with runs | Bright Strings | nothing |
| the chorus hook | Anthem | nothing; Humanoid after it sings it, an octave down ([Humanoid](house-effects.md#humanoid)) |
| a sixteenth-note arpeggio | Rolling Arp | `Arp: 0` |
| the riser into each drop | Tempest | `Cutoff` ramped from 0.15 to 0.9 over four bars |

- **Write the arpeggio, switch the plugin's off.** The arp presets come with
  NuSaw's arpeggiator on, which plays its own run from every held note. With
  `Arp: 0` the preset is a pluck and the song's sixteenths are what plays.
  The gate presets (Trance Gate, Chord Gate) chop every note the same way:
  set `Gate: 0` when the rhythm is to come from the song or an effect.
- **A supersaw lead is a lot.** Anthem carried the chorus hook at the
  level of the drums and covered everything; put through Humanoid as a robot
  vocal and 3 dB down it sat at -21.6 dBFS rms against the drums' -17.9.
- **Cutoff ramps.** `Cutoff` is an ordinary parameter, mapped and ramped like
  any other: the riser is a held chord whose filter the section opens.
- **Light on the iPad.** AUM read 16 % DSP in a chorus of four drum tracks and
  five NuSaw instances (three of them playing), and 10 % idle in a session of
  eight NuSaw instances and two drum machines.

## Not known

- Which modulation source and destination each step of `Mod Src` and
  `Mod Dst` selects, and what the gate's and the arpeggiator's rate steps
  are: the plugin's window was not read.
- How `Saws` maps 0..1 onto its 3 to 25 voices.
- Whether a preset load resets the gate's and the arpeggiator's running
  position.
