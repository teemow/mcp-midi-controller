# Songs

A song is a multi-track arrangement written as one YAML document. From it the
daemon makes two things: the **AUM session** that hosts the song's instruments
and effects, and the **MIDI** that plays it. AUM has no sequencer of its own, so
the session hosts two things that can play: a sequencer plugin with the whole
song written into it, which runs with AUM's transport, and the ProbeMidiBrain,
through which the daemon sends every note and control change when it is due.
[Playing](#playing) says which is used when.

This is what lets an agent be asked for a whole song — "four synths with their
effects, a drum machine, a different sound in the chorus" — and deliver one
that plays inside AUM.

## The loop

| Step | Tool | |
|------|------|-|
| 1. Look | `list_song_plugins`, `get_song_plugin` | Which instruments and effects are installed (the staged AUv3 probes), their factory presets and parameters. |
| | `open_sound_lab`, `audition_sound`, `list_sounds` | What a preset sounds like, before it is in a song. See [Sounds](#sounds). |
| 2. Write | `save_song` | Store the document. It is checked first, and reviewed as a song; see [What is checked](#what-is-checked) and [A full song](#a-full-song). |
| 3. Load | `load_song` | Author the song's `.aumproj`, have the iPad pull it, load it in AUM, wait for its brain and tap, and check that every track answers. See [Loading](#loading). |
| 4. Play | `play_song`, `stop_song`, `get_song_playback` | The whole song from the session's sequencer, or live: from a section, or only some tracks. |
| 5. Listen | `get_audio_tap`, `get_audio_clip` | The master strip carries a ProbeAudioTap, known to the daemon as `<session id>/master`. See [Taps](#taps). |
| | `mix_song`, `get_song_mix` | The level of every bar, every section and every track. See [The mix](#the-mix). |
| 6. Change | `get_song` → edit → `save_song` | New notes, patterns or arrangement play at once, live. The sequencer plays them after step 3. A change to what the song hosts or controls, to its tempo or to its session options needs step 3 before the song sounds right at all; `get_song` and `play_song` say when. |

`check_song` tells at any time whether the loaded session will sound.
`author_song_session` does only the first part of step 3: it builds the
session file and stages it.

`export_song_midi` writes the song as a Standard MIDI File for a sequencer or a
DAW.

`play_song` with `record: true` also records the song at the master's tap into
a file, and `upload_recording` puts that file into Google Drive: see
[Recording and Google Drive](recording.md).

## A full song

The document can hold any song. What comes out is still up to whoever writes
it, and the first eight songs written with these tools came out alike: one to
two minutes long, three to five tracks, loops of four or eight bars, ten of
the rig's 64 instruments and fourteen of its 92 effects, and a fade-out at
the end of every one. Each had taken the song before it for its model. So:

- **A stored song shows the format, not a form.** `get_song` is for reading
  how something is written. The next song gets a form, sounds and an ending
  of its own.
- **Length.** A full song runs 3 to 4½ minutes unless something else is asked
  for. In 4/4 that is minutes × tempo ÷ 4 bars: 105 bars for 3:30 at 120 BPM.
- **Form.** Decide where the song starts, where it peaks, and what the last
  chorus has that the first had not. A section that returns is a section of
  its own with something changed: a second verse with a track more, a last
  chorus with a lift. The same patterns under another scene or play list are
  a few lines. Give parts phrases of 8 or 16 bars beside the one- to four-bar
  loops, and let a repeat end differently with a pattern list
  (`[beat, beat, beat, fill]`).
- **Tracks.** Up to twelve. Beside drums, bass, chords and a lead there is
  room for a second harmony part, a counter-melody, percussion, a texture, an
  effects track. A track that rests is heard when it comes in.
- **Sounds.** Choose from everything installed, not from what the stored
  songs use: `list_song_plugins` lists the instruments and the effects, and
  `audition_sound` measures a preset in seconds. What it reports describes the
  preset. A layered fifth, a slow attack or a pitch that drifts is a character
  to write for, not a fault.
- **Movement.** [Automation](#sound) ramps any parameter over a section: a
  filter that opens into a chorus, a delay that grows into a break. A scene
  changes presets, bypasses and levels from one section to the next.
- **Variation.** A part that repeats need not sound the same twice. Some
  plugins vary themselves, and a song switches that on and moves it like any
  parameter: a drum machine that gives every hit its own level, length and
  tone, thins out, doubles and re-arranges its own pattern from bar to bar,
  and loosens its timing. A loop of four bars then differs in every bar
  while the song holds it for sixteen, and a section that returns can return
  with more or less of it. Such a plugin does not always vary the notes the
  song plays as it varies its own hits; the notation varies those
  (velocities, pattern lists, free events). A drum machine's own sequence
  varies by itself when its lanes are of other lengths than its steps, or move
  only when a voice fires: a song writes those with
  [sequence](#a-drum-machines-own-sequence). What one drum machine offers,
  and what of it a song reaches, is in
  [research/battalion.md](research/battalion.md#variation).
- **Ending.** End the song: a last chord or hit that rings out, a stop
  together on a beat, a last line left to one instrument. A fade-out is the
  exception.

An ending is a section like any other:

```yaml
patterns:
  last_chord: {step: 1/2, gate: 1, notes: "[A3 C4 E4] - | - -"}   # held for two bars
  last_note:  {bars: 2, step: 1/2, notes: "A1 -"}                    # one bar, then nothing
  last_hit:   {bars: 2, drums: {kick: "x", crash: "x"}}             # one hit on the one

sections:
  ending: {bars: 2, play: {pad: last_chord, bass: last_note, drums: last_hit}}
```

What rings on after the last bar belongs to the song. `play_song` with
`record: true` waits for that tail before it ends the recording.

### The review

`save_song` and `get_song` end their answer with a review of the song as
music. It is advice and never an error: a song that is meant to be short, or
to fade, is stored and plays as it is.

| About | The review says |
|-------|-----------------|
| `length` | how long the song is and how many bars a full one has at its tempo, when it is under 2:30 |
| `ending` | that it ends in a fade-out: level ramps take at least half of the tracks playing in the last section down by 12 dB or more, to the section's end |
| `form` | which section is heard three times or more unchanged, under whatever names; and that no track plays a phrase longer than four bars, in a song of sixteen bars or more |
| `tracks` | that it has five tracks or fewer |
| `sounds` | which instrument has the very preset another stored song has; and that none of its instruments, or none of its effects, is new to the rig's stored songs |

A stored song of under eight bars is a sound lab or a test: no other song is
compared with it. A song without a note gets no review.

## The document

```yaml
title: Night Drive
tempo: 104
time_signature: 4/4        # the default

tracks:
  - id: bass
    level: -4               # dB; 0 is unity
    instrument:
      plugin: mono          # a staged AUv3 probe id
      preset: Deep Bass     # one of its factory presets
    effects:
      - {id: drive, plugin: overdrive, bypass: true, params: {Gain: 0.5}}
      - {id: comp, plugin: compressor}

  - id: pad
    instrument: {plugin: poly, preset: Warm Strings}
    effects:
      - {id: reverb, plugin: hall, params: {Mix: 0.3}}

  - id: drums
    kind: drums
    instrument: {plugin: drumbox}

master:
  - {id: limiter, plugin: limiter}

patterns:
  bass_a:
    step: 1/8
    notes: "A1 A1 A1 A1 A1 A1 A2 A1 | F1 F1 F1 F1 F1 F1 F2 F1"
  chords:
    step: 1/4
    gate: 1
    notes: "[A3 C4 E4] - - - | [A3 C4 F4] - - -"
  beat:
    drums:
      kick:  "x...x...x...x..."
      snare: "....x.......x..."
      hat:   "o.x.o.x.o.x.o.x."

scenes:
  chorus:
    bass:
      effects: {drive: {bypass: false}}
    pad:
      preset: Synth Brass
      level: -3
      effects: {reverb: {params: {Mix: 0.5}}}

sections:
  verse:  {bars: 4, play: {bass: bass_a, pad: chords, drums: beat}}
  chorus: {bars: 4, scene: chorus, play: {bass: bass_a, pad: chords, drums: beat}}

arrangement: [verse, chorus, verse, chorus]
```

- **tracks** — each is one mixer strip: an instrument followed by its effect
  chain, on its own MIDI channel (the first track on channel 1, in order; up to
  twelve). `channel` puts a track on another of the twelve.
  `control_channels: [10, 11]` lets the track's instrument hear those channels
  as well, for one that takes control notes on a channel of its own, such as a
  drum machine's mutes on channel 10, while it plays the track's notes on the
  track's channel (see [Control notes](#control-notes)). `plugin` names a staged AUv3 probe: by its id as
  `list_song_plugins` gives it (the unit's type, subtype and manufacturer,
  such as `aumu-mono-_acme`), or by any name that fits only one plugin, its
  display name or a part of it. A name that fits several is an error that
  lists them. `kind: drums` makes it a drum track. `transpose` shifts all its
  notes, for a preset that does not sound at the written pitch (a
  [calibrated](#calibration) song needs none).
- **master** — effects on the master strip, after its fader.
- **patterns** — the phrases; see [Notation](#notation).
- **scenes** — named sound settings; see [Sound](#sound).
- **sections** — for a number of `bars`, every track listed under `play` plays
  its pattern, or its list of patterns one after another. Patterns repeat until
  the section ends and are cut there; a track left out is silent.
- **arrangement** — the sections in playing order. A section may appear more
  than once.
- **session** — optional: how the song's session is built beyond what the
  tracks say. See [The session](#the-session).

An instrument may also carry a `sequence`: a drum machine's own steps and
lanes, see [A drum machine's own sequence](#a-drum-machines-own-sequence).

Unknown keys are errors, so a misspelled field is reported rather than ignored.

## Notation

A pattern is written on a step grid. `step` is the grid's resolution: `1/16`
(the default), `1/8`, `1/4`, `1/2`, `1/32`, or a triplet such as `1/8t`.

**Melodic grid** — steps separated by spaces:

| Step | Meaning |
|------|---------|
| `A2`, `C#4`, `Eb3` | a note; C4 is middle C (MIDI 60) |
| `[A3 C4 E4]` | a chord |
| `-` | the previous note or chord keeps sounding |
| `.` | silence |
| `A2!` | an accent |
| `A2:70` | a velocity |

`notes` may be a list of lines: voices that sound together and must be equally
long.

**Drum grid** — one string per lane, one character per step: `x` a hit, `X` an
accented hit, `o` a soft one, `.` silence. The lanes `kick snare clap rim hat
open_hat pedal_hat tom_low tom_mid tom_high crash ride cowbell tamb shaker
clave` are General MIDI notes. A drum machine that puts its sounds elsewhere
gets a `drum_map` on its track (`{kick: C1, snare: D1}`), and a lane may also be
a note itself.

**Bar lines** — `|` marks a bar line in both grids. It takes no time, and it is
checked: once a grid uses bar lines, every bar must hold exactly a bar's steps.
Write them. A miscounted bar is then an error instead of a song that drifts.

**Free events** — `events: [{at: 0, dur: 1.5, note: "A2 E3", vel: 90}]` places
notes in beats from the pattern's start, for what a grid cannot say.

**The sustain pedal** — `pedal: [{at: 0.1, dur: 3.8}, {at: 4.1, dur: 3.8}]`
holds the pedal of the track's instrument down from `at` for `dur`, in beats
from the pattern's start, beside the pattern's notes, drums or events. It is
sent to the instrument as Control Change 64 on the track's channel, 127 down
and 0 up, so it does what the instrument makes of a pedal: a sampled piano
lets the notes ring on after their keys and adds the resonance of its open
strings.

- A pedal change is a pedal that comes up and goes down again: the end of one
  stretch and the start of the next, a little apart. Where the pedal goes down
  just after a chord is struck, the chord before it is damped and the new one
  caught, as a pianist changes the pedal.
- A stretch that begins while another holds the pedal takes over: the pedal
  comes up and goes down again where it begins.
- The pedal comes up with the section at the latest, as a note ends with it,
  and it repeats with its pattern. Nothing carries over into the next section,
  so a section sounds the same wherever the playback starts.
- `stop_song` lifts the pedal, in both ways of [playing](#playing).
- `save_song` says how often each track's pedal goes down.

Per pattern: `gate` is how much of a note's grid length sounds (default 0.9; 1
is legato), `velocity` the velocity of a plain note (default 96), `transpose` a
shift in semitones, and `bars` the length when the grid is shorter than the
pattern should be. Without `bars` a grid must be whole bars. `swing` (on the
song or a pattern) delays every second step by that fraction of a step.

### Control notes

Some instruments take notes on a channel of their own as commands: a drum
machine that mutes a voice while a note on channel 10 is held, or randomises
its kit on a note on channel 11. A track that lists those channels under
`control_channels` can send them, and its instrument hears them beside the
track's own notes:

- `channel` on a pattern sends all its notes on that channel;
- `channel` on a free event sends that event's notes on it, so a drum pattern
  can play its lanes on the track's channel and hold a mute on channel 10 at
  the same time:

```yaml
tracks:
  - id: kit
    kind: drums
    control_channels: [10, 11]
    instrument: {plugin: UA Battalion}
patterns:
  beat:
    drums: {C2: "x...x...x...x...", C#2: "....x.......x..."}
    events:
      - {at: 2, dur: 2, note: D2, channel: 10}   # voice 3 muted for beats 3 and 4
  shake: {channel: 11, step: 1/4, notes: "C0 . . ."}
```

A control note is sent as written: no transposition reaches it (the track's,
the pattern's or a calibrated preset's), and it is a note, not a drum lane. The
channel must be one of the playing track's `control_channels`, and a control
channel belongs to its track alone: it is no track's own channel and no other
track's control channel. The patterns of a play list follow one another, so
notes that go with a track's own notes are events in the same pattern. A track
that plays control notes only (a drum machine whose own sequencer plays) is
skipped by the [load check](#loading), which plays each track's first note on
its own channel and never a control note.

## Sound

A track declares how it sounds: its instrument's `preset` and `params`, each
effect's `preset`, `params` and `bypass`, its `level` and `mute`. That is its
sound everywhere.

A **scene** overrides some of it, and a section names the scenes it plays with.
A section's sound is therefore the tracks' own declarations overlaid with its
scenes — nothing carries over from the section before. Two consequences:

- Playback can start at any section and sounds the same as it does in the full
  song.
- Whatever a scene changes must also be declared on the track, so the sections
  that do not change it have a value to return to. `save_song` says which
  declaration is missing.

What can be set:

| | On | Value |
|-|----|-------|
| `level` | a track | dB; 0 is unity, up to +6 |
| `mute` | a track | `true` / `false` |
| `preset` | an instrument or effect | the name of one of the plugin's factory presets |
| `bypass` | an instrument or effect | `true` / `false` |
| `params` | an instrument or effect | by parameter name: a number in the parameter's own range, `"60%"` of it, or one of its value labels (`Saw`, `on`) |

A scene addresses the master's effects as the track `master`.

Loading a preset resets a plugin's parameters, so after a preset change every
parameter the song sets on that plugin is sent again. The load itself is sent
400 ms before its section, because a plugin is deaf while it loads (see
[Status](#status)); where its section begins sooner than that after the
playback starts, it is sent as the playback starts. What that track plays in
the 400 ms before the section is cut, lost or already played with the new
preset.

**Automation** moves a control within a section:

```yaml
sections:
  intro:
    bars: 4
    play: {pad: chords}
    automation:
      - {track: pad, param: Cutoff, from: 0.2, to: 0.8}              # the filter opens over the section
      - {track: pad, effect: reverb, param: Mix, from: 0.2, to: 0.6, bar: 3, bars: 2}
      - {track: pad, param: level, from: -12, to: 0, bar: 4}         # a swell into the next section
```

`param: level` is the track's fader; anything else is a parameter of the track's
instrument, or of `effect`. `bar` (1-based, default 1) and `bars` (default: to
the section's end) place the ramp.

## A drum machine's own sequence

A drum machine plays a pattern of its own while AUM's transport runs. UA
Battalion keeps one per voice: steps drawn by hand, and under them ten lanes
with a value for every step (see
[research/battalion.md](research/battalion.md#the-sequencer)). They are not
parameters; they live in the plugin's saved state. A song writes them with
`sequence:` on the instrument, and the session's file carries them:

```yaml
tracks:
  - id: kit
    instrument:
      plugin: UA Battalion
      sequence:
        1: {steps: "x... x... x... x..."}                     # voice 1 on every beat
        3:
          steps: "xxxx xxxx xxxx xxxx"
          vel: [0.75, 0.4]                                     # loud, soft, loud, soft …
          ratch: ". . . . . . . . . . . . . . 0.5 ."           # four hits in step 15
          prob: {values: "0.5 0.5 0.5 0.5 0.5 0.5 0 0 0 0 0 0", mode: free}   # 12 against 16: three bars before it comes round
        5:
          steps: "x..x ..x. .x.. x..x"
          pitch: {values: [0.5, 0.645833, 0.75], mode: trig}   # every hit the next of three pitches
```

Voices are `1` to `8`. Each says any of:

| Key | Is |
|-----|----|
| `steps` | the steps drawn by hand, a drum grid: `x` a step, `.` none, `\|` a bar line of 16 steps in 4/4 (checked) |
| `length` | how many steps the sequence runs for, 1 to 64 (default: the steps written) |
| `vel` `shift` `pitch` `ratch` `env` `pan` `var` `prob` `sends` `macro2` | the lanes |

A lane is its values, a list or a grid line of numbers from 0 to 1 with `.`
for the lane's neutral value (0.5, and 0 for `ratch`), or
`{values, length, mode}`:

- `length` is how many steps the values repeat after (default: as many as are
  written; more than that runs on at the neutral value).
- `mode` is `sync` (the default): the lane runs with the steps, its values
  repeating from the start of the sequence. `free` runs on by itself from the
  transport's start, across the end of the sequence: a lane of 12 against
  steps of 16 comes round in another place every bar. `trig` moves on only when
  the voice fires, `retrig` too, and goes back to its start with the steps.
  `shift` has only `sync` and `free`.

What a lane does, measured at 120 BPM (a step is 125 ms):

| Lane | 0 | 0.5 | 1 |
|------|---|-----|---|
| `vel` | 4 dB softer, not silent | as played | 6 dB louder (0.25: 2 dB softer, 0.75: 3 dB louder) |
| `shift` | 95 % of a step early | on the step | 95 % of a step late; in between ±(2v − 1)² of that |
| `pitch` | 24 semitones down | as tuned | 24 up: round((v − 0.5) × 48) semitones, so 0.5 + st/48 |
| `ratch` | one hit | 4 hits | 7 hits in the step: 1 + round(6v), evenly spaced |
| `env` | shorter (a hit of 95 ms: 40 ms) | as set | longer (280 ms; 0.75: 185 ms) |
| `pan` | hard left | where the voice is | hard right |
| `prob` | never | every time | a chance of a double hit (the manual) |

`var` (the voice's variation), `sends` (up for the delay, down for the reverb)
and `macro2` (the voice's MACRO 2) are written as the others; their scales are
not measured.

- **The same in every section.** A scene cannot change a saved state. A lane's
  length and mode are parameters as well (`seqChan3probSeqLength`,
  `seqChan3probSeqMode`), so params, scenes and ramps can move those: say them
  in params then, not in the sequence (`save_song` refuses both). Another
  arrangement in another section needs another voice, or the parameters of
  the generator and the Performance page.
- **No preset.** A preset replaces the whole kit with its own sequence: an
  instrument with a sequence has no preset, in no scene.
- **With the transport.** The steps play while AUM's transport runs, from
  its start. A song played from its session's sequencer has them in the bar of
  lead-in before bar 1 too.
- **Length.** Writing a voice's length switches its SNAP off, which would keep
  the sequence as long as the generator's steps.
- **Where it is written.** The sequence is written over the saved state of the
  plugin's default patch, which the rig keeps as the plugin's base state:
  capture it once with `capture_auv3_default_state {session_id, channel, slot,
  base: true}` from a session AUM saved with a Battalion of the default patch.
  `save_song` warns while the rig has neither. Where the rig has a default
  state for the plugin, which every node of it gets, the sequence is written
  over that instead. The node's state is then
  Battalion's XML alone, without the binary copy the plugin keeps beside it:
  given the XML alone, it plays what the XML says.
- **Another sequence, another session.** The sequence is in the session file,
  so a changed one makes `load_song` author the session again.

## Sounds

A preset's name says little about its sound. One sounds at the written pitch,
the next an octave below it, a third is 20 dB quieter or has a second note
layered in. A drum machine puts its pads on notes of its own. `audition_sound`
finds that out by playing a note and measuring the tap:

| | |
|-|-|
| pitch | The note a held note settles on, as `transpose`: the semitones to add to a written note so it sounds as written (12 for a preset an octave low). How far it is out of tune, how far it wanders once it has settled, and whether it glides into the note: from how far off and for how long. |
| level | rms and sample peak of the held note, in dBFS. |
| envelope | Attack, whether a held note sustains or how fast it decays, how long it rings after the release. |
| layers | Strong partials that are no harmonic of the note: a fifth, a second voice. |

With `kit: true` the instrument is treated as a drum machine: every note of a
range is hit, the ones that sound are its pads, each with level, length and
brightness and a guess at what it is: kick and toms by their pitch, a snare by
the gap between its drum and its rattle, hats and cymbals by their length, and
`perc` for what sits in the middle. The guesses make up a drum map
(`kick: 49, snare: 51, …`); they come from the sound alone, so read them and
correct them with `set_sound`.

A preset is loaded through a Program Change, so it can only be auditioned in a
session that maps it:

- **The lab.** `open_sound_lab` takes a list of instruments and loads a
  session of one strip each, nothing else in the path, with `presets: all`. It
  is the song `sound_lab` and is loaded like one. Any preset of those
  instruments is then one `audition_sound` away, a second or two each, several
  per call (`presets`, or `query` for every preset with a word in its name).
  An instrument that did not load is named when the lab opens, and the lab
  stays usable for the others.
- **A song's own session**, when its `session:` block says `presets: all`. The
  candidate is then heard through the track's own effect chain.

Measured in the lab, on a clean path with the fader at unity, the result is
stored in the **sound catalogue**: one YAML file per plugin under `sounds/` in
the config dir. `list_sounds` reads it. A measurement through an effect chain,
with parameters set on the instrument or with the fader moved is reported but
not stored: it is the chain's sound, not the preset's. What `set_sound`
corrects (a transposition, a drum map, a comment) is kept when the preset is
measured again. A measurement of silence is not stored either: it says that
the plugin did not load or that the note is none of its notes, and nothing
about the preset.

A kit's level is its kick's when it has one. The loudest pad is often a snare
far above the rest, and a kit trimmed by that plays too quiet as a whole.

## Calibration

`calibrate: true` at the top of a song compiles it against the sound
catalogue. Three things then need no correcting by hand:

```yaml
calibrate: true

tracks:
  - id: lead
    level: -3                         # loudness, the same for every preset
    instrument: {plugin: poly, preset: Glass Lead}
  - id: drums
    kind: drums
    instrument: {plugin: drumbox, preset: Analog Kit}

patterns:
  hook: {step: 1/8, notes: "C5 - - A4 - - C5 -"}     # the notes that should sound
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}

scenes:
  chorus:
    lead: {preset: Soft Saws}         # another octave, another level: both follow
```

- **Notes are sounding pitch.** A track's notes are shifted by the measured
  `transpose` of the preset it has in each section. A scene that changes the
  preset changes the shift with it, so no `transpose` on tracks or patterns is
  needed to undo a preset's octave. A track's or pattern's own `transpose`
  still applies on top. Only whole octaves are applied: a measurement of 16
  or 38 semitones is a bell's or a texture's strongest partial far more often
  than a tuning, so it is listed and the notes stay as written. `set_sound`
  with that transposition says it is the patch's tuning, and then it applies.
- **Levels are loudness.** A track's `level` is added to what brings its
  preset's reference note to -20 dBFS rms, per section. Level 0 is therefore
  the same loudness for every preset, before the effects. The fader ends at
  +6 dB: a preset too quiet for that plays as loud as the fader goes, and the
  song says how much is missing and to raise the plugin's volume.
- **Drum lanes are the kit's pads.** `kick`, `snare`, `hat` … resolve to the
  notes of the kit's measured drum map instead of General MIDI's. A lane the
  kit has no pad for is an error that lists the pads it has. The track's own
  `drum_map` still wins, and says where such a lane goes.

`save_song` and `get_song` list every preset the tracks use with what was
applied. A preset that is not in the catalogue plays as written and is listed
as not measured; `audition_sound` measures it. Without `calibrate` the
catalogue is not consulted at all.

The catalogue is measured on a clean path, so calibration brings presets to
the same pitch and into the same range of loudness. The balance of a mix with
its effects is still made by ear and by measuring it.

## What is checked

`save_song` refuses a document that does not hold together, and reports every
problem with its place in the document:

- references: tracks, patterns, scenes, sections and effects that do not exist;
- pedalling: a stretch without a start or a length, a pedal in a pattern of
  control notes;
- patterns: notes that are not notes, lines of unequal length, bars of the
  wrong length;
- channels: two tracks on one channel, a control channel that is a track's
  channel or another track's control channel, a pattern that sends on a
  channel the track playing it does not list;
- plugins, against the staged AUv3 probes: an unknown plugin, preset or
  parameter (with the names that come close), a value outside a parameter's
  range;
- a control a scene changes without a declaration on the track;
- a [sequence](#a-drum-machines-own-sequence): a voice, step or value that is
  none, a length shorter than what is written, a mode the lane lacks, a
  plugin without one a song can write, a preset beside it, a lane length or
  mode said both in the sequence and in params;
- in a calibrated song: a drum lane its kit has no pad for, a note that leaves
  the MIDI range once it is shifted for its preset.

`dry_run: true` checks without storing.

## The session

`author_song_session` builds one strip per part:

```
Brain     ProbeMidiBrain, the sequencer (a MIDI strip)
<track>   instrument > effects > fader          one strip per track
Master    master sum > fader > master effects > ProbeAudioTap
```

**MIDI.** One brain plays every track. Its output is routed to every instrument
through a filter that passes only that track's channel and its
`control_channels` (an effect with `midi: true` hears the track's channel
alone), and to AUM's MIDI Control through a filter that passes only the control
channels. A note therefore never fires a mapping, and a control message never
reaches a synth. The
sequencer sits next to the brain and is routed the same way, to the same
destinations through the same filters.

| Channels | Carry |
|----------|-------|
| 1…N | the tracks' notes, one channel each, their control notes and their sustain pedal |
| N+1…14 | plugin bypasses and parameters, filled from 14 downwards; N is the highest channel a track plays or hears on |
| 15 | strip levels and mutes, the transport and the sequencer's bypass (the convention's CCs), preset loads (Program Changes) |
| 16 | left to the rig: tap toggles and session switching |

**Mappings.** Every strip's level and mute and every plugin's bypass are mapped,
along with each parameter and each preset the song names. A preset becomes one
of the plugin's "Preset Load" actions in AUM, fired by a Program Change. With
`full_control: true` every other parameter of every plugin is mapped as well,
around the song's own mappings.

**The sequencer.** When Atom | Piano Roll² is among the staged AUv3 probes,
the session hosts it with the whole song in one clip: every track's notes on
the track's channel and every control change as a MIDI event, after a lead-in
(see [Playing](#playing)). The clip is armed and does not loop, so it plays
once whenever the transport runs from the top, whoever starts it. Its bypass
is mapped (CC 106 on channel 15), which is how a live playback that runs the
transport keeps it silent. `sequencer: false` as an argument of `load_song` or
`author_song_session` leaves it out; `sequencer: true` insists on it.
[The sequencer's state](#the-sequencers-state) says what is written.

The daemon records which session a song was authored into, for which layout,
and which notes its sequencer holds. Changing notes, patterns or the
arrangement leaves the layout alone, but not the sequencer: it keeps playing
the song as it was authored. Adding a plugin, a preset or a parameter changes
the layout, and so do the tempo (it is the session's clock) and an
instrument's [sequence](#a-drum-machines-own-sequence) (it is in a plugin's
saved state): the session must then be authored and loaded again, and until it
is `get_song` and `play_song` say so.

The session is staged under `Songs/<title>` by default (`out_id` chooses
another place), which is also where it lands in AUM's folder on the iPad.

### Taps

The master's ProbeAudioTap streams what the song sounds like to the daemon.
With `track_taps: true` every track has one of its own after its fader, which
carries that track alone.

Every tap of the session is given a name, written into the plugin's saved
state: the session's id and the strip the tap sits on.

```
Songs/Night Drive/master     the master's tap
Songs/Night Drive/bass       the tap of the track "bass"
```

The plugin dials the daemon under that name, so the daemon can tell the
streams apart. `get_audio_tap` and `get_audio_clip` take it as `name`,
`probe_sound` as `tap`, and `author_song_session` lists the names. Two things
follow from them:

- **The daemon knows which session AUM has open**, for as long as that
  session's master tap is connected, without opening AUM's menu. See
  [Loading](#loading).
- **`mix_song` hears every track at once.** See [The mix](#the-mix).

The name is the one the session was authored under. A copy of the session
saved under another name in AUM keeps it.

This needs a ProbeAudioTap that sends its name. An older build of the plugin
ignores the name and dials in without one; the daemon then knows such a tap
only by the address it dials from, takes the most recently connected one for
the song's, and reads the open session from AUM's screen, as it did before
taps had names. A session staged before taps had names is authored again by
the next `load_song`.

**Options.** A song can say how its session is built:

```yaml
session:
  track_taps: true      # a ProbeAudioTap after every track's fader
  full_control: true    # map every other parameter of every plugin as well
  hardware: x32         # the master's output profile; builtin is the default
  presets: all          # a Preset Load for every factory preset, not only the song's
```

With `presets: all` every factory preset of every plugin gets a Program Change:
the instruments' first, then the effects', filling channel 15 and then the
control channels below it (128 to a channel, so a five-track song has room for
1280). A preset's address then depends only on the session's layout. The song
can change to any preset, and any can be auditioned in the loaded session,
without a new one. Presets beyond the room are left out and counted; one the
song names must fit.

They are part of the document, so changing one makes the session out of date
like a new plugin does. The same options as arguments of `load_song` or
`author_song_session` override the document for that call.

## Loading

`load_song` takes a saved song to a session that plays. It reports every step
and stops at the first one that fails, with what it saw and what to do:

| Step | What happens |
|------|--------------|
| author | The session is built and staged. Skipped when the staged file is already this song's session: its layout, its options and, when it hosts the sequencer, the notes as they are now. |
| pull | The iPad's auv3-probe app fetches the file into AUM's folder. The app does that while it is in front; when the file is not fetched within a few seconds the daemon brings the app forward, starts its sync, and reads the app's list of sessions while it waits, up to a minute. A session the app holds as a conflict ends the wait at once: see below. Skipped when the iPad already has this very file. When the step fails, the daemon brings AUM back to the front: the app is not left there. |
| load | AUM is told to open the session (over WebDriverAgent: plugin windows and panels are closed first), and the call waits until AUM's main menu shows the session's name and AUM has finished restoring the plugins, up to 2 minutes in all. A heavy session can keep AUM's UI from answering for a minute, so the name is waited for as long as AUM shows that it is loading, and the menu is opened again when the load closed it; once the load is over the name gets 10 s more. The step fails, saying which session AUM shows, when AUM ends up with another session or none. |
| connect | The new session's ProbeMidiBrain and ProbeAudioTap dial the daemon. The master's tap dials in under the session's id, which tells the daemon that this very session is the one that came up. A tap that gives no name is taken after a few seconds, and the step says so. |
| check | `check_song`, below. |

Loading replaces the session open in AUM, so `load_song` needs
`confirm: true`. Without the iPad UI tools (`ipad_wda_url` empty) it stops
after the pull and names the session to load by hand.

Bringing the probe app forward needs its bundle id, which carries a signing
prefix. A daemon on the host the iPad is plugged into looks it up. Any other
daemon remembers it from the first `ipad_launch` with the full id; from then
on the iPad tools take `app: probe` there too.

**A session the app does not pull.** The app tells the daemon nothing about a
file it leaves alone: it simply never asks for it. The reason is on its
screen, so the pull step reads that: the "aum sessions" tab, in the folder the
session goes to, every few seconds.

The usual reason is a conflict. The app keeps AUM's folder and the daemon's
staged files alike, and a file that changed on both sides since it last synced
it is copied neither way. Its row reads "conflict — both sides changed" and
has two buttons, "keep device" and "keep controller". The pull step ends as
soon as it sees that on the session's row:

- When the copy in AUM's folder is one the daemon staged, the daemon taps
  "keep controller" on that row, the app fetches the staged session, and the
  step says that it did. Nothing but an older file of the daemon's own is
  replaced. The daemon goes by the copy's size, which is all the row gives
  of it: it keeps the sizes of the last files the iPad fetched under the
  session's name, and the row's size must be one of them.
- Any other copy is left alone and the step fails, naming the session, what
  the row says of the copy, and what to do. That is a session saved in AUM
  (AUM writes every plugin's state out again, so the file is larger: one
  authored at 105 KB came back at 156 KB), a file of the same name the iPad
  never fetched from the daemon, and any session outside `Songs/`. Picking
  "keep controller" there by hand loses what AUM's folder holds, so save
  that under another name in AUM first if it is to be kept.

The daemon walks the app's browser to the session's folder from its root
before it taps, and taps the button that follows that one row. Other files
the app holds as conflicts are never touched.

When the wait runs out without a conflict, the step reports what the app
showed instead: that it does not reach the daemon ("controller offline"), an
error on the session's row, a browser that could not be read (no AUM folder
linked), or the tab's status line. It also says when the app had only just
been started. iPadOS drops the app while AUM is in front, and every start
sends all its audio units to the daemon again (184 on the rig, a few
minutes), which it does only while it is in front. Sessions are pulled while
that runs: see Status.

**The probe app is not left in front.** AUM keeps the iPad awake; the probe
app does not. An iPad left with the app in front locks itself after its
auto-lock time, and with a passcode set nothing reaches AUM or the app until
the code is entered on the device. After a pull the load step brings AUM
forward. A pull that fails ends the load, so the pull step then brings AUM
back itself, and says so.

That stops the app's sync, because the app syncs sessions and its audio units
only while it is in front. Whatever a failed pull says to look at or do in the
app therefore starts with opening the app again: `next` names the call for
that (`ipad_launch` with the app's bundle id; `ipad_ui_tree` and `ipad_tap`
take the same `app`) and the one that brings AUM back afterwards (`ipad_launch`
without `app`). When AUM cannot be brought back, the step says that the app
is still in front, and getting AUM there comes first in `next`.

The iPad tools that take an `app` bring it forward and leave it there. For as
long as the last app the daemon brought forward is another one than AUM, each
of them ends its result with a note: which app is in front and for how long,
that the iPad locks itself with it there, and how AUM comes back. `ipad_status`
and `ipad_screenshot` carry the same note. A reader who opens the probe app to
watch its sync brings AUM back when done.

**A locked iPad.** WebDriverAgent tells whether the iPad is on its lock
screen, and the daemon asks: `ipad_status` reports it, `load_song` ends at
once with "the iPad is locked" (at the pull step, or at the load step when
the iPad already has the file) instead of waiting a minute for a pull that
cannot come, and `check_song` and every iPad tool that fails on a locked
iPad say that first, with WebDriverAgent's own words after it ("Unable to
launch … because the device was not, or could not be, unlocked"). The daemon
never unlocks the iPad and never types a code. It is unlocked on the device.

`check_song` answers whether the song will sound if it is played now:

- AUM has the song's session open. The session's master tap says so: it is
  connected under the session's id. When another session's master tap is
  connected instead, the check ends there and plays nothing;
- the brain is connected and the master tap is streaming. For a session with
  `track_taps` the check also says which tracks' taps are connected;
- the mixer has a strip for the brain, every track and the master, and no
  failed plugin. AUM says that a plugin did not load only by drawing its node
  with a ring of red stripes, so the check takes a screenshot and looks at
  the ring of every node on screen;
- every track answers a note. The daemon sets each track's own sound (its
  declaration, no scene) and mutes every strip. Then, one track at a time, it
  unmutes the strip, waits up to 3 s for the master tap to fall silent, plays
  the lowest of the notes the track starts the song with on its own channel
  (on a drum track the kick rather than a hat; a [control
  note](#control-notes) is never played, and a track with no note on its own
  channel is skipped) and measures the master tap. The note counts when
  it peaks above -60 dBFS and 6 dB over what was sounding just before it. It
  is held as long as the sound catalogue says the track's preset takes from
  the note to its level (its latency and attack), at least 250 ms and at most
  1.5 s: a slow string pad held 250 ms is heard far under its level (see
  Status). A track that stays silent is played a second time, held 1.5 s, and
  named with its plugin when it is silent again; when something was sounding
  before the note, the report gives that floor too. What another track left
  ringing in a delay or reverb is behind its mute and counts for nothing.
  What rings in the track's own strip, a reverb's tail or the first try's
  note, is let die before the note. The song's own mutes are put back at the
  end. A strip's mute and its unmute are sent at least 150 ms apart: AUM can
  keep the first of two that arrive together (see Status).

How much of AUM's screen the check touches depends on the tap. When the
master tap names the open session, only the mixer is read: plugin windows
and panels are closed, the strips and the nodes' rings are read, and AUM's
menu stays shut. When no tap names the session, the menu is opened and
closed as well, for the session's name. `ui: true` opens the menu in any
case, `ui: false` leaves AUM's screen alone.

The check does not run while a song plays.

**Which session is open.** The daemon takes it from the connected master tap
that names a session, the most recently connected one when there are several.
Without such a tap it knows only what it loaded or saw on AUM's screen
itself, and only until a new brain connects, which is what a session load in
AUM looks like from outside. `play_song`, `mix_song` and `audition_sound`
compare that session with the song's: `play_song` warns, the other two
refuse. None of them says anything when the daemon does not know.

## Playing

`play_song` returns at once; the song plays in the background. There are two
ways to play, and a session with a sequencer offers both:

| | Sequenced | Live |
|-|-----------|------|
| Who plays | the sequencer in the session, with AUM's transport | the daemon, through the brain |
| What the daemon sends | rewind and start; stop and rewind at the end | the opening sound, then every note and control change when it is due |
| Timing | AUM's own: on the sample | the network's: a few milliseconds either way |
| What is played | the whole song, as it was when the session was authored | the song as it is stored now: all of it, from a section, a number of sections, only some tracks |
| After a change to the notes | `save_song`, then `load_song`: the session is authored again, the iPad pulls it, AUM loads it (every plugin is instantiated again) | `save_song` |
| After a change to what the song hosts or controls, or its tempo | the same | the same: author and load again |

Without `mode`, `play_song` plays sequenced when the whole song is asked for
and the session's sequencer holds the song as it is stored now, and live
otherwise: when `from`, `sections` or `tracks` name a part, when the session
has no sequencer, or when the notes changed after the session was authored. In
that last case it says so, and the song still plays, live. `mode: live` and
`mode: sequenced` insist; `sequenced` is refused with the reason when the
sequencer cannot play what is asked for.

So writing a song goes live (change a pattern, hear it seconds later, a
section or a track at a time), and the song as a whole is played from the
sequencer once the session has been authored from the notes as they stand.

**Sequenced.** The clip begins with a lead-in of whole bars, as many as it
takes to cover 1.2 s (one bar at most tempos): the opening section's preset
loads go out as the transport starts, every other control 600 ms later, and
the song's first note on the bar line after. The song's bar 1 is therefore
the transport's bar 2, bar lines stay on bar lines, and what follows is the
timeline as it is played live, preset loads 400 ms early included. Because
the opening sound is in the clip, the session plays the song by itself:
`play_song` un-bypasses the sequencer, rewinds AUM's transport and starts it a
fifth of a second later (AUM does not rewind when the start follows at once),
and anything else that runs the transport from the top plays the song too.
Half a second after the last bar the daemon stops the transport and rewinds
it; `stop_song` does that at once. Either way it then lifts the sustain pedal
of every track that has one: a sequencer stopped under a held pedal leaves it
down.

`get_song_playback` reports a sequenced song's position from the daemon's own
clock, started when it sent the start; it does not read AUM's transport. In
the lead-in it reports the time to the first bar. AUM begins some tens of
milliseconds after the message (see [Status](#status)), so the report runs
ahead by that much: about a tenth of a second where it was measured.

**Live.** `play_song` first sets the session to the opening section's whole
sound — presets, then a pause for them to load, then everything else — and
then sends the timeline.

- `from` starts at a section (its id, or a 1-based position in the
  arrangement), `sections` limits how many are played.
- `tracks` plays only some tracks' notes and pedalling, to listen to a part
  alone.
- `transport: true` starts AUM's transport with the first note and stops it at
  the end, for plugins that follow the host clock. In a session with a
  sequencer, the sequencer is bypassed for the length of the playback. The
  transport is rewound before the start and again after the stop.

Live, the daemon keeps the time, not AUM. The session's tempo is the song's, so
tempo-synced effects fit, but the notes arrive over the LAN and are emitted on
the brain's next render cycle: accurate to a few milliseconds on a quiet
network, not sample-accurate. That is right for writing a song and listening to
a part of it.

Because the daemon keeps the time of a live song, its host has to stay awake
while one plays. A laptop left alone dozes: the system keeps trying to sleep,
and each attempt stalls the daemon for a second or more, the notes with it. A
sequenced song's notes do not come from the host, so a stall can only delay
the stop at the end and freeze the reported position (expected, not measured:
every sequenced take ran on a host held awake). On macOS the daemon holds its
host awake itself, in both modes: `play_song` starts `caffeinate -i -s` before
its first message goes out and ends it when the song has finished, was
stopped or failed, so the host is held for the length of the playback and no
longer. The display may still sleep, and `-s` only counts on mains power.

That covers `play_song` and nothing else the daemon times: a `play_notes`
sequence, for one, is still only as punctual as its host, so hold the host
awake by hand for those (`caffeinate -i -s`). Nor can the daemon call back a
sleep the host had already begun when `play_song` arrived. That one runs its
course, a gap of a few seconds right at the start of the song, and none
follows it.

## The mix

`get_audio_tap` reports the tap's last ten seconds as one figure, and its
`peak` is the plugin's ten-times-a-second feature, which reads under the
sample peak. A balance needs more than that. `mix_song` plays the song and
reads the master tap out for its whole length:

- **per bar and per section**: rms and sample peak in dBFS, with the bars that
  clipped marked;
- **the whole**: rms, the highest peak (the headroom), the loudest and the
  quietest bar, and what rang on after the last bar;
- **stalls**: how often the tap's stream did not deliver, since the levels
  around a stall read low.

With `per_track: true` it adds the table a balance is made from: the level of
every track in every distinct section where the track has notes or makes a
sound of its own. A track more than 12 dB over another in the same section is
pointed out, and a track that is silent where it plays notes is named. Where
the tracks are measured depends on the session:

| The session has | The tracks are measured | Passes |
|-----------------|-------------------------|--------|
| a tap on every track (`track_taps: true`), each connected under its name | each at its own tap while the song plays: after the track's fader, before the master's effects, over the whole section | the one through the song |
| no track taps, or one that is not connected | each alone at the master tap, the other strips muted, after the master's effects, over the section's first bars (`solo_bars`) | one more per track and section |

`solo: true` asks for the solo passes although the track taps are there, for
the level a track has after the master's effects. The answer of `mix_song`
says which of the two it does, and names a track whose tap is missing. The
solo passes use `play_song`'s own player, so each starts from the section's
whole sound; as the track's first note is due, every other strip is muted,
and when the measurement ends, however it ends, the strips are put back as
the last section played has them.

**The transport.** Every pass is played live, and every pass runs AUM's
transport, as `play_song` does with `transport: true`: the session's sequencer
is bypassed, the transport rewound, started with the first note, and stopped
and rewound after the last bar, and the sequencer put back in line. That is
what lets an instrument that plays by itself while the transport runs be
heard: a drum machine playing its own sequencer, whose rhythm the song writes
as parameters and no notes (see the drum machine's notes in
[research](research/battalion.md)). The passes stay live rather than
sequenced because the daemon's clock then starts with the first note, which
is where the bars' levels are counted from, and because a part of the song
and a track alone can only be played live. A stopped measurement
(`stop_song`) or a failed pass leaves the transport stopped at the top and the
sequencer in line, as a pass that ran to its end does.

Nothing in the song says which instrument sounds by itself, so the tracks are
told by listening: a track with no notes in a section is measured there too,
where its strip is open (alone in a solo pass, or at its tap), and has a row
when it sounded and none when it stayed silent. This costs a solo pass per
such track and section.

`transport: false` keeps the transport stopped through every pass, as before
the transport was run: the DSP load stays lower (AUM's rose from 40 % to 85 %
in one reading with the transport running, see [Status](#status)), only the
tracks that have notes get solo passes, and an instrument that needs the
transport is not heard.

The measurement runs in the background: `mix_song` returns with an estimate,
`get_song_mix` gives the progress or the finished report (`wait_ms` waits for
it), and `stop_song` ends it. `from` and `sections` limit it to a part of the
song, `whole: false` to the tracks.

The bars' and sections' levels are what the master tap carries: after the
master's effects, a limiter included. A peak is the highest sample, not an
inter-sample estimate.

## The sequencer's state

A sequencer can only play a song the daemon wrote if its content can be
written from outside. That decides the choice. What a session's sequencer has
to offer:

- its saved state, which the session file carries, is readable and writable
  (or it loads a file or a script the daemon can write);
- it holds a whole arrangement, not a loop: every track, for the length of
  the song;
- it follows the host's transport and starts with it from bar 1;
- it also sends Program Changes and Control Changes, on any channel, so the
  preset loads and every mapped control ride in the same sequence;
- one instance is enough.

Atom | Piano Roll² (Audio Veek) does all of it, shown on a device (see
[Status](#status)). The other installed MIDI processors that hold notes were
looked at through the state they had saved in existing sessions, and by what
they are. Most keep a loop, a step pattern or one short sequence by design.
One saves an opaque document, one a single parameter (its content is not in
the session). Several save a keyed archive, which was not decoded: they are
step sequencers, a sequence read by several playheads, and a clip launcher,
not a timeline. One scriptable processor saves its script as text and its data
as arrays of 1024 values, so a song could in principle be written as a script,
played by its interpreter. Two more, a multi-track recorder and a pattern
sequencer, had no saved content in any session and were not examined once one
candidate met every point.

Atom saves a dictionary of some fifty keys, each a JSON value. The session
file carries it as the node's saved state. What matters:

| Key | |
|-----|-|
| `version` | A number, 116. Without it Atom refuses the state ("saved in the beta version"). It is the one entry that is not JSON data, and it is not the version the session library stamps on a saved state, so the song's session sets it. |
| `patterns` | What the engine plays: a list of clips, each `{id, interval: {begin, end}, notes, events}`. A note is `{time, duration, channel, pitch, velocity}`, an event a raw MIDI message `{time, byte1, byte2, byte3}`. Times are in sixteenth notes from the clip's start, fractions allowed; channels count from 0. |
| `store` | What the window draws: the editor's own state, a JavaScript object literal (with `new Map(…)` and `new Set(…)`) in a JSON string. It holds the clip again, each note in several forms. The window never looks at `patterns`, and the engine never at `store`. |
| `isLaunched`, `isLooped`, … | The clip's switches: armed, not looped, nothing recorded, no quantizing, no snap-back of controllers, every output channel on. |

With keys left out Atom reports "Project was corrupted" and plays nothing:
seen with the editor's keys missing, and again with only `store` missing.
Which of the others it could do without was not tried; all are written. For
the engine `store` only has to be there: with an empty one the clip played and
the window stayed on a spinner, and with another clip's store the window
showed that clip's notes while the engine played the song. So both copies are
written from the same sequence, and the window shows what plays.

The song is one clip in one instance. A song's controls are step changes and
ramps the daemon has already rendered as single messages, so they need nothing
from the sequencer but to be sent on time.

## Status

The test suite covers the notation, the compiled timeline, the player in both
modes, and the authored session read back from its encoded file: strips and
chains, the matrix routes with their port name and filters, every mapping, the
preset-load actions, and the sequencer's node with its state: the clip decoded
again note by note and event by event, the editor's copy, the version, the
routes and the bypass mapping.

What a session file says and what AUM does with it are two things. Measured on
a device, in sessions authored from a song:

- **The sustain pedal.** Control Change 64 on a track's channel, sent through
  the brain, reached the track's instrument, a sampled piano: two notes held
  250 ms rang at -36 dBFS 1.3 s after their release with the pedal down and
  had fallen to -62 dBFS without it, and lifting the pedal damped them
  (-58 dBFS 0.7 s later). Twice each way.
- **Routing.** One brain plays every track. A note on a track's channel
  sounded only that track's instrument (its pitch read back at the tap), and a
  message on a control channel made no sound.
- **Preset loads written from scratch.** A Program Change on channel 15 loaded
  the mapped preset on each of four plugins; timbre and level changed at the
  tap every time.
- **Transport.** The mapped CCs on channel 15 started and stopped AUM's
  transport. Stopping does not rewind, so `play_song` rewinds before it
  starts. In one reading AUM's DSP load rose from 40 % to 85 % while the
  transport ran in a five-track session, which is why `transport` is off for
  live playing unless asked for; a later one, with the sequencer playing, is
  below.
- **Strip layout.** AUM stores a strip as its source, its output, then its
  inserts, and counts the inserts ahead of the fader. Sessions are authored
  that way; with the effects stored before the output, AUM took the first
  effect for the strip's output.
- **Fader law.** A level stored in the session is linear gain: tracks authored
  at -3 to -10 dB showed exactly those values. A mapped fader CC follows
  gain = 2·(value/127)², which puts unity at 90 and +6 dB at 127. Every `level` the
  song sends is converted by that law.
- **Bypass and mute.** Both latch by value: 64 and above bypasses a plugin or
  mutes a strip, 63 and below brings it back, and the same value twice changes
  nothing. Read from AUM's own display and at the tap. The song sends 127 and
  0. A mute and its unmute sent to one strip back to back, about 10 ms apart,
  left the strip muted in 2 of 10 tries; 30 ms and more apart they left it
  open in all 24.
- **Parameters.** A parameter's CC runs linearly over the parameter's range:
  0, 51, 64 and 127 put a reverb's mix at 0 %, 40 %, 50 % and 100 %.
- **A preset load resets parameters.** Loading a reverb preset put its mix
  back to the preset's own value, and the song's value sent after it held.
- **Parameters in groups.** Many plugins sort their parameters into groups,
  and AUM keeps the mapping of such a parameter in a collection per group,
  under the parameter's key path. A session that kept them next to the
  ungrouped ones loaded, but AUM listed none of them as mapped and no CC moved
  them. Authored where AUM keeps them, 857 parameters of a drum machine with
  groups three deep answered their CCs: a voice's gain at 0 was silence, and
  each of 25 values of an indexed parameter selected its entry
  ([research/aum-session.md](research/aum-session.md)).
- **A ramp on a plugin parameter.** A filter offset ramped over a four-bar
  intro raised the level bar by bar (-66, -56, -51, -47 dBFS rms), and a
  sequencer's fill and density ramped over a section added and dropped its
  hits.
- **A plugin with a sequencer of its own.** A drum machine whose presets
  carry patterns played them as soon as AUM's transport ran, and so under a
  song played from the session's sequencer. A song silences such a pattern
  with the plugin's own parameters, or uses it and writes no drum notes. In
  the lead-in the plugin already plays the opening section's values, so a
  song whose drums come from the plugin opens with them silent and brings
  them in from bar 1 ([research/battalion.md](research/battalion.md)).
- **A plugin is deaf while it loads a preset.** Two of five instruments lost
  every note sent up to 250 ms after the Program Change and played the ones
  from 275 ms on; at 300 ms two notes in sixteen were still lost, at 350 and
  400 ms none in thirty-two. A third lost a note at 50 ms and played at
  150 ms. The other two, and a reverb, answered at once. A note that is
  sounding when the load ends is cut. With preset changes sent 250 ms ahead,
  a song lost the downbeat of every chorus on the tracks of the slow two,
  which both change preset there, so they are sent 400 ms ahead now.
- **A whole song.** A song of five tracks and 56 bars (five instruments,
  twelve effects, 14 preset loads, 53 mappings) played from start to finish
  through `play_song`, and `get_song_playback` followed it through its nine
  sections. Recorded at the master tap, every section sounded: the fade-in,
  the verse without the lead, the chorus with the lead, the bridge without the
  drum pattern. What was missing was the downbeat of each chorus on two
  tracks, the pad's first chord and the arp's first note, lost to the preset
  load above; the same timeline sent with the 400 ms lead had both.
- **Timing.** With the host held awake, drum hits recorded at the tap sat on
  the sixteenth-note grid within 9 ms, 2 ms on average over 63 hits. On a host
  left to doze, timers fired up to 1.2 s late and the hits around such a stall
  were about 50 ms off the grid.

Measured on a device with the tools around writing a song (the daemon reached
the iPad over the LAN and its UI over WebDriverAgent):

- **Loading.** `load_song` took a saved song of five tracks and fifteen
  plugins to a session that played in 62 s: authored in 0.1 s, pulled by the
  iPad within 9 s (the daemon had brought the probe app to the front), 46 s
  until AUM had it open with its plugins restored, brain and tap connected at
  once. `open_sound_lab` took 57 and 55 s for two labs.
- **Every preset mapped.** AUM loaded sessions with 601 and with 748 Preset
  Loads, spread over the Program Changes of channel 15 and the control
  channels below it. Presets on channel 15 and on channels below it loaded
  when their Program Change was sent.
- **Auditioning.** 31 presets of four synths were played and measured in four
  calls, about 2.5 s a preset, and two kits of twenty notes each in 30 s. Of
  ten bass presets of one synth, six sounded two octaves below the written
  note, three had no steady pitch and one measured 25 semitones off. Of ten
  pads of another, five sounded at pitch, one an octave and two two octaves
  below, and two were textures with no note of their own. Three of six bell
  presets of an FM synth measured 7, 16 and 36 semitones off: their strongest
  partial, not their note.
- **A plugin that did not load.** One instrument came up as a failed node in
  a lab. Nothing in AUM's accessibility tree said so; the node's ring was
  drawn in red stripes where every other node's is grey. Every audition of it
  measured digital silence.
- **A calibrated song.** A second song, twenty bars and five tracks, was
  written in sounding pitch with no transposition anywhere and played from
  start to finish. Its bass, on a preset that sounds two octaves low,
  measured D2 at the tap (73.6 Hz, 4 cents sharp) for a written D2. With the
  levels from the catalogue four tracks came out within 7 dB of each other;
  the drum kit, trimmed by its loudest pad, the snare, was 13 dB under.
- **The check, and what it got wrong.** `check_song` at the end of that load
  called the drums silent. They were not: it had played their hat while the
  bell's delay was still ringing, and the hat was not 6 dB over that. The
  check now mutes the other strips and plays the lowest first note. With
  that, the next load's check played the kick behind the other strips' mutes
  and every track answered. The two checks after it called the first track
  silent, at digital silence, while a note sent by hand sounded, and the
  second of them left the last track's strip muted: the check had muted the
  first strip and opened it in the same moment, and muted the last one and
  put its own mute back in the same moment. That is the pair of mutes
  measured above. With the two kept 150 ms apart, six checks in a row on two
  songs heard every track at its first note, and the mixer read after each
  showed no strip muted. A check takes about 18 s.
- **A slow pad called silent.** At the end of a load the check called a
  string pad silent twice: held 250 ms, its note peaked at -33.4 dBFS. The
  preset rises to its level in 490 ms on its own (the catalogue's
  measurement) and in 1160 ms behind its phaser and cathedral reverb, where
  a 2 s note peaked at -16.3. A sequenced `play_song` played it, and at the
  load before, with the pad 7 dB louder, the check had heard it at -23.9.
  Watched at the pad's own tap through a check, its two notes ran into each
  other: the level stayed between -36 and -33 dBFS from the first note to
  the end of the second, so the first note's tail was the floor the second
  had to stand 6 dB over, and the reverb was still at -55 dBFS 3 s later.
  The check now holds a note for the attack the catalogue measured, holds
  the second try 1.5 s, and lets the tap fall silent before each note.
- **Another session in AUM.** With another song's session loaded,
  `check_song` named the session AUM had open, said it was not the song's
  and played nothing.
- **A failed plugin, seen by the tools.** A lab with the instrument that does
  not load was reported as loaded "but not every instrument came up", with
  that instrument marked as failed, read off the screenshot. Its auditions
  were reported silent and not stored.
- **The mix.** `mix_song` measured the song's twenty bars and thirteen solo
  passes in 233 s: the fade-in and the fade-out bar by bar, each section, and
  every track alone in every section. After one change of levels, saved
  without a new session, the two full sections measured -20.0 dBFS rms with
  peaks at -5.1, and no track more than 7 dB from another.
- **A restart of the daemon** in the middle of a measurement (its own update
  after a merge) ended the measurement. The session stayed loaded, brain and
  tap dialled the new daemon within seconds, and the next call worked.

- **Measuring a preset.** The analysis behind `audition_sound` was run over
  notes captured on a device with `probe_sound`, in a song's session. For
  seven presets of four synths it gave the transposition found by hand
  before: two octaves low twice, one octave low twice, at pitch three times.
  It also found what the hand measurement had not looked for: one bass preset
  starts every note 140 cents flat and is on it after 150 ms, one brass
  preset has an octave under the note at -8 dB, and one sequence preset
  jumps between octaves by itself. Twenty notes of a drum machine gave its
  eight pads: the kick, the snare, three toms pitched within 1 Hz of the hand
  measurement, two bright pads of 50 and 180 ms, and one in the middle that
  the first song had used as its closed hat. A note took 75 to 95 ms from
  being sent to sounding at the tap.

Measured on a device with the sequencer, the same song of five tracks and 56
bars authored into a session of its own:

- **The sequencer's state written from scratch.** Atom loaded the authored
  state and its window showed the song: one clip of 57 bars with 2311 notes
  and 400 control events, armed, loop off. What it took is in
  [The sequencer's state](#the-sequencers-state).
- **One start.** After the rewind and the start the song played from the
  first bar to the last with nothing else sent, in two takes. Bar by bar,
  every section sounded as it does played live with the transport running in
  the same session: over the first take the two differ by 0.6 dB on average,
  and per section by 0.8 dB or less, with one exception. In the bridge of the
  first sequenced take the level was 3 dB down for the four bars: the bass's
  fundamental 4 dB weaker and its second partial more than 10 dB stronger
  than in the live takes. AUM's strip showed the level and the bypass the song
  asks for there, and the two later sequenced takes had the bridge like the
  two live ones. Notes sent live to the same synth gave the live sound
  whatever their order, velocity or doubling, but one single note also came
  out different once. So it is not what the sequencer does to that part every
  time, and the synth can do it without the sequencer; what causes it, and
  whether the sequencer makes it likelier, five takes do not say.
- **Control changes from the sequencer.** Program Changes and Control Changes
  sent as the clip's MIDI events reached AUM's MIDI Control like the brain's.
  Read back: the chord of the chorus's preset was there from the downbeat of
  all three choruses, as it is live with the 400 ms lead; after a stop in the
  bridge AUM's strips showed that section's levels and bypasses; the bars of
  the fade-in and the fade-out matched the live take's; in a two-track
  session a test note after a stop had the preset of the section it was
  stopped in. Plugin parameters were not read back one by one.
- **Timing.** The drums alone (the other strips muted), 100 hits recorded at
  the tap: 0.07 ms from the sixteenth-note grid on average, 0.33 ms at worst.
  The same part played live in the same session a minute later, 99 hits:
  1.8 ms on average, 11 ms at worst. The whole mix, 274 onsets above 4 kHz
  over 112 s: 0.3 ms on average, 4.4 ms at worst.
- **The start.** AUM's transport began 22 to 28 ms after the Start Play
  message in a two-track session and 89 to 94 ms after it in the five-track
  one, and once, at the first start after a load, 177 ms. That moves the song
  as a whole; nothing in it shifts.
- **Stop.** Stopped in the middle of a section with a chord, a lead note and
  a bass note sounding, the level fell 16 dB within half a second and 30 dB
  within one and a half: the notes were released, what remained was reverb.
  A clip that had played to its end stayed silent while the transport ran on.
- **The sequencer's bypass.** With CC 106 at 127 the transport ran in silence.
  Taken back to 0 while the transport ran, the sequencer came in at the
  transport's position.
- **DSP.** AUM showed 36 to 39 % with the session idle and 48 % in the last
  chorus, the transport running and the sequencer playing all five tracks.

Those takes ran against a daemon without this mode: the session was authored
with the same code and staged through the daemon's receiver, and the rewind,
start and stop were sent with `send_midi`. Then the same song went through the
tools of a daemon that has the mode:

- **`load_song`.** It took the saved song to a loaded session with the
  sequencer in it in 74 s: authored in 0.3 s, pulled by the iPad in 9.5 s,
  loaded by AUM in 58.5 s, brain and tap connected, every track answering its
  first note.
- **`play_song`, sequenced.** One call played the song from the first bar to
  the last. `get_song_playback` counted down the lead-in, named the section
  and bar on the way, and reported the song finished; AUM's transport then
  stood stopped at the top. Recorded at the tap, the bars are within 0.5 dB of
  the live take on average and each section within 1.2 dB, the bridge among
  them; the chorus's chord is there from all three downbeats; 288 onsets above
  4 kHz sit 0.4 ms from the sixteenth-note grid on average, 3.9 ms at worst.
  The song's first bar sounded 0.15 s after the middle of the call, the
  lead-in deducted: the daemon's position ran about a tenth of a second ahead
  of the sound.
- **`stop_song`.** Sent in the eighth bar, it stopped the song at once: the
  level was 10 dB down after 0.3 s and 25 dB after 2 s, and the transport
  stood at the top.
- **A part, live, with the transport.** One verse played with
  `transport: true` in the same session read -18.1 to -18.7 dB bar by bar, as
  the verse does in the whole takes: the sequencer, bypassed by the daemon,
  added nothing. Afterwards it was back in line.
- **A rewind needs time.** That live playback had left the transport in its
  eighth bar, and the next sequenced `play_song` went on from there instead
  of the top. With the two messages sent by hand: a Start Play 1.4 ms after
  the Rewind did not rewind, one 32 ms and one 99 ms after it did. Stop
  followed at once by Rewind does rewind. `play_song` therefore sends the
  rewind first and the start 200 ms later, in both modes, and a live playback
  with the transport rewinds again when it has stopped. With that in the
  daemon: the transport stopped in the middle of the song and left there, a
  sequenced `play_song` began at the top, the lead-in silent (-88 dB where the
  song had read -20 dB) and the fade-in after it; and after a verse played
  live with the transport, AUM's transport stood at the top.
- **A session the probe app holds as a conflict.** A two-bar song was
  authored, fetched and loaded; its session was then saved in AUM and the
  song authored again. The app listed it as "conflict — both sides changed",
  its copy at 156 KB against the 105 KB the daemon had staged, and `load_song`
  failed at "pull" after 66 s saying only that the app had been brought to
  the front. Read with the code that is in the daemon now, the app's tree
  gave that row, its state, its size and its "keep controller", in a list
  where the button lay 1200 points below the window's edge. The browser was
  walked from its root into the folder, and a click on that button's
  element, found by its frame, made the app fetch the file: the row was in
  sync at the next look, 1.5 s later, and the six conflicts the app held in
  other folders were as before.
- **The pull step against a conflict, whole.** The same song, saved in AUM
  again (164 KB) and authored again, with the daemon as it is now:
  `load_song` failed at "pull" after 52 s with the conflict, the copy's 164
  KB against the 105 KB staged, and what to do. Nothing was tapped in the app,
  and AUM was in front again. Most of the 52 s went by before the app showed
  the conflict at all: it had been started afresh and was sending its audio
  units, so a slower start can run out the minute first, and the step then
  ends with the reasons for a timeout instead. The step's resolving branch
  was run from outside the daemon, with 164 KB counted as a size it had
  staged: it walked the browser, tapped "keep controller" on that row, and
  the iPad fetched the file, 49 s in all. The choice among several conflicts
  in one folder has been run against the stand-in app only.
- **The app's own conflicts.** Earlier runs on the rig had met such a
  conflict three times, each on a song's session with nothing saved in AUM.
  What the daemon saw of the app while the above was measured: after three
  of four pulls the app uploaded the session it had just fetched back to the
  daemon (the one compared had the same bytes), and it uploaded six files of
  other folders in every sync cycle, 20 s apart, for as long as it was in
  front. A copy the app takes for changed is therefore not always one
  somebody changed, which is why the daemon resolves a conflict whose copy is
  its own file and no other.
- **The app is started again and again.** Each of the three times AUM had
  been in front and the app was brought forward again, it came up on its
  audio units tab, sending its units from the start ("syncing 2/184…"):
  iPadOS had dropped it. All 184 had been sent again within 7
  minutes the first time and within 2.5 the second. A staged session was
  fetched within 10 s of such a start and a conflict was on screen within
  20 s, so a pull does not wait for the units.
- **The iPad locks itself with the probe app in front.** The app was brought
  forward on the rig and left there while its tree was read every 20 s for
  the end of its unit sync, with no touch. A quarter of an hour later the
  iPad was on its lock screen and wanted its passcode. WebDriverAgent still
  answered: ready, `GET /wda/locked` true, the Home Screen as the app in
  front. AUM keeps the screen awake, the app does not. That is why a failed
  pull now brings AUM back and the iPad tools say when they leave another
  app in front.

**Other metres.** Songs in 6/8 (92 BPM), 7/8 (148 BPM, grouped 2+2+3) and 3/4
(108 BPM), each a little over a minute, were played from the session's
sequencer from the first bar to the last with their bars in place: the hats of
the 6/8 song sat on its eighth-note grid in its first bar and in its 35th.

Not yet measured:

- the check's note held for the catalogue's attack, its 1.5 s second try and
  its wait for a silent tap (the tests run them against a stand-in with a
  slow pad and a ringing reverb);
- a kit's level taken from its kick, and a preset measured off the octave
  that `calibrate` therefore leaves as written;
- `audition_sound` in a song's own session with `presets: all`, `set_sound`,
  and `load_song` on a daemon that cannot bring the probe app to the front;
- AUM brought back after a failed pull, and `load_song`, `check_song` and
  the iPad tools on a locked iPad (the tests run them against the stand-in;
  on the rig only the locked iPad's answer to `/wda/locked` was read);
- `get_song_playback` against AUM's own position display (it was measured
  against the sound);
- a sequenced song on a host that dozes;
- an edit made in the sequencer's window, and a session saved from AUM with
  the sequencer in it;
- a song much longer than two minutes in the sequencer;
- `transport: true` through a whole live song with the daemon's own bypass of
  the sequencer (one section was; the whole-song take sent the bypass by
  hand);
- `mix_song` running the transport in its passes (with the sequencer
  bypassed, the other strips muted in a solo pass, a track without notes
  measured by listening): the levels of a song whose drums come from a
  plugin's own sequencer were read during a sequenced `play_song`, not yet
  in a `mix_song`;
- sessions authored with `track_taps` or `full_control`;
- taps that dial in under their names: that needs the ProbeAudioTap build
  that sends them on the iPad. Until then every tap comes without a name and
  the tools work as described under [Taps](#taps). The test suite runs both
  cases against a stand-in whose taps take their names from the staged
  session file.

Not every plugin behaves when a session hosts it from scratch. One synth came
up as "invalidated" on every load, and one drum machine loaded and took its
preset but stayed silent.

Presets do not agree on pitch either. On three synths one preset sounded at
the written note and another of the same plugin one or two octaves below it;
one sounded an octave above with a fifth layered under every note. The track's
`transpose` corrects its own preset, and a pattern's `transpose` the preset
that pattern is played with. Other things only listening showed: a tape
effect's preset bent a held note by more than a semitone either way until two
of its parameters were turned down, a drum machine's snare clipped inside the
plugin at any velocity until its own level parameter was. One synth crashed
once in over a hundred preset loads; AUM marks its node as failed, and loading
the session again brings it back.

Play each instrument alone and listen at the tap before writing for it, one
preset at a time.

## Files

| | Where |
|-|-------|
| Songs | `$XDG_CONFIG_HOME/mcp-midi-controller/songs/<id>.yaml` — part of the rig's configuration, like scenes |
| The sound catalogue | `$XDG_CONFIG_HOME/mcp-midi-controller/sounds/<plugin id>.yaml` — measured per rig, and readable and correctable by hand |
| Authored sessions | the AUM staging dir (state dir), like every authored session |
| Which session a song was authored into | `song-sessions.json` in the state dir |
| MIDI exports | `song-exports/` in the state dir |
| Recordings | `recordings/` in the state dir ([recording.md](recording.md)) |

A song names the plugins of one rig. Keep the songs directory out of a public
repository if that is private.
