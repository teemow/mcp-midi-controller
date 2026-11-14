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
| 2. Write | `save_song` | Store the document and get its `revision`. It is checked first, and reviewed as a song; see [What is checked](#what-is-checked) and [A full song](#a-full-song). |
| 3. Load | `load_song` | Author the song's `.aumproj`, have the iPad pull it, load it in AUM, wait for its brain and tap, and check that every track answers. See [Loading](#loading). |
| 4. Play | `play_song`, `stop_song`, `get_song_playback` | The whole song from the session's sequencer, or live: from a section, or only some tracks. |
| 5. Listen | `get_audio_tap`, `get_audio_clip` | The master strip carries a ProbeAudioTap, known to the daemon as `<session id>/master`. See [Taps](#taps). |
| | `mix_song`, `get_song_mix` | The level of every bar, every section and every track. See [The mix](#the-mix). |
| 6. Change | `get_song` → edit → `save_song` (or `patch_song` with `craft_tools: true`) | `get_song` and `save_song` return the song's `revision`; `save_song` given that `revision` refuses to store over a song that has changed since, and `patch_song` applies an id-keyed merge patch against that revision through `save_song` and refuses a stale one ([ADR 0002](adr/0002-patches-by-id-with-a-revision.md)). See [Saving over a changed song](#saving-over-a-changed-song). New notes, patterns or arrangement play at once, live. The sequencer plays them after step 3. A change to what the song hosts or controls, to its tempo or to its session options needs step 3 before the song sounds right at all; `get_song` and `play_song` say when. |

`check_song` tells at any time whether the loaded session will sound.
`author_song_session` does only the first part of step 3: it builds the
session file and stages it.

`export_song_midi` writes the song as a Standard MIDI File for a sequencer or a
DAW.

`play_song` with `record: true` also records the song at the master's tap into
a file, and `upload_recording` puts that file into Google Drive: see
[Recording and Google Drive](recording.md).

A song the iPad cannot compute in one pass is recorded a group of tracks at a
time with AUM's own recorder and mixed from the recordings:
`record_song_stems`, `get_song_stems` and `mix_song_stems`, see
[Stems](#stems). The mix is a song whose tracks play files, see
[A track that plays a file](#a-track-that-plays-a-file).

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
- **Tracks.** Up to twelve in a song that plays from one session; a larger
  piece is recorded in stems
  ([More than twelve tracks](#more-than-twelve-tracks)). Beside drums, bass,
  chords and a lead there is room for a second harmony part, a
  counter-melody, percussion, a texture, an effects track. A track that rests
  is heard when it comes in.
- **Sounds.** Choose from everything installed, not from what the stored
  songs use: `list_song_plugins` lists the instruments and the effects, and
  `audition_sound` measures a preset in seconds. What it reports describes the
  preset. A layered fifth, a slow attack or a pitch that drifts is a character
  to write for, not a fault.
- **Movement.** [Automation](#sound) ramps any parameter over a section: a
  filter that opens into a chorus, a delay that grows into a break. A
  [modulator](#modulators) moves one by itself, in time with the song: a
  filter that sweeps every two bars for as long as a section lets it. A scene
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
  [calibrated](#calibration) song needs none). `instrument: {file: …}` in
  place of a plugin makes the track play a recording: see
  [A track that plays a file](#a-track-that-plays-a-file). `modulators`
  move parameters of the track's plugins by themselves: see
  [Modulators](#modulators). `group` names the
  stem the track is recorded into: see [Stems](#stems). A song may have more
  than twelve tracks when its groups make stems of at most twelve tracks each;
  its channels are then counted in each stem: see
  [More than twelve tracks](#more-than-twelve-tracks).
- **master** — effects on the master strip, after its fader.
- **stems** — optional: each stem's own strip in the mix of its recordings,
  by stem name: its `level` and its `effects`. See
  [Stems](#stems), "A stem's strip in the mix".
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

Switching a plugin back on is the same kind of change. AUM loses the
parameter changes that reach a plugin while it is bypassed and those that come
with its un-bypass, so where a section switches a plugin back on, every
parameter the song sets on that plugin is sent again 200 ms after the
un-bypass (see [Status](#status)); a ramp that starts there carries on from
where it has got to. A playback that opens with a section does the same for
every plugin that is on there and bypassed anywhere else in the song, since it
may still be bypassed in AUM. Until then the plugin plays with the values it
had.

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

### Mapping ranges

A parameter's value reaches its plugin as a 7-bit Control Change, and AUM
spreads the CC's 128 steps over the parameter's whole range. That is enough
for movement, but not for every setting: a frequency exposed from 10 Hz to
30 kHz moves in steps of about 236 Hz, so an 80 Hz high-pass cannot be set.
`range` on an instrument or an effect narrows AUM's mapping of a parameter to
part of its range, its **mapping range**, and the 128 steps are spread over
that part ([ADR 0003](adr/0003-exact-settings-from-mapping-ranges.md)):

```yaml
effects:
  - id: hp
    plugin: …
    params: {"Band 1 Frequency": 80}
    range:  {"Band 1 Frequency": [40, 250]}   # 128 steps about 1.7 Hz apart
```

- A range is two numbers in the parameter's own units, the lowest first, and
  lies within the parameter's range. It names a parameter the plugin's own
  `params` set.
- It is the session's, not a section's: a scene cannot change it. Every value
  the song gives the parameter, in its declaration, a scene, a ramp or a
  [modulator](#modulators)'s depth, lies within it, and a modulator's default
  depth is the whole mapping range. A value means what it means without a
  range: `"60%"` is a share of the parameter's whole range, a label its value
  there.
- A value within the range plays live. A changed range is a changed session:
  it is authored and loaded again, as for a new parameter.
- A song without `range` compiles to the same session and the same MIDI as
  before.

How AUM spreads a CC over a narrowed range, for a parameter its plugin shows
on a log scale, is [measured](#status).

## Modulators

A modulator moves one parameter by itself: an LFO that sweeps a synth's filter
in time with the song, or lets a reverb breathe, for as long as the sections
let it. It is a plugin that sends control changes, and K-Devices LFOH is the
one a song can host. Use as many as there are parameters to move.

```yaml
tracks:
  - id: pad
    instrument:
      plugin: NuSaw
      preset: Adagio
      params: {Cutoff: 0.6}       # where Cutoff rests while the sweep is bypassed
    effects:
      - {id: hall, plugin: Other Desert Cities, params: {Mix: 0.3}}
    modulators:
      - {id: sweep, plugin: LFOH, preset: gruv, target: {param: Cutoff}, depth: [0.3, 0.8]}
      - {id: breath, plugin: LFOH, params: {Smooth: 0.8}, target: {effect: hall, param: Mix}, depth: ["20%", "45%"]}

scenes:
  break:
    pad:
      modulators: {sweep: {bypass: true}}               # the filter stands at 0.6
  drop:
    pad:
      modulators: {sweep: {preset: wonkie, depth: [0.1, 0.9]}}

sections:
  build:
    bars: 8
    play: {pad: chords}
    automation:
      - {track: pad, modulator: breath, param: Smooth, from: 0.8, to: 0.1}
```

- `target` is the parameter it moves: one of the track's instrument, or with
  `effect` one of the track's effects. A parameter has one modulator, and a
  modulator moves a parameter of its own track.
- `depth` is the lowest and the highest value it moves the parameter between,
  written as any value of that parameter is: a number in its range, or
  `"NN%"`. Without it, the whole range.
- `preset`, `params` and `bypass` are the modulator's own, as for any plugin:
  its wave, its rate, whether it follows the tempo. LFOH's parameters are
  0..1 to a song. LFOH turns `Time Numerator` into 1 + 15·x and `Time
  Denominator` into one of seven steps (x = 0, 1/6, 2/6 … 1). In sync, its
  default patch (numerator 1, denominator step 1/6) runs a cycle a quarter
  note; `Time Numerator: 0.07` (2) runs one every half note.
- `id` names it in scenes and ramps (by default its plugin). A track's effects
  and modulators share their ids.

**How it is wired.** The modulator sits on its track's strip after the
effects and passes the audio through, as LFOH sits when it is set up by hand.
The session routes its MIDI output (`MIDI Out`) to AUM's MIDI Control through
the filter the brain's route there has, so only the control channels arrive.
The parameter it moves is mapped like every parameter the song sets, to a
controller on a control channel. LFOH is told to send exactly that controller
on exactly that channel. `save_song` lists them:

```
ch2  pad        NuSaw > Other Desert Cities  (24 notes)
     ~ sweep (LFOH) moves pad Cutoff, sending CC 7 on ch14 between 38 and 102
```

LFOH's routing is four of its parameters: `MIDI Ch`, `MIDI CC`, `MIDI Min`
and `MIDI Max`. The session maps them like any parameter, and every section's
state sets them: the channel and controller of the target's mapping, and the
depth as the two controller values its ends are. LFOH turns the 0..1 a host
gives these parameters into whole numbers: the channel is 1 + 15·x, the
controller and the two ends 127·x. Its own saved state shows that, keeping
every parameter both ways. What the song sends lands on the number it means:
the controller and the ends exactly, the channel less than 0.12 above it, so
LFOH lands on it whether it rounds or cuts off. The routing is the song's to
set: a declaration, scene or ramp that sets one of the four is refused.

**Scenes, ramps and presets.**

- Where a modulator runs, the parameter is its alone. The section's state does
  not send the parameter, and `save_song` refuses a scene of that section that
  sets it, or a ramp that moves it there.
- A scene may bypass a modulator. Its output then goes to channel 1, which
  MIDI Control does not hear. The plugin keeps running there: AUM's own
  bypass of it stays off, because AUM loses the parameter changes that reach
  a plugin together with its un-bypass, and the routing that brings the
  modulator back is such a change (see [Status](#status)). The parameter
  holds the value its plugin declares, or a scene gives it, as any parameter
  does, and a ramp may move it there. A modulator that is bypassed anywhere
  needs that declaration. The modulator is sent away before the parameter is
  set, so the value is the parameter's own.
- A scene may change a modulator's preset, its parameters (its rate) and its
  `depth`. A ramp may move its parameters (`modulator:` in place of
  `effect:`), each declared on the modulator as for any ramp.
- A preset load replaces a plugin's parameters, the routing too. As for every
  plugin whose preset changes, every parameter the song sets on it is sent
  again at the section's start, the routing among them. In the 400 ms between
  the load and the section the modulator sends wherever the preset says.
  LFOH's default patch sends controller 0 on channel 1, and neither reaches a
  mapping.
- A preset load of the target's plugin resets the target to the preset's
  value until the modulator's next message, which comes within milliseconds
  while it moves.

A modulator is part of the session: adding one, or taking one away, needs
the session authored and loaded again. Its target and its depth are in what
the song sends, so moving it to a parameter the session maps already, or
giving it another depth, needs no new session.

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

### Samples

Each of Battalion's voices has a sampler beside its synth engine (see
[research/battalion.md](research/battalion.md#sampler)). Which sample it plays
is not a parameter either: the saved state keeps it, one per voice. A song
names it with `samples:` on the instrument, by its path in the plugin's
factory library:

```yaml
    instrument:
      plugin: UA Battalion
      params:
        drum1synthenabled: "Off"          # the sample alone
        drum1samplerEnvelope: No Env      # all of it, not cut by an envelope
        drum3samplermode: Granular
        drum3samplerparam1: 0.7           # grain size
      samples:
        1: Kicks/Abstrakta 1 Kick.wav
        3: Synths and Instruments/Bass Glasshouse 2 MNRK.wav
```

- **A voice is `1` to `8`; a sample is its path in the factory library**, the
  folders the plugin's sample selector shows (`Kicks/…`, `Hats/…`,
  `Synths and Instruments/…`, `Artists/<artist>/…`). A `file://` URL is
  written as it is (a sample elsewhere on the device; not tried on the rig),
  and `""` takes a voice's sample away.
- **The library needs no setup.** The state keeps a sample as a file URL into
  the app's bundle, whose id changes with every install and update. The plugin
  finds a sample by what follows `Unfiltered Audio/Sample Library/` in it,
  whatever bundle comes before (see
  [research/battalion.md](research/battalion.md#sampler)), so the song writes
  one id that stands for any.
- **Nothing checks the path before it plays.** A path that is not in the
  library leaves the voice without its sample, silent with the synth off. Hear
  each voice with `probe_sound` after the first `load_song`.
- **The synth and the sampler play together** unless the song says
  otherwise: `drum<N>synthenabled: "Off"` leaves the sample alone,
  `drum<N>enginemix` balances the two, `drum<N>samplerEnvelope` picks the
  envelope that shapes it (`No Env` plays it whole), `drum<N>samplermode`
  the engine (Classic, Phase Warp, Cloner, Granular, Phase Slice, Phase
  Stretch, Garbage-Fi), and `drum<N>samplerStart`, `drum<N>samplerEnd` and
  `drum<N>samplerpitch` where it starts, ends and how it is tuned. These are
  parameters: scenes and ramps move them.
- As with the sequence: no preset, the same sample in every section, and
  another sample is another session.

### Locks

The plugin's window can lock a knob (right-click it): its VARIATION, the
Performance page and a preset change then leave that parameter as it is, and
so does the voice's randomise note (ch 11 notes 24–31). A song writes the locks
with `locks:`, by the parameters' ids:

```yaml
    instrument:
      plugin: UA Battalion
      params:
        drum2variation: 0.5               # every hit its own ...
      locks: [drum2pitch, drum2pan, drum2outGain]   # ... but in tune, in place and as loud
```

- A lock is a parameter of the plugin (`get_song_plugin` lists them); the
  song can still set and move it by `params`, scenes and ramps: a lock keeps
  the plugin's own moves off it, not MIDI.
- On the rig, with VARIATION at full: a locked voice's pan and pitch held over
  16 hits (pan the same each hit, against up to 6.5 dB to either side on an
  open voice), its length within a few ms (against 55–135 ms), and the
  randomise note left the locked pitch and pan alone while it moved the open
  voice's pitch and length. A CC still panned the locked voice left and right.
- As with the sequence: no preset, the same locks in every section, and
  other locks are another session.

## A track that plays a file

A track's instrument can be a recording instead of a plugin:

```yaml
tracks:
  - id: strings
    level: -2
    instrument: {file: "Night Drive stem strings/Master - 104 bpm - 001.wav"}
    effects:
      - {id: air, plugin: hall, params: {Mix: 0.2}}

session:
  lead_bars: 1
```

`file` is a path in AUM's Recordings folder: the folder, which AUM names after
the session a take was recorded in, and the file with its extension. The
strip's source is then AUM's own File Player with that file where an
instrument would sit. Its effects, level, mute, scenes and level ramps are the
strip's, as on any track.

- **It plays with the transport.** The player is written enabled, with Loop
  and Sync off, rate 1 and no normalizing: the file plays once, at its own
  speed and level, from its first sample at the top of AUM's transport. It
  cannot be started anywhere else. So a song with a file track plays from its
  session's sequencer, whole and from the top: its session needs the
  sequencer, and `play_song` refuses `mode: live`, `from`, `sections` and
  `tracks` for it.
- **No notes.** A file track is left out of every section's `play` and has no
  preset, parameter, bypass, kind, transposition or control channel; a scene
  sets its level, its mute and its effects. `save_song` names whatever a
  document says beyond that. A song whose tracks all play files needs no
  pattern, and its summary says which file each track plays.
- **The lead-in.** A recording made of a song's session begins with that
  session's lead-in (see [Playing](#playing)): its bar 1 is the transport's
  bar 2. `session: {lead_bars: 2}` gives the song that plays it a lead-in of
  at least that many bars, so its own control changes (a scene, a ramp of a
  level) meet the bars they are written for. A song takes more bars when its
  opening sound needs more.
- **What is written.** A File Player node as the strip's source, whose saved
  state names the file by its path alone (`AUM Recordings/<file>`) with the
  switches above. AUM itself saves a bookmark beside the path, a reference to
  the file only the device can make; it loads the file without one (see
  [Status](#status)). Another file is another session: `load_song` authors it
  again.
- **A file that is not there.** AUM then loads the session with an empty
  player, which plays nothing and says nothing. So `check_song`, and with it
  `load_song`, opens every File Player and reads its panel: the file it has,
  its length in frames, and whether Loop or Sync is on. A player without the
  song's file fails the check, and so does one that loops or follows the
  tempo. A file track is played no note: a recording of a session begins with
  a bar of silence, so there is nothing to listen for.
- **`mix_song`** plays such a song from the sequencer and from the top, as
  `play_song` does, and reads its bars and sections out of that pass
  (`sections` may end it early; `from` is refused). `per_track` needs a tap on
  every track (`session: {track_taps: true}`): a solo pass per section would
  play the file's beginning every time.

AUM's Recordings folder is not in AUM's own Documents folder. It lies in the
storage AUM shares with AudioShare (an app group's File Provider storage), and
what `ios fsync --app=com.kymatica.AUM` reaches is the Documents folder alone.
The daemon therefore cannot list, copy or check a recording by itself: what it
knows of one it reads off AUM's screen (FILES → Recordings, a File Player's
panel).

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
- size: more than twelve tracks that are not grouped into at most twelve
  stems of at most twelve tracks each. In a song of
  [more than twelve tracks](#more-than-twelve-tracks) the channels above are
  checked within each stem;
- plugins, against the staged AUv3 probes: an unknown plugin, preset or
  parameter (with the names that come close), a value outside a parameter's
  range;
- a [mapping range](#mapping-ranges): one that is not two numbers, the lowest
  first, lies outside its parameter's range, names a parameter the plugin's
  params do not set or one parameter twice, and a value the song sets outside
  it;
- a [file track](#a-track-that-plays-a-file): a plugin beside the file, a path
  that is not inside AUM's Recordings or has no extension, a preset,
  parameter, kind or transposition, a pattern to play, a scene or a ramp that
  sets anything but its level, mute or effects;
- a control a scene changes without a declaration on the track;
- a [modulator](#modulators): a plugin that is none, or a modulator declared
  as an effect; a target the track does not have; a parameter with two
  modulators; a depth that is not two values, lies outside the parameter's
  range or runs downwards; a song that sets the modulator's routing; a scene
  or ramp that sets the parameter in a section where its modulator runs; a
  modulator that is bypassed somewhere while its target declares no value to
  hold there;
- a [sequence](#a-drum-machines-own-sequence): a voice, step or value that is
  none, a length shorter than what is written, a mode the lane lacks, a
  plugin without one a song can write, a preset beside it, a lane length or
  mode said both in the sequence and in params;
- in a calibrated song: a drum lane its kit has no pad for, a note that leaves
  the MIDI range once it is shifted for its preset.

`dry_run: true` checks without storing.

### Saving over a changed song

`save_song` replaces the stored song with the document it is given. When the
document was read with `get_song`, pass the `revision` that came with it: the
save is refused when the song is stored at another revision, because a
`patch_song` or a craft tool's `apply` changed it in between, and the
refusal names the stored revision, as `patch_song`'s does:

```
/revision: song "night_drive" is at revision 7c1e…, not 3fa0…: read it again with get_song and make the change against 7c1e…
```

Read the song again, make the change on what is stored now, and save with the
new revision. A dry run checks the revision too. Without `revision`,
`save_song` replaces the song whatever is stored, and a `revision` for a song
that is not stored yet is refused.

## The session

`author_song_session` builds one strip per part (a song of
[more than twelve tracks](#more-than-twelve-tracks) has no session of its own:
each of its stems has one, built the same way):

```
Brain     ProbeMidiBrain, the sequencer (a MIDI strip)
<track>   instrument > effects > modulators > fader   one strip per track
<track>   File Player > effects > fader         a track that plays a file
Master    master sum > fader > master effects > ProbeAudioTap
```

**MIDI.** One brain plays every track. Its output is routed to every instrument
through a filter that passes only that track's channel and its
`control_channels` (an effect with `midi: true` hears the track's channel
alone), and to AUM's MIDI Control through a filter that passes only the control
channels. A note therefore never fires a mapping, and a control message never
reaches a synth. A [modulator](#modulators)'s output goes to MIDI Control
alone, through the same filter. The
sequencer sits next to the brain and is routed the same way, to the same
destinations through the same filters.

| Channels | Carry |
|----------|-------|
| 1…N | the tracks' notes, one channel each, their control notes and their sustain pedal |
| N+1…14 | plugin bypasses and parameters, filled from 14 downwards; N is the highest channel a track plays or hears on |
| 15 | strip levels and mutes, the transport, the sequencer's bypass (CC 106) and the master's record-arm (CC 107), preset loads (Program Changes) |
| 16 | left to the rig: tap toggles and session switching |

**Mappings.** Every strip's level and mute and every plugin's bypass are mapped,
along with each parameter and each preset the song names. So is the master's
Rec enable, which arms it for AUM's own recorder ([Stems](#stems)); a File
Player has no mapping. A parameter with a [mapping range](#mapping-ranges) has
its mapping's range (AUM's 0 % and 100 %) set to it; every other mapping spans
its whole range. A preset becomes one
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
the layout, and so do a parameter's mapping range, the tempo (it is the
session's clock) and an
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
  lead_bars: 2          # the sequencer's lead-in before bar 1, at the least
```

`lead_bars` is for a song that plays recordings: see
[A track that plays a file](#a-track-that-plays-a-file).

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
| pull | The iPad's auv3-probe app fetches the file into AUM's folder. The app does that while it is in front; when the file is not fetched within a few seconds the daemon brings the app forward, starts its sync, and waits up to a minute: for the file, and for the app's report of what its sync made of it. A conflict or an error the app reports ends the wait at once: see below. The screen of an app that sends no report is read instead. Skipped when the iPad already has this very file. When the step fails, the daemon brings AUM back to the front: the app is not left there. |
| load | AUM is told to open the session (over WebDriverAgent: plugin windows and panels are closed first), and the call waits until AUM's main menu shows the session's name and AUM has finished restoring the plugins, up to 2 minutes in all. A heavy session can keep AUM's UI from answering for a minute, so the name is waited for as long as AUM shows that it is loading, and the menu is opened again when the load closed it; once the load is over the name gets 10 s more. The step fails, saying which session AUM shows, when AUM ends up with another session or none. When AUM is not running any more (it crashed under the load, or was closed), the step starts it again and loads the session once more: see below. |
| reload | Only when AUM draws a node of the session as failed after the load: the daemon reloads that node (RELOAD in its long-press menu, as `aum_node reload` does), waits 3 s for the plugin to come up and reads the mixer again, up to three times. The step names each plugin by the song (`pad's Perforator (insert slot 1)`) and says whether it came up. One that stays failed is the check's to report, and `next` then says to load the session again. `reload: false` leaves a failed node as it is. See below. |
| connect | The new session's ProbeMidiBrain and ProbeAudioTap dial the daemon. The master's tap dials in under the session's id, which tells the daemon that this very session is the one that came up. A tap that gives no name is taken after a few seconds, and the step says so. The session is the daemon's current one from then on: see "The current session" below. |
| check | `check_song`, below. |

**A plugin that failed to load.** AUM says that a plugin did not load only by
drawing its node with a ring of red stripes (the check reads that off a
screenshot, below). Some plugins fail as AUM initialises them on nearly every
load of a session, and come up when the node is reloaded: on the rig
Perforator came up failed after each of seven loads of the sessions that
hosted it, and a reload brought it back
([research/house-effects.md](research/house-effects.md#perforator)). Loading
the whole session again only throws the dice once more, so the load reloads
the node itself. A reload starts the plugin anew, and what was set on it is
gone: Perforator came back at its Init preset. The song's opening sound puts
its preset and parameters back: the check sends it before it plays a note,
and the sequencer sends it in the lead-in, at the transport's start, before
the song's first bar.

Loading replaces the session open in AUM, so `load_song` needs
`confirm: true`. Without the iPad UI tools (`ipad_wda_url` empty) it stops
after the pull and names the session to load by hand.

Bringing the probe app forward needs its bundle id, which carries a signing
prefix. A daemon on the host the iPad is plugged into looks it up. Any other
daemon remembers it from the first `ipad_launch` with the full id; from then
on the iPad tools take `app: probe` there too.

**A session the app does not pull.** After every sync cycle the app tells the
daemon what it made of each file: it posts a sync report to the daemon's
receiver (`POST /aum-session/sync`; the format is
[docs/aum-sync-report.md](https://github.com/teemow/auv3-probe/blob/main/docs/aum-sync-report.md)
in the app's repository). The report lists the files the app holds as
conflicts, those it could not copy with the reason, the copies still queued,
and the staging rev of the daemon's list of files that the cycle went by. The
pull step waits for the report of a cycle that saw the session staged, one
whose rev is the rev the session was staged at or a later one, and ends on
what it says of the session:

| The report lists the session | The pull step |
|------------------------------|---------------|
| as a conflict | ends as described below. |
| with an error | fails with the app's text for it. The app tries the copy again at its next cycle. |
| as a copy still queued | keeps waiting. When its time runs out it says that the app was on it. |
| nowhere | takes the app to hold the staged file. That is the file the daemon has just seen the app download, as a rule. When it saw no download the step says so, and it fails when the staged file is no longer the one authored: the app has then sent the copy in AUM's folder over it. |

A report that says the app copies no file at all (no AUM folder is linked, or
the folder cannot be read) fails the step with the app's text. Only a report
that arrives during the step counts: an earlier one may be from an app that
has been out of sight since. The app's own uploads move the staging rev on
too, so a report can be of an older rev than the daemon's current one and
cover the session all the same. The daemon therefore keeps the rev each
song's session was staged at and compares with that.

A daemon that cannot bring the app forward (no iPad UI tools, an app whose
bundle id it does not know) ends the step on the report as well, when somebody
else has the app in front. It has no look at a conflict's row then, and names
the conflict without picking a side.

An app from before the report tells the daemon nothing about a file it leaves
alone: it simply never asks for it. The reason is on its screen, so when no
report arrives during the step the pull step reads that instead: the "aum
sessions" tab, in the folder the session goes to, every few seconds.

The usual reason is a conflict. The app keeps AUM's folder and the daemon's
staged files alike, and a file that changed on both sides since it last synced
it is copied neither way. Its row reads "conflict — both sides changed" and
has two buttons, "keep device" and "keep controller". The pull step ends as
soon as the app reports the conflict, or its screen shows it on the session's
row:

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
  that under another name in AUM first if it is to be kept (`aum_files
  save`, which says whether AUM wrote the file: `docs/ipad-control.md`,
  "Saving a session").

Whose copy AUM's folder holds is on the session's row and nowhere else, so
the row is read in either case, and the side is picked there. An app with the
sync report names the file on the row's two buttons, in their accessibility
identifiers (`keep-device:<file path>`, `keep-controller:<file path>`), and
the daemon presses the button that names the session's file. In an app
without them the daemon walks the browser to the session's folder from its
root before it taps, and taps the button that follows that one row. The
browser's navigation bar names its folder as well (`aum-folder:<folder
path>`). The daemon goes by that to tell which folder is shown, and only when
the bar's title is the path's last part; it never chooses a button by it.
Other files the app holds as conflicts are never touched.

When the wait runs out, the step says what the app's last report said of the
session: that its copy was still queued, or that the report was of a rev from
before the session was staged. For an app without reports it says what the
app showed instead: that it does not reach the daemon ("controller offline"),
an error on the session's row, a browser that could not be read (no AUM
folder linked), or the tab's status line.

It also says how far the app's other sync was, the one that sends its audio
units to the daemon, which runs only while the app is in front. The report
has it as `audio_units`: `syncing` and `waiting` are named with their counts,
`done` is not, and `done` does not need every unit to have been sent (a unit
that does not instantiate fails at every attempt). For an app without reports
the step reads "syncing 2/184…" off the app's first tab when the app comes
forward. iPadOS drops the app while AUM is in front, and such an app sends
all its audio units again at every start (184 on the rig, a few minutes).
Sessions are pulled while that runs: see Status.

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

**AUM goes away under the load.** AUM crashed on the rig once while it
replaced the open session with the one to load, in the system's own code.
WebDriverAgent then answers every further call of its session with "The
application under test … is not running, possibly crashed", and the iPad shows
its Home Screen. The Home Screen keeps the iPad awake no more than the probe
app does.

A new WebDriverAgent session starts an app that is not running, and of the
session that was open nothing is left that the load would not have replaced.
So the load step opens a new session, which starts AUM, waits until AUM has
drawn its screen, and loads the session again. It does that once. The step's
line then begins with "AUM was not running" and what WebDriverAgent said.

When AUM is gone again, the step fails with "AUM crashed or is not running"
and what WebDriverAgent said both times. The session is not loaded a third
time. AUM is started once more with nothing loaded, so that it is in front
and the iPad stays awake. `next` says to load the song again, and where AUM's
crash report is on the iPad if AUM goes away at this session every time. When
AUM does not start then either, the step says that the iPad is on its Home
Screen and locks itself after its auto-lock time, and starting AUM comes first
in `next`. Where AUM never came up at all, the step says that AUM is not in
front instead: what was in front before still is.

Only the load step starts AUM again. Any other tool that drives AUM when it
goes away (the `aum_*` tools, `ipad_tap` and its kin, `check_song`'s look at
the mixer) fails with WebDriverAgent's words, what they mean and what to do.
It does not make its gestures again: the session they were meant for is gone,
and an AUM started anew shows another screen. `ipad_status` and
`ipad_screenshot` end with a note that the Home Screen is in front until AUM
is brought forward again. The next call that drives AUM starts it, as every
WebDriverAgent session bound to AUM does.

`check_song` answers whether the song will sound if it is played now:

- AUM has the song's session open. The session's master tap says so: it is
  connected under the session's id. When another session's master tap is
  connected instead, the check ends there and plays nothing;
- the brain is connected and the master tap is streaming. For a session with
  `track_taps` the check also says which tracks' taps are connected;
- the mixer has a strip for the brain, every track and the master, and no
  failed plugin. AUM says that a plugin did not load only by drawing its node
  with a ring of red stripes, so the check takes a screenshot at every stop
  of its walk along the mixer and looks at the ring of every node, strips
  scrolled out of the window included. A screenshot that cannot be had fails
  the read rather than passing the mixer as clean, so a stem is not recorded
  on it. `check_song` reloads nothing: for a
  failed node its `next` names `load_song`, which reloads it, and the
  `aum_node reload` call (strip and node) that reloads it once;
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

**The current session.** The daemon keeps one session as its current one: the
session whose mappings the `control_*` tools speak, and whose control surface
the brain is sent when it connects (`aum_auto_import`, see
`docs/aum-brain-control.md`). It was the session the iPad had downloaded
last. A download is a guess at what AUM will load, and the guess is wrong
when a session is loaded that the iPad holds already. On the rig a song was
loaded, then a second one, which the iPad downloaded, then the first again,
which it did not: the first song's brain was sent the second song's control
surface.

The open session decides now. When a master tap dials in under the id of a
staged session, that session becomes the current one. Its rig is imported and
its surface pushed, unless both are in place since this brain connected. The
connect step of `load_song` does the same for the session it loaded, which
covers a tap that gives no name. So after a load the current session, the rig
and the brain's surface are the loaded session's, with or without a download.

A download, a session switch and `import_aum_session` set the current session
as before: for a session whose taps give no name they are all the daemon has.
Two things follow. Between a brain's connect and its session's tap the brain
can still be sent the surface of the session downloaded last; the tap's
connect replaces it. And a session the iPad downloads while another one stays
open and connected becomes the current one at once, until the open session's
master tap dials in again or a session is loaded.

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
loads go out as the transport starts, every other control 600 ms later, the
parameters of the plugins those switch back on 200 ms after that, and the
song's first note on the bar line after. The song's bar 1 is therefore
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
sound — presets, then a pause for them to load, then everything else, and
200 ms later the parameters of the plugins that switched back on — and then
sends the timeline.

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

## Stems

Every instrument of a song is computed while the song plays. Past what the
iPad computes in time, AUM drops audio: on the rig a piece for a piano and
eleven modelled instruments read 93 to 100 % in AUM's DSP display in its
tutti, and a pass of it came out with gaps. Such a song is recorded a group of
tracks at a time, each group alone, and mixed from the recordings.

| Step | Tool | |
|------|------|-|
| 1. Group | `group:` on the tracks, or `groups` as an argument | Tracks that share a group are one stem. A track without a group is a stem of its own, called by its id, and no group may have that name. |
| 2. Record | `record_song_stems`, `get_song_stems` | Each stem from a session of its own, with AUM's own recorder. |
| 3. Mix | `mix_song_stems` | Writes the mix as a song of [file tracks](#a-track-that-plays-a-file), loads it, plays and records it. |

```yaml
tracks:
  - id: piano
    instrument: {plugin: grand}
  - id: violins
    group: strings
    instrument: {plugin: bowed, preset: Violins}
  - id: cellos
    group: strings
    instrument: {plugin: bowed, preset: Cellos}
    effects:
      - {id: comp, plugin: compressor}
master:
  - {id: hall, plugin: hall, params: {Mix: 0.2}}
  - {id: limiter, plugin: limiter}
```

```
record_song_stems {song: night_drive, confirm: true}
get_song_stems    {wait_ms: 55000}                     # until it is done
mix_song_stems    {song: night_drive, confirm: true}
```

That records `piano` and `strings`, one after the other, and leaves three
things: two files in AUM's Recordings, the song `night_drive_mix`, and a
recording of the mix at the master's tap. `groups: {low: [piano, cellos]}` as
an argument groups the tracks another way for that call.

**A stem's session** hosts the stem's tracks and nothing else of the song, so
the other tracks cost the iPad nothing while it is recorded. The tracks are as
the whole song has them: plugins, effects, faders, scenes, ramps and MIDI
channels, for the same bars. It is staged as `Songs/<title> stem <stem>`, has
no tap on its tracks whatever the song's `session:` says, and needs the
sequencer: the stem is played from it, locked to AUM's transport.

**The master's effects are in the mix, all of them.** A stem's master strip is
empty. A limiter has to see the sum of the stems, so it cannot be in a stem. A
hall on the master is linear, and one on each stem would add up to the same
sound; it is left to the mix all the same. It is then one reverb on the sum, as
in the whole song, a scene or a ramp of it works as written, and the stems stay
dry of it and can be mixed again another way.

**Lead-in and tail.** Every stem's session and the mix's play the same
lead-in: the bars a session takes whose opening sound loads presets (one bar at
most tempos), or the song's own `lead_bars` when it asks for more. After the
song's last bar every one of them runs on for the same tail, `tail_bars` (by
default the bars that cover four seconds), in the last section's sound: its
scenes, and every control it ramped held where the ramp ended, so a fade that
ended at silence stays there. What rings out in a stem is in its file.

**A pass.** The daemon loads the stem's session (`load_song`'s steps, the
reload of a failed plugin and the check included; what the check finds is
noted with the take and does not stop it). A plugin that AUM still draws as
failed after the load reloaded it does stop it: the stem is not recorded over
a plugin that plays nothing or passes nothing on. The stem's report says which
plugin stayed failed and how often it was reloaded, and the recording ends
there; `record_song_stems` again loads the stem's session anew. A plugin that
came up when it was reloaded is noted with the take. The reload started it
anew, and the stem's sequence loads its preset and sets its parameters in the
lead-in, at the transport's start, as for any other plugin. It switches AUM's recorder off, in case one was left on, reads AUM's list
of the session's recordings, and arms the master strip: by the master's mapped
Rec enable, and then it reads the REC button off AUM's screen, which it taps
when the mapping did not arm it. Then it switches the recorder on, which
starts the transport: the sequencer plays the stem from the top.

**One message ends the take, and the stem's own sequence sends it**: the
recorder's off, half a beat before the tail's last bar line. AUM ends a take
on the next bar line, so the take ends there, as long as the lead-in, the song
and the tail. The daemon sends no off of its own meanwhile, because a second
off before that bar line withdraws the first and the recorder runs on (see
[Status](#status)). It also means a take ends by itself when the daemon that
started it is gone: a recorder that nobody switches off records on through a
stopped transport. The transport is stopped a bar after the stem's end, and
the master disarmed.

When AUM's list holds no new take after that, the sequence has not ended it.
The daemon then switches the recorder off itself, once, with the transport
stopped, and reads the list again. Such a take ran on after the stem and is
not whole beats long: the report says so, and the mix is not played from it
(below).

**Looking up the take.** Each reading walks FILES → Recordings from its root
to the session's folder, whatever FILES showed last (see
[ipad-control.md](ipad-control.md#listing-and-loading-sessions)), and reads
as much as tells the take, no more:

- AUM's list sorted LAST OPENED, as it was on the iPad, shows a folder's
  newest take first. The daemon then reads the folder's first page, before
  the pass and after it, and does not scroll it to its end: the take is
  there. A list sorted otherwise is read whole, both times. A folder read at
  its top before the pass and whole after it (its sort was changed in
  between) says nothing of which take is new: the stem is reported as not
  recorded, and the recorder is switched off.
- A folder the root does not hold has no take yet. That is said only once
  the root was read through, and that reading answers for the later stems of
  the same recording whose folders it did not hold either: AUM makes a
  session's folder with its first take, and only the stem's own pass, which
  comes later, makes one into it. So of a new song's stems only the first
  reads the root through.

On an iPad whose root of Recordings held 90 folders (AUM 1.4.8), one reading
took 25 to 28 s for a folder on the root's first page (34 s read whole), 85 s
for one on its ninth page holding 22 takes (102 s whole), and 116 s for a
folder the root does not hold. So a stem whose folder is near the top of the
root spends about 50 s reading, and of a new song's stems the first about
140 s and each one after it about 25 s, the reading after its pass, its new
folder being at the top of the root (each about 150 s while every reading
took the folder whole and every new stem read the root through). The first
reading after a pass ends about 25 s after the stem on such an iPad, and a
take the sequence did not end runs on that long.
`stop_song` ends a pass, and the whole recording: the transport stops and the
recorder is switched off.

**The take** is the file AUM's list has afterwards and had not before, the
one with the highest number when there are several. AUM names it
`<session>/Master - <tempo> bpm - <nnn>.wav`, counting up in the session's
folder. The list gives a file's date, size and format, and no length, and is
not always in the order of the takes; sorted LAST OPENED it shows the newest
one first.

**What is kept.** The daemon notes down every take: the stem and its tracks,
the file, and a fingerprint of the stem's session and of what its sequencer
played. `record_song_stems` records the stems that have no take of the song as
it is now, so after a change to one track only that track's stem is recorded
again, and a recording that failed half way goes on where it stopped.
`again: true` records a stem although its take is current, `stems` picks some.
Takes made with another lead-in or tail are not kept beside new ones: they
would not line up.

**The report** of `get_song_stems` lists each stem with its tracks, the file's
path in AUM's Recordings, what AUM's list says of it, the level the master's
tap heard during the pass, and what the load's check found. With `song` and no
recording running it reports the takes kept for that song, and whether each is
still of the song as it is.

**The mix.** `mix_song_stems` writes a song `<song>_mix` (`mix` names another
id): one track per stem that plays the stem's file through the stem's strip
(below; at unity and dry for a stem without one), the whole song's master
effects with their scenes and ramps, the same sections, lead-in and tail. It
is a song like any other: `load_song`, `play_song`, `check_song` and
`mix_song` take it. Running `mix_song_stems` again writes it anew, so a stem's
level and effects in the mix are changed in the song. Then the call loads it and
plays it from its sequencer, recorded at the master's tap as `play_song` with
`record: true` does (`record: false`, `play: false` and `upload` as there).

**A stem's strip in the mix.** A stem's track in the mix is its strip there,
and the song can say what that strip is: under `stems`, by the stem's name, a
`level` in dB and `effects`, a chain as on any track.

```yaml
stems:
  strings:
    effects:
      - {id: eq, plugin: eq, params: {Low Cut: 80}}
      - {id: room, plugin: hall, params: {Mix: 12}}
  brass:
    level: -2
    effects:
      - {id: eq, plugin: eq, params: {Low Cut: 40, High Shelf: -2}}
      - {id: room, plugin: hall, params: {Mix: 30}}
      - {id: comp, plugin: compressor, params: {Ratio: 2}}
```

`mix_song_stems` writes the strip onto the stem's file track: the recording
plays through those effects at that level, before the master's. That is where
a row of an orchestra gets the room it sits in, its own equalizer or a
compressor of its own, on the recording of its group. A stem the song leaves
out plays at unity and dry, as before. A stem is recorded without its strip,
so a change to `stems` needs no new take: it is a new mix of the same takes,
and `mix_song_stems` with the takes as they are writes it. `save_song`
checks the strips' plugins, presets and parameters against the staged
plugins, and names a mistake with its place under `stems`; a strip for a stem
the song does not have is an error. `get_song` lists each stem with its strip,
and the mix's report each file with the strip it plays through.

A strip is written for the tracks of the document's own stem. Takes recorded
with `groups` (another grouping) are of other stems: their mix plays them at
unity and dry, even a stem that has a strip's name but other tracks, and
`mix_song_stems` and `get_song_stems` name the strips left out.

**Length.** A take's exact length is read where the daemon can read it: in the
panel of the File Player that has it loaded. So it is the mix's load that
reports, for every stem, the frames, the seconds, the beats, and whether that
is a whole number of beats; `get_song_stems` has it from then on. A take that
began with the transport and ended on a bar line is whole beats long (on a
device 4 to 7 frames short of them, 0.0003 of a beat at most). `mix_song_stems` does not
play a mix in which a take is not whole beats long, or shorter than the
lead-in, the song and the tail: that take did not start with the transport or
lost its end, and the stems would not line up. It also refuses a stem without
a take, and one whose take is of the song as it was before a change (`stale:
true` mixes that one all the same).

**What AUM's own recorder does that the tap does not:**

| | AUM's recorder | A ProbeAudioTap |
|-|----------------|-----------------|
| Written | on the iPad, by AUM, into its Recordings | on the daemon's host, from a stream |
| The way there | none: no wireless link, nothing to stall or arrive late | every sample over the wireless link; a tap on each of thirteen tracks was more than the link carried |
| Begins | on the transport's first sample | when the stream is asked for; cut to the first sound afterwards |
| Ends | on a bar line | when the tap has been silent for a while |
| Format | WAV in the bit depth of AUM's Bit Depth setting (16 bit on the rig), at the session's rate | 32-bit float as streamed, stored as FLAC or WAV |
| Reached | by AUM's File Player and file browser; not from outside (see [A track that plays a file](#a-track-that-plays-a-file)) | a file on the host, which `upload_recording` puts into Google Drive |

So the stems are recorded in AUM, where they line up on the sample and no link
is in the way, and the mix, which is light, is recorded at the tap, where the
daemon can reach the file. The mix can lose audio to the link as any tap
recording can; its record says so (see [recording.md](recording.md)).

**AUM's settings are left alone.** Buffer size and bit depth are AUM's global
settings, and nothing here changes them. A session's clock options are in its
file: sessions are authored without a pre-roll.

**Not done.** A stem is recorded at the master, as one file: the strips of a
stem are not recorded one by one. A stem whose tracks alone are still too
heavy has to be split into two groups.

### More than twelve tracks

One session hosts twelve tracks: each takes a MIDI channel, and the channels
above them carry the session's controls ([The session](#the-session)). A song
may have more when its stems are all that is played of it: every stem holds
at most twelve tracks, and there are at most twelve stems, because the mix
has a track for each. So a large piece is one document:

```yaml
title: Chamber Piece
tempo: 96

tracks:
  - {id: violins1, group: strings, instrument: {plugin: bowed, preset: Violins}}
  - {id: violins2, group: strings, instrument: {plugin: bowed, preset: Violins}}
  - {id: violas,   group: strings, instrument: {plugin: bowed, preset: Violas}}
  - {id: cellos,   group: strings, instrument: {plugin: bowed, preset: Cellos}}
  - {id: basses,   group: strings, instrument: {plugin: bowed, preset: Basses}}
  - {id: flute,    group: winds,   instrument: {plugin: reed, preset: Flute}}
  - {id: oboe,     group: winds,   instrument: {plugin: reed, preset: Oboe}}
  - {id: clarinet, group: winds,   instrument: {plugin: reed, preset: Clarinet}}
  - {id: bassoon,  group: winds,   instrument: {plugin: reed, preset: Bassoon}}
  - {id: horns,    group: brass,   instrument: {plugin: brass, preset: Horns}}
  - {id: trumpets, group: brass,   instrument: {plugin: brass, preset: Trumpets}}
  - {id: trombone, group: brass,   instrument: {plugin: brass, preset: Trombone}}
  - {id: piano,    instrument: {plugin: grand}}              # a stem of its own
  - {id: timpani,  kind: drums, instrument: {plugin: drumbox}}

master:
  - {id: hall, plugin: hall, params: {Mix: 0.2}}
  - {id: limiter, plugin: limiter}

stems:                                    # the rows of the stage, nearer and farther
  strings: {effects: [{id: room, plugin: hall, params: {Mix: 0.1}}]}
  brass:   {level: -2, effects: [{id: room, plugin: hall, params: {Mix: 0.3}}]}

patterns:
  line:  {step: 1/8, notes: "A4 . C5 . E5 . C5 . | F4 . A4 . C5 . A4 ."}
  chord: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - - | [A3 C4 F4] - - -"}
  low:   {step: 1/2, gate: 1, notes: "A1 - | F1 -"}
  roll:  {drums: {tom_low: "x.......x......."}}

sections:
  strings:
    bars: 8
    play: {violins1: line, violins2: chord, violas: chord, cellos: low, basses: low}
  tutti:
    bars: 8
    play: {violins1: line, violins2: chord, violas: chord, cellos: low, basses: low,
           flute: line, oboe: line, clarinet: chord, bassoon: low,
           horns: chord, trumpets: line, trombone: low,
           piano: chord, timpani: roll}

arrangement: [strings, tutti]
```

`save_song` checks it and lists it by its stems:

```
saved "Chamber Piece" (chamber_piece): 96 BPM, 16 bars, 0:40
14 tracks in 5 stems: more than the 12 one session hosts, so the song has no session of its own and plays as its stems and their mix
arrangement:
   1  strings      bars 1-8  0:00
   2  tutti        bars 9-16  0:20
stems (each is recorded alone, from a session of its own that hosts its tracks; a track's MIDI channel is its channel there):
  strings: 5 track(s)
    ch1  violins1   Bowed Strings  (64 notes)
    ch2  violins2   Bowed Strings  (48 notes)
    ch3  violas     Bowed Strings  (48 notes)
    ch4  cellos     Bowed Strings  (16 notes)
    ch5  basses     Bowed Strings  (16 notes)
  winds: 4 track(s)
    ch1  flute      Reeds  (32 notes)
    ...
  piano: 1 track(s)
    ch1  piano      Grand Piano  (24 notes)
  timpani: 1 track(s)
    ch1  timpani    DrumBox  (16 notes)
  master: Hall > Limiter (in the mix: a stem is recorded without the master's effects)
takes: 0 of the 5 stems have a take of the song as it is now
next: record_song_stems {song: "chamber_piece", confirm: true} records the stems without a current take (get_song_stems follows it), then mix_song_stems {song: "chamber_piece", confirm: true}
```

```
record_song_stems {song: chamber_piece, confirm: true}
get_song_stems    {wait_ms: 55000}                     # until it is done
mix_song_stems    {song: chamber_piece, confirm: true}
```

That loads five sessions, one after the other, records a pass of each, and
leaves the song `chamber_piece_mix`: five file tracks under the hall and the
limiter. From then on the mix is the song that is loaded, played, checked and
measured, and a stem's level is its track's `level` there. After a change to
the flute, `record_song_stems` records `winds` again and nothing else.

**The check.** More than twelve tracks are accepted only when the grouping
fits: at most twelve tracks in every stem, and at most twelve stems. A track
without a group is a stem of its own, so thirteen tracks without a `group`
are thirteen stems and refused; the error names the stems and what does not
fit. Twelve stems of twelve tracks, 144 tracks, are the most. A grouping
given to `record_song_stems` as `groups` is held to the same.

**MIDI channels are counted in each stem.** A stem's session hosts its tracks
and no others, so a track plays on its place in its stem: the first track of
every stem on channel 1 (`violins1`, `flute`, `horns`, `piano` and `timpani`
above). `channel` names another of the twelve and `control_channels` further
ones, and both are taken within the stem alone: two tracks of different stems
may name the same channel, two of one stem may not, and a control channel
must be free in its track's stem. A stem's controls fill the channels above
its own tracks (see [The session](#the-session)), so a stem of five tracks
has channels 6 to 14 for its bypasses and parameters. With `groups` as an
argument the channels are counted in those groups; a track that names its
`channel` keeps it there.

**What the tools do with such a song:**

| Tool | |
|------|-|
| `save_song`, `get_song` | Every plugin, preset and parameter is resolved on the whole document, so a problem is reported with its place there. Then each stem is planned and compiled as the song it is recorded from, which says what only a session can: more controls than its channels hold, a calibrated preset's lanes. The summary lists the tracks stem by stem, each with its channel in its stem, and says which stems have a take of the song as it is now. |
| `list_songs` | Lists it with its tracks and its stems. |
| `record_song_stems`, `get_song_stems`, `mix_song_stems` | As for any song: a session and a pass for each stem, then the mix. |
| `load_song`, `play_song`, `check_song`, `mix_song`, `author_song_session`, `audition_sound` with the song | Refuse: the song has no session of its own. The answer names `record_song_stems`, `mix_song_stems` and the mix, which all of them take. |
| `export_song_midi` | Exports the notes alone, a track per song track on its channel in its stem. The control messages belong to a stem's session each and are left out. |

**A song of twelve tracks or fewer is one session, as before**, whether or
not it names groups, and its stems keep the channels the tracks have in the
whole song. A song that grows past twelve tracks counts its channels in its
stems from then on. Every stem whose tracks move to another channel by that
is another session, and its take is of the song as it was: `record_song_stems`
records those stems again, and keeps the takes of the others.

**Not done.** The whole song cannot be heard in one pass, by its nature:
`play_song` of a section or of some tracks, and `mix_song`'s measurement of
every track, need a session that hosts them. A stem's tracks can be heard and
measured together by saving them as a song of their own. The mix has a fader
for each stem, not for each track: a track's level is changed in the song,
and its stem recorded again.

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
  open in all 24. Parameter changes that reach a plugin in the same burst as
  its un-bypass are lost: an LFOH bypassed by one section's state and
  brought back by the next, with its channel and its rate changed in the
  same messages, came back with neither, and took the channel when it was
  sent again alone. Going into the bypass, a change sent with it held. A
  reverb (AUReverb2, its Dry/Wet Mix at 0 or 100, a short kick through it
  read at the master tap: peak -9.0 dBFS dry, -17.7 dBFS wet) showed the
  rest. Its mix sent with the un-bypass, or just before it in the same burst,
  was lost every time (7 of 7); 1 to 3 ms after it, 7 times in 13; 5 ms after
  it, once in 6; 10 to 100 ms after it, never (17 tries). A mix sent 300 ms
  into the bypass was lost as well: un-bypassed alone 300 ms later, the
  reverb came back with its old mix (5 of 5). Going into the bypass the mix
  sent with it held (3 of 3). Played as a song by the daemon, a section that
  switched the reverb on with a new mix stayed dry, and one that switched it
  on after its mix had changed during the bypass came back wet. Over the
  network the un-bypass sometimes takes effect later still: a mix sent again
  50 ms after it was lost 2 times in 105, 100 ms after it once in 115, 200 ms
  after it never in 43, and a mix changed with the reverb on never in 48. A
  few readings whose clip of the tap began at the kick are left out. So a
  song sends a plugin's parameters again 200 ms after its un-bypass (see
  [Sound](#sound)). Replayed through `probe_sound` with the messages and the
  timing the song compiles to, both sections sounded as written, the one
  switched on with a new mix wet and the other dry (43 of 43).
- **Parameters.** A parameter's CC runs linearly over the parameter's range:
  0, 51, 64 and 127 put a reverb's mix at 0 %, 40 %, 50 % and 100 %.
- **Mapping ranges.** Not measured on a device yet. The session stores a
  [mapping range](#mapping-ranges) as its two ends' places in the
  parameter's range, and the song sends a value as its place between those
  two ends, both linear in the parameter's value, as AUM maps a whole range.
  Whether AUM does the same for a parameter its plugin shows on a log scale
  (a filter's frequency), or follows the plugin's taper, is the measurement
  of https://github.com/teemow/mcp-midi-controller/issues/229, with an 80 Hz
  high-pass set through a range and read at the tap. Until then a range's
  precision is the step it is measured to have, not the one computed
  (`aumPosition` and `along` in `internal/song/range.go` are the law).
- **Modulators.** A scratch song of one NuSaw pad, whose Cutoff an LFOH
  swept between 0.1 and 0.9 in sync at 120 BPM, recorded at the master tap
  and measured every 100 ms. Its spectral centroid ran between about 60 Hz
  and 5.5 kHz once a beat, 16 cycles without drift. So the LFOH sent the
  controller and channel of Cutoff's mapping: CC 3 on channel 14, its channel
  sent as 111 of 127. Bypassed by a scene (bypassed in AUM and sent to
  channel 1 at once), it stopped, and Cutoff held its declared 0.5 at about
  610 Hz. Parked by hand while it ran (channel 1 alone), it stopped, and sent
  back to channel 14 it ran again at once. With `Time Numerator` at 0.07 the
  cycle took two beats. AUM's bypass of the plugin also stopped it, and lifted
  alone it ran again; lifted together with the routing it stayed parked (see
  **Bypass and mute**), which is why the song parks a bypassed modulator and
  leaves AUM's bypass off. With the song parking a bypassed modulator and
  leaving AUM's bypass off, the sweep stopped in the bypassed section and
  came back at once at the next, with the rate its scene gave it.
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
- **The sync report on the rig.** With the app that sends the report on the
  iPad and a daemon that takes it, the daemon's log had a line for the app's
  report whenever its counts changed: one before the first copy of a cycle,
  with the copies queued, and one at the cycle's end. After a cycle in which
  the app had uploaded six files, its report was of a rev six behind the
  daemon's, as expected. `load_song` of a new song: the app reported the
  session as a copy queued, the daemon saw the download, the next report
  listed the session nowhere, and the pull step ended after 25 s with "the
  iPad pulled the session". The app had been started afresh in that time.
  The same session was then saved in AUM and staged again with another note.
  The app's report counted one conflict more, and the step failed after 26 s
  with the conflict, the copy's 159 KB against the 105 KB staged, and what to
  do. No side was picked, and AUM was in front again. The step's text is the
  same whether the report or the app's screen told it of the conflict, so the
  run does not show which of the two it ended on.
  In the app's tree every conflict row had both identifiers and the
  navigation bar had its folder's path, and the daemon's reader took each
  row's file path from them. A search for the button by its identifier found
  one button in a folder where the label "keep controller" found five. A
  click on it, made from outside the daemon on that session's row while the
  row was scrolled out of sight, had the app fetch the staged file and report
  one conflict fewer. The app did not upload the fetched file back in the
  minute it then stayed in front, and the staging rev stood still. An app
  that was started again reported its audio units as `done` at once (183 of
  184; one unit fails at every start). The first report after each start
  came without `audio_units`.
- **What that run showed beside the report.** When the probe app came
  forward, the brain and the tap of the session open in AUM lost their
  connections to the daemon. With AUM in front again neither dialled anew,
  for two minutes, until the session was loaded again. So after a pull that
  fails, the session AUM still shows is without its brain and tap until it is
  loaded once more. AUM itself crashed once, while the load step replaced a
  session that hosted two of Apple's own audio units, a few minutes after
  the probe app had been installed anew. Its crash report shows a
  segmentation fault in the system's own code, reading a parameter of an
  audio unit. The step failed with WebDriverAgent's "is not running,
  possibly crashed" and left the Home Screen in front, where nothing keeps
  the iPad from locking itself. AUM had to be started again.

Measured on a device with AUM's own recorder and its File Player (AUM
1.4.8), in sessions authored from a song of two stems, eight bars at 120 BPM
with a lead-in of one bar and a tail of two. The sessions were authored with
the code that is in the daemon now and staged through its receiver; the
controls were sent with `send_midi`, before the tools existed:

- **The recorder's mappings latch by value.** The master's Rec enable, mapped
  to a controller, armed the strip at 127, left it armed at a second 127 and
  disarmed it at 0. The transport's Toggle Record is no toggle either: 127
  switched the recorder on and started the stopped transport with it, a second
  127 changed nothing, and 0 ended the take.
- **A recorder left on records on.** With the recorder not switched off (the
  second 127 above), stopping the transport ended nothing: the record button
  stayed lit, the master's arm could not be changed by its mapping, and no
  file was listed. Switched off three minutes later, with the transport
  stopped, it left one take of 2:56.9, 353.909 beats: it had recorded through
  the stopped transport, and ended at once, not on a bar line.
- **A take ends on the next bar line.** Recorder on, and off 22.02 s after the
  message that started it, 20 ms after the end of the eleventh bar by the
  daemon's clock: the take was 1 151 993 frames at 48 kHz, 7 frames short of
  twelve bars (24 s, 48 beats). AUM had ended it on the bar line after the
  one it was aimed at. The recorder is therefore switched off half a beat
  before the last bar line.
- **The take's name and what AUM lists of it.** The takes were `Master - 120
  bpm - 001` and `- 002` in a folder named after the session, in FILES →
  Recordings. The list printed each as `12. Nov 2026 at 21:23:42 • 4.4 MiB •
  wav`: a date, a size, a format, the name without its extension, no length.
  They were 16 bit, AUM's Bit Depth setting as it stood.
- **A File Player written with a path alone loads its file.** A session
  authored with two file tracks, each File Player's saved state naming its
  take as `AUM Recordings/<session>/Master - 120 bpm - 00n.wav` and carrying
  no bookmark, loaded with both files in place. The panel of each showed the
  file's name with its extension, `Duration 1'151'993 frames · 00:23.9`,
  `Format WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz`, Loop OFF, Sync OFF, the
  number of beats (47.9997), rate 1.000 and beat offset 0.
- **It plays with the transport.** Started from the top, the master's tap was
  silent through the lead-in and carried the take's notes from the second bar
  (the written C2, read back at 65.5 Hz), through the hall and the limiter on
  the master. AUM showed 8 to 9 % DSP with the two players and the two master
  effects.
- **A second off withdraws the first.** `record_song_stems` as first merged
  had the daemon send the recorder's off about when the stem's sequence sends
  its own. Run on the rig, its first pass left the recorder on: AUM listed no
  new take, the tool said so and ended, and the record button was still lit
  minutes later; switched off by hand the take was 33.2 MiB, three minutes
  for 22 s of music. By hand in the same session: the off sent twice, 150 ms
  apart, in the fifth bar of a pass, and the recorder was still on a bar and
  a half later; the sequence's own off then ended that take at its end.
- **The sequence's own off ends the take where the stem ends.** A pass with
  the recorder switched on by hand and nothing sent after it: at 24.5 s the
  record button was idle while the transport still ran, and the take was 4.0
  MiB at 16 bit, 22 s: the lead-in, the song and the tail. The same in the
  pass above, once its two offs had cancelled each other.
- **AUM's list of a folder is not in the order of the takes.** Read after
  each take, it gave 002, 001; then 003, 001, 002; then 005, 004, 003.

Then the same song went through the tools of a daemon that has them, one
pass of the whole first, then its stems and their mix (two stems: one track,
and two tracks; a hall and a limiter on the master):

- **`record_song_stems`.** Both stems in 4:38: a session loaded for each
  (about 40 and 80 s), a pass of 22 s, the take found in AUM's list each time. The
  master was armed by its mapping alone, and no take needed the daemon's own
  off. The takes were `Master - 120 bpm - 006.wav` in the folder of the first
  stem's session, which held five earlier takes, and `- 001.wav` in the
  second's.
- **The takes' lengths**, read by `mix_song_stems` from the File Players of
  the loaded mix: 1 055 994 and 1 055 996 frames at 48 kHz, 6 and 4 frames
  short of 22 s. Both are 44 beats, the lead-in, the song and the tail; the
  two differ by 0.04 ms.
- **`mix_song_stems`.** It wrote the mix (two file tracks, the hall and the
  limiter), loaded it in 81 s (the iPad pulled it in 27 s, AUM had it open
  after 53 s more), found each File Player with its file, and played it from
  the sequencer with the tap recording.
- **The mix against the one pass**, both recorded at the master's tap, each
  placed by its own first note:

  | | One pass, rms / peak dBFS | Mix of the stems | Difference |
  |-|---------------------------|------------------|------------|
  | first stem alone, 2 bars | -32.3 / -14.8 | -32.2 / -14.6 | +0.1 / +0.2 |
  | second stem alone, 2 bars | -33.1 / -13.8 | -33.0 / -13.6 | +0.0 / +0.2 |
  | in turns, 2 bars | -32.6 / -12.9 | -32.6 / -13.0 | -0.1 / -0.1 |
  | together, 2 bars | -24.2 / -7.4 | -24.2 / -7.4 | 0.0 / 0.0 |
  | the whole | -28.7 / -7.4 | -28.6 / -7.4 | +0.0 / 0.0 |

  So the limiter, which the together section drives, and the hall work on the
  sum of the stems as they do on the instruments.
- **Where the stems sit against each other.** Every note's onset in the mix
  against the same note's in the one pass, 12 notes of each stem where it
  plays alone or the two take turns: the first stem's -0.02 ms on average
  (-0.17 to +0.08), the second's -0.07 (-0.40 to +0.38). The distance between
  the two stems' notes differs between the mix and the one pass by 0.06 ms
  where each plays alone and by 0.02 ms where they take turns. Where both
  play at once the reading does not tell them apart.
- **A full disk.** The first recording of the mix failed with "no space left
  on device" on the daemon's host; the mix had played. Played again with room
  on the disk, it was recorded.

**Other metres.** Songs in 6/8 (92 BPM), 7/8 (148 BPM, grouped 2+2+3) and 3/4
(108 BPM), each a little over a minute, were played from the session's
sequencer from the first bar to the last with their bars in place: the hats of
the 6/8 song sat on its eighth-note grid in its first bar and in its 35th.

Not yet measured:

- the parameters a song sends again after an un-bypass, played by the
  daemon's player or by the session's sequencer, and on a plugin other than a
  reverb: they were replayed through `probe_sound` with the song's own
  messages and timing (see **Bypass and mute**);
- of the stems tools: the daemon's own off after a pass whose sequence sent
  none, `stop_song` during a pass, a take that is not whole beats long met by
  `mix_song_stems`, a stem of many strips whose master is off screen when its
  REC button is read, a File Player whose file is missing (the check reads
  its panel; what AUM shows there for an empty player was not looked at), a
  song long or heavy enough to need its stems, and a mix with a scene or a
  ramp on its master. The tests run the first four against a stand-in whose
  recorder latches by value, withdraws an end at a second off, ends a take
  on a bar line and lists it the way AUM does, and whose File Players show
  what the rig's showed;
- a song of [more than twelve tracks](#more-than-twelve-tracks): none has
  been recorded on the rig. The tests record and mix one of thirteen tracks in
  two stems against the stand-in, each stem from a session whose channels
  begin at 1;
- `mix_song` and `check_song` called by themselves on a song of file tracks
  (the check ran as part of the mix's load);
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
- of the probe app's sync report: a pull step known to have ended on the
  report and not on the app's screen, a conflict the daemon resolves by
  itself on a report (the copy has to be the size of a file it staged; on
  the rig the click by identifier was made from outside the daemon), a
  report that lists the session with an error, one that says the app copies
  no file at all, and a pull step on a daemon that has no look at the app.
  The tests run these against a stand-in app that posts its reports over
  HTTP;
- `get_song_playback` against AUM's own position display (it was measured
  against the sound);
- a sequenced song on a host that dozes;
- AUM started again by the load step, and the current session taken from the
  open session. Neither has run on the rig. The tests run a load against a
  stand-in whose AUM goes away at the tap on the session's row and is started
  by the next WebDriverAgent session, or is not; and three loads in a row,
  the third without a download, against a stand-in whose taps dial in under
  their sessions' ids, or under none. What an AUM that was just started shows
  on the iPad, and how long it takes to draw it, was not looked at: the
  stand-in shows AUM's start screen after two reads;
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
- of [modulators](#modulators): a scene's `depth`, a ramp of a
  modulator's parameter, a modulator on an effect, several at once, what
  LFOH's factory presets hold for its routing, and the other steps of `Time
  Denominator`. The tests read the authored session back and compile the
  sections' states;
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
| The takes of a song's stems | `song-stems.json` in the state dir; the files themselves are in AUM's Recordings on the iPad |
| MIDI exports | `song-exports/` in the state dir |
| Recordings | `recordings/` in the state dir ([recording.md](recording.md)) |

A song names the plugins of one rig. Keep the songs directory out of a public
repository if that is private.
