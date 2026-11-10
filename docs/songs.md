# Songs

A song is a multi-track arrangement written as one YAML document. From it the
daemon makes two things: the **AUM session** that hosts the song's instruments
and effects, and the **MIDI** that plays it. AUM has no sequencer of its own, so
the notes come from the ProbeMidiBrain hosted in the session: the daemon sends
it every note and control change when it is due.

This is what lets an agent be asked for a whole song — "four synths with their
effects, a drum machine, a different sound in the chorus" — and deliver one
that plays inside AUM.

## The loop

| Step | Tool | |
|------|------|-|
| 1. Look | `list_song_plugins`, `get_song_plugin` | Which instruments and effects are installed (the staged AUv3 probes), their factory presets and parameters. |
| | `open_sound_lab`, `audition_sound`, `list_sounds` | What a preset sounds like, before it is in a song. See [Sounds](#sounds). |
| 2. Write | `save_song` | Store the document. It is checked first; see [What is checked](#what-is-checked). |
| 3. Load | `load_song` | Author the song's `.aumproj`, have the iPad pull it, load it in AUM, wait for its brain and tap, and check that every track answers. See [Loading](#loading). |
| 4. Play | `play_song`, `stop_song`, `get_song_playback` | The whole song, or from a section, or only some tracks. |
| 5. Listen | `get_audio_tap`, `get_audio_clip` | The master strip carries a ProbeAudioTap. |
| | `mix_song`, `get_song_mix` | The level of every bar, every section and every track. See [The mix](#the-mix). |
| 6. Change | `get_song` → edit → `save_song` | New notes, patterns or arrangement play at once. A change to what the song hosts or controls, to its tempo or to its session options needs step 3 again; `get_song` and `play_song` say when. |

`check_song` tells at any time whether the loaded session will sound.
`author_song_session` does only the first part of step 3: it builds the
session file and stages it.

`export_song_midi` writes the song as a Standard MIDI File for a sequencer or a
DAW.

`play_song` with `record: true` also records the song at the master's tap into
a file, and `upload_recording` puts that file into Google Drive: see
[Recording and Google Drive](recording.md).

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
  twelve). `plugin` names a staged AUv3 probe: by its id as
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

Per pattern: `gate` is how much of a note's grid length sounds (default 0.9; 1
is legato), `velocity` the velocity of a plain note (default 96), `transpose` a
shift in semitones, and `bars` the length when the grid is shorter than the
pattern should be. Without `bars` a grid must be whole bars. `swing` (on the
song or a pattern) delays every second step by that fraction of a step.

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
      - {track: pad, param: level, from: -30, to: 0}                 # a fade-in over the section
      - {track: pad, effect: reverb, param: Mix, from: 0.2, to: 0.6, bar: 3, bars: 2}
```

`param: level` is the track's fader; anything else is a parameter of the track's
instrument, or of `effect`. `bar` (1-based, default 1) and `bars` (default: to
the section's end) place the ramp.

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
- **A song's own session**, when its `session:` block says `presets: all`. The
  candidate is then heard through the track's own effect chain.

Measured in the lab, on a clean path with the fader at unity, the result is
stored in the **sound catalogue**: one YAML file per plugin under `sounds/` in
the config dir. `list_sounds` reads it. A measurement through an effect chain,
with parameters set on the instrument or with the fader moved is reported but
not stored: it is the chain's sound, not the preset's. What `set_sound`
corrects (a transposition, a drum map, a comment) is kept when the preset is
measured again.

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
  still applies on top.
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
- patterns: notes that are not notes, lines of unequal length, bars of the
  wrong length;
- plugins, against the staged AUv3 probes: an unknown plugin, preset or
  parameter (with the names that come close), a value outside a parameter's
  range;
- a control a scene changes without a declaration on the track;
- in a calibrated song: a drum lane its kit has no pad for, a note that leaves
  the MIDI range once it is shifted for its preset.

`dry_run: true` checks without storing.

## The session

`author_song_session` builds one strip per part:

```
Brain     ProbeMidiBrain (a MIDI strip)
<track>   instrument > effects > fader          one strip per track
Master    master sum > fader > master effects > ProbeAudioTap
```

**MIDI.** One brain plays every track. Its output is routed to every instrument
through a filter that passes only that track's channel, and to AUM's MIDI
Control through a filter that passes only the control channels. A note therefore
never fires a mapping, and a control message never reaches a synth.

| Channels | Carry |
|----------|-------|
| 1…N | the tracks' notes, one channel each |
| N+1…14 | plugin bypasses and parameters, filled from 14 downwards |
| 15 | strip levels and mutes and the transport (the convention's CCs), preset loads (Program Changes) |
| 16 | left to the rig: tap toggles and session switching |

**Mappings.** Every strip's level and mute and every plugin's bypass are mapped,
along with each parameter and each preset the song names. A preset becomes one
of the plugin's "Preset Load" actions in AUM, fired by a Program Change. With
`full_control: true` every other parameter of every plugin is mapped as well,
around the song's own mappings.

The daemon records which session a song was authored into and for which
layout. Changing notes, patterns or the arrangement leaves the layout alone.
Adding a plugin, a preset or a parameter changes it, and so does the tempo
(it is the session's clock): the session must then be authored and loaded
again, and until it is `get_song` and `play_song` say so.

The session is staged under `Songs/<title>` by default (`out_id` chooses
another place), which is also where it lands in AUM's folder on the iPad.

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
| author | The session is built and staged. Skipped when the staged file is already this song's session. |
| pull | The iPad's auv3-probe app fetches the file into AUM's folder. The app does that while it is in front; when the file is not fetched within a few seconds the daemon brings the app forward and starts its sync. Skipped when the iPad already has this very file. |
| load | AUM is told to open the session (over WebDriverAgent: plugin windows and panels are closed first), and the call waits until AUM has finished restoring the plugins. |
| connect | The new session's ProbeMidiBrain and ProbeAudioTap dial the daemon. |
| check | `check_song`, below. |

Loading replaces the session open in AUM, so `load_song` needs
`confirm: true`. Without the iPad UI tools (`ipad_wda_url` empty) it stops
after the pull and names the session to load by hand.

Bringing the probe app forward needs its bundle id, which carries a signing
prefix. A daemon on the host the iPad is plugged into looks it up. Any other
daemon remembers it from the first `ipad_launch` with the full id.

`check_song` answers whether the song will sound if it is played now:

- the brain is connected and the tap is streaming;
- AUM shows the song's session (the name in its menu), a strip for the brain,
  every track and the master, and no line that reports a failed plugin;
- every track answers a note. The daemon sets each track's own sound (its
  declaration, no scene), plays the track's first note of the song alone and
  measures the tap. A track that stays silent is named with its plugin.

It does not run while a song plays. `ui: false` leaves AUM's screen alone
(reading the session name opens and closes AUM's menu).

The daemon knows which session AUM has open only from what it loaded or saw
there itself, and only until a new brain connects, which is what a session
load in AUM looks like from outside. `play_song` warns when that session is
not the song's. It says nothing when it does not know.

## Playing

`play_song` first sets the session to the opening section's whole sound —
presets, then a pause for them to load, then everything else — and then sends
the timeline. It returns at once; the song plays in the background.

- `from` starts at a section (its id, or a 1-based position in the
  arrangement), `sections` limits how many are played.
- `tracks` plays only some tracks' notes, to listen to a part alone.
- `transport: true` starts AUM's transport with the first note and stops it at
  the end, for plugins that follow the host clock.

The daemon keeps the time, not AUM. The session's tempo is the song's, so
tempo-synced effects fit, but the notes arrive over the LAN and are emitted on
the brain's next render cycle: accurate to a few milliseconds on a quiet
network, not sample-accurate. That is right for writing a song and listening to
it. Playing a song on stage belongs to the brain itself, locked to AUM's
transport, which is not built yet.

Because the daemon keeps the time, its host has to stay awake while a song
plays. A laptop left alone dozes: the system keeps trying to sleep, and each
attempt stalls the daemon for a second or more, the notes with it. On macOS
the daemon holds its host awake itself: `play_song` starts `caffeinate -i -s`
before it sets the opening sound and ends it when the song has finished, was
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

With `per_track: true` it then plays every distinct section once per track
that has notes in it — the section's first bars, that track alone — and adds
the table a balance is made from: the level of every track in every section.
A track more than 12 dB over another in the same section is pointed out, and
a track that is silent where it plays notes is named. The passes use
`play_song`'s own player, so each starts from the section's whole sound.

The measurement runs in the background: `mix_song` returns with an estimate,
`get_song_mix` gives the progress or the finished report (`wait_ms` waits for
it), and `stop_song` ends it. `from` and `sections` limit it to a part of the
song, `whole: false` to the solo passes.

The levels are what the master tap carries: after the master's effects, a
limiter included. A peak is the highest sample, not an inter-sample estimate.

## Status

The test suite covers the notation, the compiled timeline, the player, and the
authored session read back from its encoded file: strips and chains, the
matrix routes with their port name and filters, every mapping, the preset-load
actions.

What a session file says and what AUM does with it are two things. Measured on
a device, in sessions authored from a song:

- **Routing.** One brain plays every track. A note on a track's channel
  sounded only that track's instrument (its pitch read back at the tap), and a
  message on a control channel made no sound.
- **Preset loads written from scratch.** A Program Change on channel 15 loaded
  the mapped preset on each of four plugins; timbre and level changed at the
  tap every time.
- **Transport.** The mapped CCs on channel 15 started and stopped AUM's
  transport. Stopping does not rewind, so `play_song` rewinds before it
  starts. While the transport ran, AUM's DSP load rose from 40 % to 85 % in a
  five-track session, which is why `transport` is off unless asked for.
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
  0.
- **Parameters.** A parameter's CC runs linearly over the parameter's range:
  0, 51, 64 and 127 put a reverb's mix at 0 %, 40 %, 50 % and 100 %.
- **A preset load resets parameters.** Loading a reverb preset put its mix
  back to the preset's own value, and the song's value sent after it held.
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

Not yet measured:

- `load_song`, `check_song`, `open_sound_lab`, `audition_sound` and
  `mix_song` themselves, a session with `presets: all` and a song with
  `calibrate`: the test suite runs them against a stand-in for the iPad;
- `transport: true` through a whole song;
- a ramp on a plugin parameter (the song played ramps on levels only);
- sessions authored with `track_taps` or `full_control`.

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
