# Unfiltered Audio Battalion

Battalion is a drum machine of eight voices. Each voice is a synth engine and a
sampler side by side, with two envelopes, two LFOs, a filter, a distortion and
a sequencer lane of its own. This note is what it takes to drive one from the
daemon: over MIDI, through its parameters, and in a [song](../songs.md).

The AUv3 is `aumu / uaBL / UnAu`, "UA Battalion", version 1.2.0 where this was
measured.

## Sources

- The manual (Michael Hetrick, 80 pages, written for 1.0.5):
  <https://files.plugin-alliance.com/products/unfiltered_audio_battalion/unfiltered_audio_battalion_manual.pdf>.
  Page numbers below are its printed ones.
- The maker's release notes for 1.1 to 1.2: <https://www.unfilteredaudio.com/pages/beta>.
- Product pages: <https://www.unfilteredaudio.com/products/battalion>,
  <https://www.plugin-alliance.com/products/battalion>.
- The plugin's parameter tree as the probe dumps it, its saved state as AUM
  keeps it in a session file, and what a device showed (marked *measured*).

What comes from a forum post by one of the two developers is marked *dev*
(the plugin's thread on the Elektronauts forum,
<https://www.elektronauts.com/t/unfiltered-audio-battalion-soft-drum-synth/211853>).

## MIDI

Battalion has a fixed MIDI layout by channel (manual pp. 76-78). Nothing in
the plugin remaps it.

| Channel | Notes | Does |
|---------|-------|------|
| 1 | C, C#, D, D#, E, F, F#, G of any octave | trigger voice 1 to 8 at its own pitch |
| 2 to 9 | any | play voice 1 to 8 chromatically, one voice per channel; note 60 is the voice's own pitch |
| 10 | 0 / 1 / 2 | sequencer: toggle / play / stop |
| 10 | 12-19, 24-31 | toggle mute, toggle solo of voice 1 to 8 |
| 10 | 36-43, 48-55 | mute, solo of voice 1 to 8 while the key is held |
| 10 | 60-67 | choke voice 1 to 8 |
| 11 | 0 / 1 / 2 | previous / next / random preset |
| 11 | 3 | randomise every parameter |
| 11 | 12 / 13 / 14 | Performance page: randomise / reset / commit |
| 11 | 24-31 | randomise voice 1 to 8 |

The bundled device type `battalion` is this table: a note control for every
entry (`voice_1`, `play_voice_3`, `sequencer_stop`, `mute_2`, `preset_next` …),
each on its own channel. Bound to the endpoint that reaches the plugin, it
works wherever the session routes those channels to the instrument. A song's
session routes each instrument its track's channel and the track's
`control_channels`: a Battalion on a track on channel 1 with
`control_channels: [10, 11]` plays the song's pads and hears its mutes, solos,
chokes and randomise notes (see [In a song](#in-a-song)). Notes 0 to 3 of
channel 11 load presets or randomise every parameter; a song sends none of
them.

*Measured:* on channel 1 the eight pads repeat in every octave (24-31, 36-43,
48-55, 60-67 …), each octave the same sound, and G# to B are silent. A scan
for a kit's pads therefore needs eight notes, 36 to 43, and a wider one finds
the same eight pads again.

- **Velocity** scales each envelope's peak by that envelope's Velocity knob
  (50 % in the default patch, so soft and hard hits differ less than one
  expects) and is a modulation source of its own (p. 14, 16).
- **Note length** does not matter: both envelopes are attack, hold, decay,
  with no sustain (p. 15).
- **No MIDI out**, also not from its sequencer (1.2.0).
- **No CC map.** A parameter moves when the host maps it; see
  [Parameters](#parameters).
- Since 1.2.0 a note from outside takes the values of the sequencer step that
  is active when it arrives, also while the transport stands (*dev*): the
  PITCH lane transposes it, the VEL lane scales it (down to nothing below the
  middle, up to 20 % louder above it, *dev*), the PROB lane can drop it, a
  ratchet repeats it. A global option of the plugin turns that off (*dev*). A
  preset whose lanes are neutral leaves notes alone; the Performance page's
  probability and ratchet still reach them (see [Variation](#variation)).

## A voice

```
synth engine ─┐
              ├─ blend ─ distortion ─ filter ─ gain, pan ─┬─ output pair
sampler ──────┘     (POST puts the distortion after)      ├─ delay send
                                                          └─ reverb send
```

### Synth engines

Every engine has PITCH (semitones; 0 is a C at 130.81 Hz for most engines, an
octave lower for Kick and Tripwire) and four knobs of its own (pp. 46-70). In
the parameter tree they are `Param 1` to `Param 4`; the names below are what
the window shows for each engine (*measured*: every engine was selected
through its parameter and its knobs read off the window).

| Engine | What it is | Param 1 | Param 2 | Param 3 | Param 4 |
|--------|-----------|---------|---------|---------|---------|
| Kick | oscillator with a pitch envelope | pitch decay | pitch depth | waveform | sub (a sine an octave down) |
| Hat | noise and a stack of squares | blend | sample rate | cutoff | harmonic |
| Snare | noise burst over a pitched body | pitch decay | pitch depth | cutoff | sample rate |
| Clap | retriggered noise and a tail | claps | spacing | cutoff | reverb decay |
| Modal | decaying sines tuned like a struck object | harmonics | dampen | vary | bright |
| Cowbell | two squares and noise | filter | click | noise | harmonic |
| FM | two operators, carrier feedback | ratio | depth | feedback | env |
| Additive | up to 24 partials | frequency ratio | amp ratio | partials | amp envelope |
| Chaos | two oscillators modulating each other; not for melodies | ratio | chaos | filter | chaos envelope |
| VOSIM | two sines windowed by a saw; vocal | partial 1 | partial 2 | FM | FM envelope |
| SumSyn | summation synthesis; kicks, tonal percussion | ratio | bright | PM | PM envelope |
| Reson | noise through a resonant filter bank | harmonics | dampen | material | ring |
| Combinatoric | sine through a modulated comb | comb ratio | feedback | pitch envelope | FM |
| Kombucha | Karplus-Strong: a string at high feedback, a snare at low | sample rate | feedback | pitch envelope | damp |
| Pillow | damped, clicky kick | pillow | click | rattle | sub |
| Zap | kick with an exaggerated sweep; lasers | env | click | overtone | sub |
| Radiator | noisy, burnt kick; a snare when pitched up | env | tick | overtone | roast |
| LPG | low-pass gate; the modular bongo | fold | shape | noise | damp |
| Terraining | two shaped oscillators through a wave terrain | pitch 2 | shape 1 | shape 2 | terrain |
| Shame | two FM oscillators into sample-and-hold mixers; digital | pitch 2 | FM | feedback | shame |
| Entangled | the same with gentler mixers | pitch 2 | FM | feedback | tangle |
| Haywire | a more acoustic snare; takes ratchets well | overtone | pitch depth | bright | rattle |
| Tripwire | Haywire's kick | pitch decay | pitch depth | transient | square |
| Builder | an oscillator and a noise section to shape yourself | oscillator wave | oscillator gain | noise tilt | noise gain |
| Scrapyard | eight tuned sines modulated by a ninth; metal | ratio | zap | material | FM |

STEREO makes an engine produce a stereo signal, each in its own way, at more
CPU.

*Measured:* PITCH moves in whole semitones, 24 either way: semitones =
round((v - 0.5) x 48), so v = 0.5 + st/48 reaches each semitone through a
CC, and a kit can be tuned to a key. With their other knobs at the default
patch's values, these engines hold a pitch to write for (note 60 on the
voice's chromatic channel):

| Note 60 sounds | Engines | `transpose` |
|----------------|---------|-------------|
| C3 | Additive, VOSIM, LPG, Haywire, Builder; Kombucha at high feedback | 12 |
| C5 | Modal, Reson, Scrapyard | -12 |
| C2 | Pillow, Zap, Radiator; Tripwire 23 cents sharp | 24 |
| C1 | Kick, with its pitch envelope nearly off (held notes read sharp otherwise: +10 cents with pitch decay 0.1 and depth 0.15) | 36 |

Entangled sounds 40 cents sharp, Shame 20 and FM 59 at their defaults.
SumSyn, Terraining, Combinatoric, Chaos, Cowbell, Hat and Clap give no note to
write for. Modal does not answer velocity evenly (a note at 90 rang shorter
than at 50 and at 127); Additive does.

### Sampler

START and END markers (END before START plays backwards), PITCH, and four
knobs that are neutral at 50 % (pp. 6-8, 72-75; the last three modes came
after the manual, their knob names are read from the plugin's window):

| Mode | What it is | Param 1 | Param 2 | Param 3 | Param 4 |
|------|-----------|---------|---------|---------|---------|
| Classic | plain playback | fine | tilt | comp | gain |
| Phase Warp | the sequencer's time bending, on the playhead | mirror | shape | split | swing |
| Cloner | spectral resynthesis, a little different each hit | fine | tilt | variation | damp |
| Granular | grains; pitch and speed apart | size | rate | speed | blur |
| Phase Slice | jumps between slices of the sample | slices | chance | algo | window |
| Phase Stretch | warps the slices in time | slices | depth | algo | window |
| Garbage-Fi | playback with its accuracy taken away | clock | stutter | hiccup | bits |

Samples are referenced by path, not stored in a preset. The default patch has
none loaded.

### Envelopes, LFOs, modulation

- Two envelopes, attack / hold / decay with a CURVE for the decay. The synth
  follows Envelope 1 unless its ENV 2 switch is on; the sampler follows
  Envelope 1, Envelope 2 or none (the whole sample plays).
- Two LFOs: speed in tempo units or Hz, an amount, a shape (wavetable,
  triangle tilt, pulse width, smooth random) and a reset mode (free run,
  always reset, soft reset, sample and hold, one shot). One Shot with Triangle
  Tilt is a third envelope.
- Eight modulation sources per voice (LFO 1 and 2, Envelope 1 and 2, Random,
  Velocity, Macro 1 and 2), each with a bipolar depth to every sound
  parameter of the voice. Those depths are parameters too: 296 a voice. Random
  draws a new value from 0 to 1 on every hit; Velocity is the hit's velocity
  from 0 to 1 (p. 14).
- MACRO 1 and 2 are knobs that do nothing but modulate, so one of them moves
  everything it is mapped to.
- VARIATION adds a little randomness to nearly every control of the voice on
  each hit (p. 19). A control locked in the window is left out.

*Measured:*

- A depth d adds (2d - 1) x the source to the target's value (0 to 1). An LFO
  runs from 0 to 1, or from -1 to 1 with BIPOLAR on. So a depth above 0.5
  only adds what Random or Velocity bring, one under 0.5 only takes away. For
  pitch the full depth is 48 semitones: Random at 0.52 gives 0 to 2
  semitones, at 0.56 0 to 6.
- LFO speed, tempo-synced, as a period in beats: 0.3 is 2.67, 0.4 is 1.5, 0.5
  is 1, 0.6 is 0.75, 0.75 is 0.375, 1.0 is 0.125; 0.18 and less is ten beats
  or more. Free: 0.18 is 0.5 Hz, 0.3 is 3.5 Hz, 0.4 is 11 Hz.
- An LFO in sample and hold that is tempo-synced gives hits on a regular grid
  nearly the same value each time; free, every hit gets another.

### Tone and mixer

- **Filter**: Scream (resonance unrestricted) and Dark (muffled) as low, high
  and band pass; five comb filters, where cutoff is the pitch and resonance
  the feedback.
- **Distortion**: Bitcrush, Rate Crush, Wavefold, Phase, Tape, Tube,
  MiniGlitch, MegaGlitch, Contrast, Square, Dropout. Tape and Tube colour the
  sound at 0 already.
- **Trigger** names a voice that fires whenever this one does (a click on a
  kick). **Choke** names a voice this one silences (a closed hat choking the
  open one), or the delay or the reverb, which gates that effect.
- **Output bus**: the main pair or one of eight more. Only the main pair runs
  through the master section.

*Measured:*

- **Trigger and Choke entries.** A number is that voice. "1-4", "5-8", "Odd"
  and "Even" are those voices, all at once. "Last" is the voice that was
  played last before this one: hit after voices 6, 8 and 2 in turn, voice 1
  fired 6, 8 and 2. "Random" behaves as if its random value moved on
  only while the sequencer runs. With the transport stopped it is the
  same voice on every hit (voice 2 on 50 of 50 hits in one session, voice 5
  in another); with it running it is another voice on nearly every hit,
  for the generator's hits and for notes from outside alike (25 hits fired
  voices 2 to 7 in no order). A song played from its session's sequencer runs
  the transport. Chains run on (1 fires 2 fires 3 fires 4 from one hit); a
  loop does not run away, each voice fires once. Choke "Delay" and "Reverb"
  cut that effect's tail to silence.
- **A voice's Out Gain** is cubic: dB = 60 x log10(v / 0.68), so 0.8 is +4 dB
  and 0.5 is -8 dB. Velocity 40 against 127 is only 3.5 dB at the default
  velocity depth.
- **The filter**, Scream LP on noise: cutoff 1.0 leaves its spectral centroid
  at 8.4 kHz, 0.8 at 5.4, 0.6 at 2.6, 0.4 at 0.8 kHz; 0.2 leaves next to
  nothing.

## The sequencer

Each voice has a sequencer (pp. 34-44): steps drawn by hand, and a generator
that adds hits of its own. A preset holds one sequence; there is no pattern
bank.

| Control | Does |
|---------|------|
| STEPS, FILL | the generator spreads FILL hits evenly over STEPS steps (a Euclidean rhythm) |
| PATTERN | picks a permutation of that distribution; the first is the plain one |
| ROTATE | shifts the generated hits by whole steps |
| INVERT | swaps hits and rests |
| DENSITY | a chance for the whole voice: below the middle every hit fires less often, above it empty steps start to fire |
| SWING, SHIFT, SHAPE | every second step later or earlier; the whole voice ahead of or behind the beat; time bent across each group of STEPS steps |
| SPEED, MULT | the length of a step (1/16 by default; SEQ TEMPO UNIT also has 1/5, 1/6, 1/7, 1/9 to 1/15) |
| LENGTH, SNAP | the length of the drawn sequence, 1 to 64 steps. With SNAP off the generator's STEPS and the LENGTH run against each other |
| RESET | restart both after a number of steps |
| HUMANIZE | for all voices: small timing offsets that hang together |

Under the steps are ten lanes with a value per step: VEL, SHIFT, PITCH, RATCH
(retriggers within the step), ENV (envelope length), PAN, VAR, PROB, SENDS
(up for delay, down for reverb) and MACRO 2. A lane has a length of its own
and follows the steps (SYNC), runs free (FREE), or advances only when a hit
fires (TRIG, RETRIG: the latter also restarts with the steps). PROB in the
middle is a sure hit, lower is less likely, higher adds a chance of a double
hit (p. 37). A lane of another length than the steps, or one that advances
only on hits, gives a pattern that comes round in another place every time:
the maker's advice for patterns that evolve (*dev*).

The lengths and modes of the lanes are parameters (`seqChan<N>probSeqLength`,
`seqChan<N>probSeqMode` …); the values of the lanes and the steps drawn by
hand are not. They are in the plugin's saved state, which AUM keeps in the
session file: JUCE's binary XML (the header `VC2!`, a length, the XML text),
whose `sequences` element holds for each voice 64 values of each lane as a
comma-separated list (`fills0` are voice 1's steps, 0 or 1; `velocities0`,
`shift0`, `pitches0`, `ratchets0`, `lengths0`, `pans0`, `variations0`,
`probabilities0`, `sends0`, `mod0`). Neutral is 0.5, and 0 for steps and
ratchets. The same state holds every parameter as `<PARAM id=… value=…>` in
the units the window shows, the sample paths, and the transport state.

**Transport.** The sequencer follows the host's tempo, and by default its
transport: it plays while the host plays. Its Stop button makes it ignore the
host until Play is pressed again; channel 10 note 2 does the same. There is no
parameter for that state.

*Measured:* a generator's step is that fraction of a whole note: 5 steps of
1/5, 7 of 1/7, 9 of 1/9 and 11 of 1/11 each turn once in a 4/4 bar.

## The Performance page

Every knob of this page is an offset to the same knob of every enabled voice:
the envelopes, the engine's and the sampler's knobs, cutoff and resonance,
variation, the macros, and of the sequencers density, steps, fill, pattern,
probability and ratchet (pp. 29-33). MOD AMP scales every modulation depth.
Each voice can be left out (ENABLE) or turned the other way (FLIP). GLOBAL
DEPTH scales the whole page from -100 % to 100 %, and ENABLED switches it off.
It is the plugin's surface for changing a whole kit at once. RANDOMIZE sets
every knob of the page to a random value as deep as RANDOMIZE DEPTH allows,
RESET puts them all back to neutral, and COMMIT writes what they do into the
voices' own knobs and resets them (pp. 31-33); channel 11 notes 12, 13 and 14
press the three.

*Measured:* the page adds (2k - 1) x (2D - 1) to that knob of every enabled
voice, k being the page's knob and D its DEPTH: a decay offset of +0.3 took a
225 ms voice to 860 ms at +100 %, 485 ms at +50 %, 55 ms at -50 % and a click
at -100 %. Its pitch offset moves in semitones too (0.625 is an octave). MOD
AMP (`modulationDepth`) scales every modulation depth by 2v: 0 is none, 0.5
as set. Its probability and ratchet: see [Variation](#variation).

## Effects and master

- **Send A, a delay**: time (synced or free), feedback with a tilt filter,
  ping pong, GLITCH (the delay time wanders), ducking, a sample rate for the
  whole effect, and how much of it feeds the reverb.
- **Send B, a reverb**: ten modes (Dark, Rusty, SATREV, Glitter, Crayon,
  Bokeh, Flare, Flutter, Hollow, Austere; the manual describes none), pre-delay,
  decay, a tilt filter before and after, a sample rate.
- **Master**, on the main pair only: MAXIMIZE, a three-band EQ, the output
  gain, then a clipper (none, hard, soft, wavefold; soft by default).

*Measured:*

- **Delay time**, synced, in beats: 0.125 is 1/12, 0.25 is 1/6, 0.375 is 1/4,
  0.5 is 3/8, 0.625 is 3/4, 0.75 is 4/3, 0.875 is 8/3. Free: about 2 s x v.
- **Feedback** is a gain per echo: 0.5 is -6 dB, 0.8 -2 dB, 0.95 -0.5 dB, and
  1.0 holds without running away.
- **The delay's sample rate** is its clock: 0.5 doubles the echo time (and
  what is in the delay drops an octave), 0.2 makes it five times as long.
- **GLITCH** at 0.5 scattered a single hit's echoes 30 to 175 ms apart and
  20 dB quieter. DUCKING showed nothing on a single hit.
- **The reverb's modes**, time to -20 and -40 dB at decay 0.5: Dark 1.4 and
  3.7 s, Glitter 1.6 and 3.9, Bokeh 1.9 and 4.3, Flare 1.6 and 4.0, Flutter
  1.7 and 4.2, Hollow 1.6 and 3.9. Rusty, SATREV, Crayon and Austere start
  more than 20 dB under the hit: thin. Dark at decay 0.27 is at -40 dB after
  2.1 s, at 0.7 after 5.1 s.
- **The first hit** that reaches the reverb after the plugin has loaded gets
  no reverb; every later one does.

## Presets

A preset is a whole kit **with its sequence**. Many factory presets carry a
tempo in their name; that is the tempo they were written at, not one they set.
Loading one over a Program Change replaces all of it. The window also keeps
presets of a single voice, which a host cannot load.

## Parameters

The plugin publishes 3225 parameters, all writable, in groups:

| Group | Parameters |
|-------|-----------|
| Drum 1 … 8 | 63 each: both envelopes, both generators, blend, variation, filter, distortion, mute, solo, gain, sends, pan, output bus, trigger, choke, both LFOs, both macros |
| Drum 1 … 8 Modulation | 296 each: a depth from each of the eight sources to each of 37 targets |
| Seq. Channel 1 … 8 | 35 each: the generator, the modifiers, the length, and a length and mode for every lane |
| Effect | 6: maximize, the EQ, the output gain, the clipper |
| Send A, Send B | 11 and 8 |
| Performance | 46 |
| Sequencer | 2: humanize, mult |

The steps and lane values of a sequence are not parameters. Almost every
parameter runs from 0 to 1 whatever its knob shows; AUM's MIDI Control panel
lists each with the range the plugin displays. Envelope times are the square
of the value times the longest time: 0.2 of 5 s is 200 ms.

The groups are groups of the plugin's parameter tree, and that decides where a
host keeps a mapping. See [AUM session format](aum-session.md): AUM files a
parameter under its key path, one collection per group.

## Measured on a device

AUM on an iPad, Battalion 1.2.0, in sessions authored from songs, 2026-11-11.
Levels and pitches are read at the session's audio tap.

- **MIDI.** Channel 1 as above. Channels 2 and 3 play voices 1 and 2
  chromatically and in tune (within 5 cents from note 60 to 84). Note 60 is the
  voice's own pitch: C1 (32.7 Hz) for the Kick engine at its default pitch, C3
  (130.8 Hz) for Kombucha and Additive. About two octaves under note 60 the
  pitch stops falling. Channel 10: note 2 stopped the sequencer under a
  running host, and it stayed stopped over a host stop and start until note 1;
  note 0 toggled it; note 12 muted and unmuted voice 1. Channel 11: note 1
  and note 0 stepped to the next and the previous preset.
- **Level.** A voice of the default patch peaks around -4 dBFS with the strip
  at unity, many factory presets at 0 dBFS. A factory pattern playing read
  -10 dBFS rms with its peaks at full scale. Keep the strip near -10 dB, or
  let a calibrated song set it.
- **The sequencer runs with AUM's transport.** With a factory preset loaded,
  starting the transport plays the preset's pattern. A song that is played
  from its session's sequencer runs the transport, so it gets that pattern on
  top of its own notes unless it silences it.
- **Density silences it, and notes still play.** A voice's Density at 0
  (-100 %) stopped its hits; all eight at 0, or Performance Density at 0,
  silenced a preset's whole pattern (digital silence once the effects had
  rung out). With the transport running and the pattern silenced that way, 14
  of 14 notes on each of two voices sounded.
- **The generator is playable through parameters.** In the default patch,
  whose sequence is empty, Pattern Fill above 0 makes the voice play with the
  transport, and Steps, Fill, Rotate and Density change the rhythm as they
  are moved.
- **A preset load.** A note 50 ms after the Program Change sounded (the old
  kit's sound until about 150 ms, the new one's from 300 ms). With the
  transport running, the pattern of the new preset took over after a gap of
  about 0.2 s.
- **Parameters.** A CC mapped to a parameter moved it once the mapping sat
  where AUM keeps it (see [AUM session format](aum-session.md)); 857
  parameters were mapped in one session. Each of the 25 engines was reached
  through the engine selector's CC: AUM rounds to the nearest index.
- **The middle of a knob.** A CC has no value for exactly 0.5: 64 is 50.4 %,
  63 is 49.6 %. A parameter a song declares at 0.5 therefore goes out as
  50.4 %. On a knob that does nothing in the middle that is a small offset,
  but not everywhere: Density just above the middle lets every empty step fire
  once in a while; a pitch modulation depth there is +0.8 % of 48 semitones,
  and four of them detuned a voice by one to two semitones; Mult ran its
  generator 2 % fast; the Performance page's probability at 49.6 % dropped 3
  of 128 steps of a voice. Declare no bipolar knob at its middle unless the
  song moves it, and write 0.496 where it must sit near it.
- **The lead-in.** A song played from its session's sequencer starts with a
  bar of lead-in, in which the opening section's values are sent. The
  plugin's sequencer is already running then and plays them. A song whose
  drums come from the generators opens with them empty and fills them from
  bar 1.
- **Four instances** in one session loaded and played. AUM took over a
  minute to bring them up, and its UI did not answer for most of that time.
  `load_song` waits for the session's name as long as AUM shows that it is
  loading (up to 2 minutes), so such a load is reported as verified;
  `check_song` found every track.

How the sequencer's parameters run, read off the plugin's window (v is the
parameter's value, 0 to 1):

| Parameter | Shows |
|-----------|-------|
| Pattern Steps | 1 + 15·v, rounded: 0 is 1 step, 1 is 16 |
| Pattern Fill | v × steps, rounded: a share of the steps |
| Pattern | the permutation, as a share of how many there are |
| Rotate | (2v − 1) × steps: 0.5 is none |
| Shift, Swing | ±(2v − 1)², in percent: 0.25 is -25 % |
| Shape, Density | (2v − 1), in percent: 0.25 is -50 % |
| Mult | /32 at 0, 1x at 0.5, 32x at 1 |
| Reset Length | 2 + 1022·v steps |
| SEQ LENGTH | 1 to 64, one per index |

Elsewhere: an envelope time is v² times the longest (5 s; 20 s for Envelope
2's hold), a voice's and the master's gain are 0 dB at 0.68, a send is off at
0, and a voice's Synth Pitch is round((v - 0.5) x 48) semitones.

## Variation

A Battalion pattern need not play the same bar twice, and a hit need not
sound like the one before it. The plugin varies at three levels: each hit's
sound, each bar's pattern, and the kit as a whole. What it offers, and how a
song reaches it:

| Means | Varies | A song reaches it by |
|-------|--------|----------------------|
| VARIATION of a voice | every hit: nearly every knob of the voice | `drum<N>variation` |
| VARIATION of the Performance page | every enabled voice's VARIATION | `performvariation` |
| the Random source | every hit: any of 37 targets of the voice | a depth, `drum<N><target>random` |
| an LFO in sample and hold, or smooth random | every hit, or slowly over bars | `drum<N>lfo1resetMode`, `drum<N>lfo1shapeMode`, a depth `drum<N><target>lfo1` |
| the Velocity source | every hit, by its velocity | the song's velocities, a depth `drum<N><target>velocity` |
| MiniGlitch, MegaGlitch | every hit: a small delay in the voice whose time wanders | `drum<N>distortMode`, `drum<N>distort` |
| the delay's GLITCH | the echoes | `sendAGlitch` |
| DENSITY | which of the generator's hits fire; ghost hits on empty steps | `seqChan<N>density`, `performdensity` |
| the PROB lane | each step's chance; double hits | not the lane; `performprobability` offsets every step of every enabled voice |
| the RATCH lane | retriggers within a step | not the lane; `performratchet` |
| the VEL, SHIFT, PITCH, ENV, PAN, VAR, SENDS and MACRO 2 lanes | per step | no: their values are in the saved state |
| a lane's length and mode (SYNC, FREE, TRIG, RETRIG) | where the lane's values fall against the steps | parameters (`seqChan<N>velSeqLength`, `seqChan<N>velSeqMode` …), with nothing to move while the lanes are neutral |
| PATTERN, ROTATE, INVERT, FILL, STEPS | the generator's arrangement | `seqChan<N>pattern`, `seqChan<N>euclidRotate` …; `performpattern`, `performfill`, `performsteps` |
| step counts and step units against each other; SNAP off | a polymeter: a bar comes back only after several | `seqChan<N>euclidSteps`, `seqChan<N>seqTempoUnit`, `seqChan<N>lockLength` |
| SWING, SHIFT, SHAPE | a voice's timing | `seqChan<N>swing`, `seqChan<N>shift`, `seqChan<N>shape` |
| HUMANIZE | every voice's timing | `humanize` |
| Trigger and Choke "Random", "Last" | which voice answers a hit | `drum<N>trigger`, `drum<N>choke` |
| randomise, reset, commit the Performance page | the whole kit's offsets at once | channel 11 notes 12, 13, 14, on a track with `control_channels: [11]` |
| randomise a voice | one voice's whole sound | channel 11 notes 24-31: likewise |
| mute, solo, choke | drop-outs and breaks | channel 10 notes, on a track with `control_channels: [10]` |
| the song's own notation | velocities, pattern lists that end differently, free events, ramps, scenes | the document |

Generator hits and notes from outside do not take all of it alike: DENSITY
and the Performance page's probability are made for the generator's hits.

### What a device showed

Battalion 1.2.0 in AUM, default patch and no preset, 120 BPM (a sixteenth is
125 ms); every parameter moved by its CC, the tap's capture analysed. The
spreads are over 16 hits, the patterns over 16 bars unless said.

- **VARIATION** gives every hit its own level, length and tone, and leaves the
  Pitch knob alone: a Modal voice stayed exactly on C5 at 0, 0.1, 0.25 and
  0.5. Kick engine: length 75-80 ms at 0, 60-95 at 0.1, 60-105 at 0.5, 45-120
  at 1.0; level spread (sd) 0.03, 0.2, 1.0 and 2.2 dB; its pitch sweep (an
  engine knob) moved about a semitone (sd) at 0.1. Modal: 125-140 ms at 0,
  120-150 at 0.1, 105-165 at 0.5. Generator hits vary the same as notes (Modal
  at 0.5: 105-175 ms over 33 hits).
- **The Performance page's VARIATION** adds (2v - 1) to every enabled voice's:
  at 0.75 the Modal varied as at a VARIATION of 0.5 (85-145 ms, levels over
  6.5 dB), at 1.0 as at 1.0 (70-190 ms, 12 dB).
- **The Random source**, on a cowbell: pan 0.35 with a depth of 0.65 put each
  hit between -2.9 and +2.5 dB right-minus-left (-3.9 dB without); a decay
  depth of 0.6 made the hits 95-335 ms long (85-100 without); a gain depth of
  0.4 took 0 to 5 dB off each hit; a cutoff depth of 0.3 (cutoff 1.0) moved
  the spectral centroid between 3.3 and 8.8 kHz (9.0-9.4 without).
- **MegaGlitch** at 0.5 spread a snare's length from 95-100 ms to 70-125 ms and
  gave most hits a stray echo 35-115 ms after; at 0.8, 20-210 ms and tails
  17 dB louder. MiniGlitch at 0.2 to 0.5 changed the length (50-105 ms) and
  echoed rarely.
- **DENSITY** under the middle lets each of the generator's hits fire with a
  chance of about 2v, above it each empty step with a chance of 2v - 1: with
  four hits a bar, 0.3 kept 58 % of them and 11 of 16 bars differed, 0.4 kept
  89 % (5 of 16 differed); 0.6 filled 21 % of the empty steps and 0.7 43 %,
  16 and 15 of 16 bars different. On notes from outside it does nothing: 36
  of 36 sounded at 0.3, 0.4 and 0.6.
- **The Performance page's probability** under the middle lets every step of
  the generator fire with a chance of 4v - 1: a hat on all sixteen steps kept
  77 % of 256 at 0.45, 66 % at 0.4, 36 % at 0.35, 20 % at 0.3 and nothing at
  0.25; every bar of 16 differed. Above the middle it doubles steps at random:
  47 % at 0.6, every step from 0.75. On notes from outside it is a switch, not
  a chance: at 0.48 all 36 notes sounded, at 0.46 and less none, with the
  transport running or not; at 0.55 every note was doubled.
- **The Performance page's ratchet** is a fixed roll, no chance: 2 hits a step
  at 0.6, 4 at 0.75, faster at 1.0, on the generator's steps and on notes from
  outside (36 notes, 144 hits at 0.75), with the transport running or not.
  ENABLE off kept a voice out of all of it.
- **HUMANIZE** moved each step evenly within about ±25 ms x v (at 120 BPM: a
  fifth of a sixteenth at 1.0): ±2.5 ms at 0.1, ±6 at 0.25, ±12.5 at 0.5.
- **PATTERN**: every value is another arrangement of the same hits, the first
  kept on the downbeat. Five hits in 16 steps gave 21 different bars for the
  values 0, 0.05 … 1.0, and 13 different bars for CC 0 to 12: each CC step is
  a new arrangement. A change takes effect at once, also in the middle of a
  bar.
- **Trigger "Random"** draws another voice on each hit only while the
  transport runs; see [Tone and mixer](#tone-and-mixer).
- **Channel 10**, to a Battalion on a track's own channel 10, its generators
  playing: note 38 held for 2 s muted voice 3 for those 2 s and no longer; note
  48 held soloed voice 1; note 14 toggled voice 3's mute.
- **Channel 11**, likewise: each note 12 gave the kit another state (at the
  default RANDOMIZE DEPTH of 0.1 the hat on sixteenths dropped to one hit and
  then none a second, the kick's hits grew 3 to 5 times longer; at 0.5 the hat
  turned into 6 ms clicks, then silence), and note 13 put every value back
  exactly. Note 26 gave voice 3 a new sound and a new rhythm on every press
  (onsets a second 12, then 3, 10, 8; 7 dB louder), with no way back but the
  parameters the song declares.

### What a song does not reach

- **The lanes and the hand-drawn steps.** Their values are not parameters.
  They are plain XML in the saved state the session file carries (see [The
  sequencer](#the-sequencer)), so a session author could write them: a VEL
  lane of 12 steps against a pattern of 16, a PROB lane in TRIG mode. Whether
  the plugin takes an authored state is not tried.
- **Two kits from one control channel.** A control channel belongs to one
  track, so of two Battalions in a song only one can hear channel 10; the
  other's mutes are out of reach.
- **Channel 11's notes 0 to 3** (C-1 to D#-1): previous, next and random
  preset and randomise everything. A song writes none of them. Nothing in the
  daemon refuses one, and a section sends only the controls that change, so
  what the song declares would not come back.

## In a song

A [song](../songs.md) sets a plugin's parameters by name, in a track's
declaration, in a scene, or as a ramp. Battalion's identifiers are regular:
`drum<N><what>` for a voice (`drum1mode`, `drum3env1decay`, `drum2sendB`),
`seqChan<N><what>` for its sequencer (`seqChan4euclidFill`), `perform<what>`
for the Performance page, `sendA…` and `sendB…` for the effects.
`get_song_plugin` lists them.

**A kit, played by the song.** A preset is a kit and a pattern. To use the
kit and write the pattern, silence the plugin's sequencer and play the voices.
The pads are the notes C to G of one octave; a calibrated song takes each
kit's lanes from the sound catalogue, so another preset in the chorus keeps
the patterns.

```yaml
  - id: drums
    kind: drums
    instrument:
      plugin: UA Battalion
      preset: Rebound
      params: {seqChan1density: 0, seqChan2density: 0, seqChan3density: 0, seqChan4density: 0,
               seqChan5density: 0, seqChan6density: 0, seqChan7density: 0, seqChan8density: 0}
scenes:
  chorus: {drums: {preset: View}}
```

**A kit from parameters.** Without a preset the kit is the default patch,
and every voice can be built in the song: an engine, its four knobs, an
envelope, a filter, a distortion, the sends. The song then moves them.

```yaml
      params:
        drum1mode: Kick
        drum1param1: 0.38        # pitch decay
        drum1param4: 0.65        # sub
        drum1env1decay: 0.3      # 450 ms
        drum3mode: Hat
        drum3env1decay: 0.09     # 40 ms
        drum3choke: "4"          # the closed hat cuts the open one
…
    automation:
      - {track: drums, param: drum3env1decay, from: 0.09, to: 0.22}   # the hat opens
      - {track: drums, param: performcutoff, from: 0.3, to: 0.5}      # the whole kit's filter
```

**The generators as the drummer.** No drum notes: each voice's rhythm is a
fill of a number of steps, set and moved by the song. Voices with different
step counts drift against each other. The transport has to run, so the song
is played from its session's sequencer (or live with `transport: true`).

```yaml
      params:
        seqChan1euclidSteps: 1.0       # 16 steps
        seqChan1euclidFill: 0.0        # empty until a section fills it
        seqChan4euclidSteps: 0.733     # 12 steps
scenes:
  groove:
    drums:
      params:
        seqChan1euclidFill: 0.25       # four on the floor
        seqChan2euclidFill: 0.125      # two hits ...
        seqChan2euclidRotate: 0.625    # ... moved onto beats 2 and 4
        seqChan4euclidFill: 0.4167     # 5 in 12
…
    automation:
      - {track: drums, param: seqChan3euclidFill, from: 0.0, to: 0.5}   # the hat fills up
```

**Voices as instruments.** A song's tracks take the MIDI channels in order,
and on channels 2 to 9 a Battalion plays one voice chromatically. The second
track's Battalion therefore plays its first voice as a bass, the third
track's its second voice, and so on. `transpose` brings the written notes to
the voice's pitch: 36 for the Kick engine, 12 for most others. Additive holds
a clean pitch; Modal and FM give their own.

```yaml
  - id: drums                     # channel 1: the kit
    kind: drums
    instrument: {plugin: UA Battalion}
  - id: bass                      # channel 2: voice 1, chromatic
    transpose: 36
    instrument:
      plugin: UA Battalion
      params: {drum1mode: Kick, drum1param1: 0.12, drum1param2: 0.2, drum1env1decay: 0.36}
  - id: pluck                     # channel 3: voice 2
    transpose: 12
    instrument:
      plugin: UA Battalion
      params: {drum2mode: Kombucha, drum2param2: 0.92, drum2env1decay: 0.42}
```

**A factory groove, bent.** The plugin plays its preset's own pattern with
the transport, and the song turns the Performance page: the groove comes in
hit by hit, opens, thins out and stutters, and a scene swaps in another
preset.

```yaml
    instrument:
      plugin: UA Battalion
      preset: "…a factory groove…"
      params: {performBypass: "On", performanceDepth: 1.0,
               performdensity: 0.496, performcutoff: 0.496, performdecay: 0.496, performratchet: 0.496}
…
    automation:
      - {track: drums, param: performdensity, from: 0.0, to: 0.496, bars: 6}
      - {track: drums, param: performratchet, from: 0.496, to: 0.85, bar: 3, bars: 2}
```

**A groove that differs in every bar.** The generators play; the kick and the
snare stay as written, and everything else varies: the Performance page
drops a share of the other voices' steps at random, a second hat comes in
only as ghost hits, VARIATION gives each hit its own sound and HUMANIZE its
own place. A scene re-arranges a voice's hits for another section.

```yaml
    instrument:
      plugin: UA Battalion
      params:
        seqChan1euclidFill: 0.25          # kick: four to the bar
        seqChan2euclidFill: 0.125         # snare: two hits ...
        seqChan2euclidRotate: 0.625       # ... moved onto 2 and 4
        drum3env1decay: 0.09
        seqChan3euclidFill: 0.5           # hat: eighths ...
        drum3variation: 0.15              # ... no two alike
        drum4mode: Hat
        drum4env1decay: 0.09
        seqChan4euclidFill: 0.0
        seqChan4density: 0.62             # a second hat, ghost hits only: about a quarter of the steps
        seqChan5euclidFill: 0.1875        # three hits of the Modal voice ...
        seqChan5pattern: 0.0
        drum5variation: 0.25
        performBypass: "On"
        performEnable1: "Off"             # kick and snare out of the Performance page
        performEnable2: "Off"
        performprobability: 0.45          # every other voice: 77 % of its steps
        humanize: 0.15                    # within about ±4 ms at 120 BPM
scenes:
  b:
    drums: {params: {seqChan5pattern: 0.3}}   # ... another arrangement of them
```

**Every hit its own, on a played part.** DENSITY and the page's probability
leave notes from outside alone or drop all of them, so a part the song plays
varies through the per-hit sources: VARIATION, the Random source with its
range set by the knob under it, the velocity the song writes, a glitch.

```yaml
      params:
        drum2variation: 0.15              # level, length, tone; the pitch stays
        drum3pan: 0.35
        drum3panrandom: 0.65              # each hat between 0.35 and 0.65
        drum3env1decayvelocity: 0.59      # accents ring longer
        drum2distortMode: MegaGlitch
        drum2distort: 0.3                 # now and then a stray echo
```

**Drop-outs and new states from channels 10 and 11.** The track keeps
channel 1 for the pads and lists 10 and 11 as its `control_channels` (see
[Control notes](../songs.md#control-notes)). A note on 10 mutes or solos a
voice for as long as it is held (C2 to G2 mute voices 1 to 8, C3 to G3 solo
them), C4 to G4 choke one; a note on 11 gives the Performance page a new
state (C0), puts it back exactly (C#0), or gives a voice a new sound (C1 to
G1). A control note goes with the pads as a free event with a `channel`, in
the same pattern: the patterns of a play list follow one another.

```yaml
  - id: kit
    kind: drums
    channel: 1                            # the pads: C2 to G2 play voices 1 to 8
    control_channels: [10, 11]
    instrument:
      plugin: UA Battalion
      params: {seqChan3euclidFill: 1.0}   # voice 3's generator: a hat on every step
patterns:
  beat:
    drums: {C2: "x...x...x...x...", C#2: "....x.......x..."}   # voices 1 and 2, from the song
  turn:
    drums: {C2: "x...x...x.x.x.x."}
    events:
      - {at: 0, dur: 0.25, note: C0, channel: 11}   # the Performance page: a new state
      - {at: 2, dur: 2, note: D2, channel: 10}      # the hat out for beats 3 and 4
  back: {channel: 11, step: 1/4, notes: "C#0 . . ."}   # the page put back; the hat alone
sections:
  verse: {bars: 8, play: {kit: [beat, beat, beat, turn, beat, beat, beat, back]}}
```

A Battalion whose generators play everything takes the same `control_channels`
and plays control patterns alone; the load check then skips the track, which
has no note of its own to play.

A parameter a song declares is sent again after every preset load, so it
holds over a preset's own value: declare only what the song means to set.

## Not known

- Whether a mute or solo from channel 10 also holds back the pad notes the
  song plays on channel 1, and whether a randomise from channel 11 reaches a
  kit while the song plays its pads: both were measured on the generators'
  hits only, with the plugin on a track of its own channel 10 or 11.

- Which step's lane values a note takes while the transport stands still
  (the Performance page's probability and ratchet reached notes then too, so
  the option that makes notes ignore the sequencer is off by default).
- Why the page's probability is a switch for notes from outside (between
  0.46 and 0.48) and a chance for the generator's hits.
- Whether HUMANIZE moves the voices together, as the manual says.
- Choke "Random" with the transport running; Trigger "Random" on voice 8.
- What lane lengths and the TRIG and RETRIG modes do, and whether the plugin
  takes lane values written into its saved state.
- The other voices' chromatic channels (4 to 9) and the rest of channels 10
  and 11 were not tried one by one.
