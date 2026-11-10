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
| 2. Write | `save_song` | Store the document. It is checked first; see [What is checked](#what-is-checked). |
| 3. Author | `author_song_session` | Build the song's `.aumproj` and stage it. The iPad's auv3-probe app pulls it into AUM's folder. |
| 4. Load | in AUM | Open the session. Its brain and tap dial the daemon. |
| 5. Play | `play_song`, `stop_song`, `get_song_playback` | The whole song from the session's sequencer, or live: from a section, or only some tracks. |
| 6. Listen | `get_audio_tap`, `get_audio_clip` | The master strip carries a ProbeAudioTap. |
| 7. Change | `get_song` → edit → `save_song` | New notes, patterns or arrangement play at once, live. The sequencer plays them after step 3 and 4. A change to what the song hosts or controls, or to its tempo, needs step 3 and 4 before the song sounds right at all; `get_song` and `play_song` say when. |

`export_song_midi` writes the song as a Standard MIDI File for a sequencer or a
DAW.

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
  twelve). `plugin` is the id of a staged AUv3 probe as `list_song_plugins`
  gives it: the unit's type, subtype and manufacturer, such as
  `aumu-mono-_acme` (the example uses short ids to stay readable).
  `kind: drums` makes it a drum track. `transpose` shifts all its notes, for
  a preset that does not sound at the written pitch.
- **master** — effects on the master strip, after its fader.
- **patterns** — the phrases; see [Notation](#notation).
- **scenes** — named sound settings; see [Sound](#sound).
- **sections** — for a number of `bars`, every track listed under `play` plays
  its pattern, or its list of patterns one after another. Patterns repeat until
  the section ends and are cut there; a track left out is silent.
- **arrangement** — the sections in playing order. A section may appear more
  than once.

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

## What is checked

`save_song` refuses a document that does not hold together, and reports every
problem with its place in the document:

- references: tracks, patterns, scenes, sections and effects that do not exist;
- patterns: notes that are not notes, lines of unequal length, bars of the
  wrong length;
- plugins, against the staged AUv3 probes: an unknown plugin, preset or
  parameter (with the names that come close), a value outside a parameter's
  range;
- a control a scene changes without a declaration on the track.

`dry_run: true` checks without storing.

## The session

`author_song_session` builds one strip per part:

```
Brain     ProbeMidiBrain, the sequencer (a MIDI strip)
<track>   instrument > effects > fader          one strip per track
Master    master sum > fader > master effects > ProbeAudioTap
```

**MIDI.** One brain plays every track. Its output is routed to every instrument
through a filter that passes only that track's channel, and to AUM's MIDI
Control through a filter that passes only the control channels. A note therefore
never fires a mapping, and a control message never reaches a synth. The
sequencer sits next to the brain and is routed the same way, to the same
destinations through the same filters.

| Channels | Carry |
|----------|-------|
| 1…N | the tracks' notes, one channel each |
| N+1…14 | plugin bypasses and parameters, filled from 14 downwards |
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
transport keeps it silent. `sequencer: false` leaves it out; `sequencer: true`
insists on it. [The sequencer's state](#the-sequencers-state) says what is
written.

The daemon records which session a song was authored into, for which layout,
and which notes its sequencer holds. Changing notes, patterns or the
arrangement leaves the layout alone, but not the sequencer: it keeps playing
the song as it was authored. Adding a plugin, a preset or a parameter changes
the layout, and so does the tempo (it is the session's clock): the session
must then be authored and loaded again, and until it is `get_song` and
`play_song` say so.

The session is staged under `Songs/<title>` by default (`out_id` chooses
another place), which is also where it lands in AUM's folder on the iPad.

## Playing

`play_song` returns at once; the song plays in the background. There are two
ways to play, and a session with a sequencer offers both:

| | Sequenced | Live |
|-|-----------|------|
| Who plays | the sequencer in the session, with AUM's transport | the daemon, through the brain |
| What the daemon sends | rewind and start; stop and rewind at the end | the opening sound, then every note and control change when it is due |
| Timing | AUM's own: on the sample | the network's: a few milliseconds either way |
| What is played | the whole song, as it was when the session was authored | the song as it is stored now: all of it, from a section, a number of sections, only some tracks |
| After a change to the notes | `save_song`, `author_song_session`, the iPad pulls the session, AUM loads it (every plugin is instantiated again) | `save_song` |
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
`play_song` un-bypasses the sequencer, rewinds and starts AUM's transport, and
anything else that runs the transport from the top plays the song too. Half a
second after the last bar the daemon stops the transport and rewinds it;
`stop_song` does that at once.

`get_song_playback` reports a sequenced song's position from the daemon's own
clock, started when it sent the start; it does not read AUM's transport. In
the lead-in it reports the time to the first bar. AUM begins a few tens of
milliseconds after the message (see [Status](#status)), so the report runs
ahead by that much.

**Live.** `play_song` first sets the session to the opening section's whole
sound — presets, then a pause for them to load, then everything else — and
then sends the timeline.

- `from` starts at a section (its id, or a 1-based position in the
  arrangement), `sections` limits how many are played.
- `tracks` plays only some tracks' notes, to listen to a part alone.
- `transport: true` starts AUM's transport with the first note and stops it at
  the end, for plugins that follow the host clock. In a session with a
  sequencer, the sequencer is bypassed for the length of the playback.

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

The takes ran against a daemon without this mode: the session was authored
with the code as it is here and staged through the daemon's receiver, and the
rewind, start and stop were sent with `send_midi`, the messages `play_song`
sends. `play_song` in sequenced mode is covered by the tests and has not run
against a device.

Not yet measured:

- `play_song` and `stop_song` in sequenced mode on a device, and
  `get_song_playback` against AUM's own position;
- a sequenced song on a host that dozes;
- an edit made in the sequencer's window, and a session saved from AUM with
  the sequencer in it;
- a meter other than 4/4 and a song much longer than two minutes in the
  sequencer;
- `transport: true` through a whole live song with the daemon's own bypass of
  the sequencer (the take above sent the bypass by hand);
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
| Authored sessions | the AUM staging dir (state dir), like every authored session |
| Which session a song was authored into | `song-sessions.json` in the state dir |
| MIDI exports | `song-exports/` in the state dir |

A song names the plugins of one rig. Keep the songs directory out of a public
repository if that is private.
