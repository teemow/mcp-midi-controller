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
- The plugin's parameter tree as the probe dumps it, and what a device showed
  (marked *measured*).

What comes from a forum post by one of the two developers is marked *dev*.

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
  is active when it arrives: the PITCH lane transposes it, the VEL lane scales
  it, the PROB lane can drop it, a ratchet repeats it. A global option of the
  plugin ("MIDI Triggers Ignore Sequencer Data", *dev*) turns that off. A
  preset whose lanes are neutral leaves notes alone.

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
  parameter of the voice. Those depths are parameters too: 296 a voice.
- MACRO 1 and 2 are knobs that do nothing but modulate, so one of them moves
  everything it is mapped to.
- VARIATION adds a little randomness to nearly every control of the voice on
  each hit.

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
| SPEED, MULT | the length of a step (1/16 by default) |
| LENGTH, SNAP | the length of the drawn sequence, 1 to 64 steps. With SNAP off the generator's STEPS and the LENGTH run against each other |
| RESET | restart both after a number of steps |
| HUMANIZE | for all voices: small timing offsets that hang together |

Under the steps are ten lanes with a value per step: VEL, SHIFT, PITCH, RATCH
(retriggers within the step), ENV (envelope length), PAN, VAR, PROB, SENDS
(up for delay, down for reverb) and MACRO 2. A lane has a length of its own
and follows the steps (SYNC), runs free (FREE), or advances only when a hit
fires (TRIG, RETRIG).

**Transport.** The sequencer follows the host's tempo, and by default its
transport: it plays while the host plays. Its Stop button makes it ignore the
host until Play is pressed again; channel 10 note 2 does the same. There is no
parameter for that state.

## The Performance page

Every knob of this page is an offset to the same knob of every enabled voice:
the envelopes, the engine's and the sampler's knobs, cutoff and resonance,
variation, the macros, and of the sequencers density, steps, fill, pattern,
probability and ratchet (pp. 29-33). MOD AMP scales every modulation depth.
Each voice can be left out (ENABLE) or turned the other way (FLIP). GLOBAL
DEPTH scales the whole page from -100 % to 100 %, and ENABLED switches it off.
It is the plugin's surface for changing a whole kit at once.

## Effects and master

- **Send A, a delay**: time (synced or free), feedback with a tilt filter,
  ping pong, GLITCH (the delay time wanders), ducking, a sample rate for the
  whole effect, and how much of it feeds the reverb.
- **Send B, a reverb**: ten modes (Dark, Rusty, SATREV, Glitter, Crayon,
  Bokeh, Flare, Flutter, Hollow, Austere; the manual describes none), pre-delay,
  decay, a tilt filter before and after, a sample rate.
- **Master**, on the main pair only: MAXIMIZE, a three-band EQ, the output
  gain, then a clipper (none, hard, soft, wavefold; soft by default).

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
  63 is 49.6 %. On a knob that does nothing in the middle that is a small
  offset. For Density it matters: just above the middle every empty step
  fires once in a while. Write 0.496.
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
2's hold), a voice's and the master's gain are 0 dB at 0.68, and a send is
off at 0.

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
               performdensity: 0.496, performcutoff: 0.5, performdecay: 0.5, performratchet: 0.5}
…
    automation:
      - {track: drums, param: performdensity, from: 0.0, to: 0.496, bars: 6}
      - {track: drums, param: performratchet, from: 0.5, to: 0.85, bar: 3, bars: 2}
```

A parameter a song declares is sent again after every preset load, so it
holds over a preset's own value: declare only what the song means to set.

## Not known

- Whether "MIDI Triggers Ignore Sequencer Data" is on by default, and which
  step's lane values a note takes while the transport stands still.
- The meaning of the Trigger and Choke entries 1-4, 5-8, Odd, Even, Last and
  Random.
- The range of the Pitch knobs in semitones.
- The other voices' chromatic channels (4 to 9) and the rest of channels 10
  and 11 were not tried one by one.
