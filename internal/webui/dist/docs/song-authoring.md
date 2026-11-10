# Writing a song: design

[songs.md](songs.md) describes the song document, its session and how it is
played. This note is about the work around it: what writing the first song on a
device cost, and what the daemon does so the next one costs less. It keeps the
rule of the song feature: nothing AUM or a plugin can do is taken away. Every
addition is optional, and a song written the old way compiles to the same
session and the same MIDI.

## What the first song cost

A five-track song was written, played and balanced on a device. The document
itself was the small part. The rest went into scripts written on the day:

| Cost | Why |
|------|-----|
| A preset could not be heard before it was in the song. | A session maps a Program Change only for the presets its song names. Twelve candidates meant twelve throwaway scenes, a new session and a reload. |
| A preset's name says little about its sound. | Presets of one synth sounded at pitch, one or two octaves below it, or with a second note layered in. Levels differed by 30 dB. One effect preset bent a held note by more than a semitone. Each was found by ear and by hand-written analysis, then corrected with `transpose` per track and per pattern and with volume parameters. |
| A drum machine's pads had to be found. | They sit on the plugin's own notes, and the kit decides what each pad is. Every note was hit and the hits analysed. |
| Getting a session into AUM took five steps. | Save, author, bring the probe app to the front so it pulls the file, load it in AUM, wait for the brain and the tap. Every new preset or parameter needed all five again. |
| Balancing needed a recorder. | `get_audio_tap` averages over the tap's whole window and its peak is the tap's 10 Hz feature, which reads under the sample peak. Levels per track, per section and per bar came from clips fetched and analysed outside the daemon. |
| Faults were silent. | A synth that swallows notes while it loads a preset, a plugin that failed to load, a session other than the song's in AUM: none of them is an error anywhere. `play_song` warned about the wrong session from what the iPad last downloaded, which is not what AUM has open. |
| Names were long and values misleading. | Plugin ids are whole component ids. A probe's parameter values are the default patch's, not what a preset sets. |

## The pieces

### 1. One call from a saved song to a session that plays: `load_song`

`load_song` does the five steps and reports each one:

1. author the session (skipped when the staged file is already the song's);
2. have the iPad pull it: bring the probe app to the front, start its sync, and
   wait for the download the receiver sees (skipped when the iPad already
   fetched this very file);
3. load it in AUM over WebDriverAgent, and wait until AUM has finished loading;
4. wait for the brain and the tap of the new session to dial in;
5. run the check below.

A step that fails ends the call with what was seen and what to do. Loading
replaces the session open in AUM, so the call needs `confirm: true`, like every
AUM operation that loses state.

The daemon then knows which session AUM has open because it loaded it and saw
AUM show its name. It forgets that again when the brain reconnects without a
`load_song` (somebody loaded something else). `play_song` warns from this
knowledge and no longer from the last download. Where the session's tap
names itself (8, below), the daemon asks the tap instead.

### 2. A check that listens: `check_song`

`check_song` answers "will this song sound if I play it now": the brain is
connected, the tap is streaming, AUM shows the song's session and no alert, the
mixer has the song's strips, no node is drawn as failed, and **every track
answers a note**. For the last one the daemon sets each track's own sound,
mutes every strip, and then plays each track's own first note with only that
strip open, measuring the tap. A track that stays silent is named, with its
plugin: that is what a failed or crashed plugin and a missing route look like
from outside.

The first run on a device added two things to this. AUM tells that a plugin
did not load only through the colour of its node, so the check reads the
nodes' rings off a screenshot. And a track must be checked behind the others'
mutes: a delay that was still ringing made a quiet hat look like no answer.

The second run added a third. AUM can keep the first of two opposite mutes
that reach one strip together, so muting a strip and opening it in the same
moment left it muted, and the check heard nothing. A strip's mute and its
unmute are sent apart, and a track that stays silent is played once more
before it is called silent.

`load_song` ends with this check. It can be called on its own at any time, for
instance when a playback sounded wrong. Where the session's tap names itself
(8, below), the check learns the open session from it and leaves AUM's menu
shut; it still reads the strips and the nodes' rings off the mixer.

### 3. A sound catalogue the daemon fills by measuring

The catalogue is one file per plugin in the config dir (`sounds/<plugin>.yaml`),
part of the rig like scenes and songs. An entry is a preset as measured at the
tap on a clean path (no effects, fader at unity):

- **pitch**: what a written note sounds as, as the semitones to add so it
  sounds as written (an instrument an octave low has `transpose: 12`); the
  deviation in cents; how far a held note wanders;
- **level**: rms and sample peak of a reference note;
- **envelope**: attack, whether a held note sustains or dies away, release;
- **layers**: strong partials that are not the note (a fifth, a detuned copy);
- for a kit, the **pads**: every note that makes a sound, with level, length,
  brightness and a guess at what it is (kick, snare, hat, tom, cymbal).

Entries are measurements, so they carry the note, the velocity and the date,
and can be measured again. A guess (a pad's lane) can be corrected by hand; a
corrected value is kept when the preset is measured again.

### 4. A lab to audition in: `open_sound_lab`, `audition_sound`

A session can map every factory preset of its plugins, not only the ones a song
names: the session option `presets: all` spreads the extra Preset Loads over
the Program Change space of the control channels. The song's own presets keep
their addresses, so the option changes nothing for a song that does not use it.

`open_sound_lab` writes a song that is only instruments, with that option, and
loads it. `audition_sound` then plays a phrase on any preset of any plugin in
the session: it loads the preset, waits for the plugin, plays, measures the
tap and returns the entry above. Several presets in one call (by name or by a
word in the name) are compared in one table. Measured on a clean path, the
entry goes into the catalogue.

A song's own session can be authored with `presets: all` too. `audition_sound`
then plays a candidate through the track's real effect chain. That measurement
is reported but not stored: it is the chain's, not the preset's.

Twelve candidates are twelve seconds in one loaded session.

### 5. A song written in sounding pitch and loudness: `calibrate`

With `calibrate: true` a song is compiled against the catalogue:

- **Notes are sounding pitch.** Each track's notes are shifted by the
  `transpose` of the preset the track has *in that section*, so a scene that
  changes the preset changes the shift with it. No `transpose` on tracks and
  patterns to undo a preset's octave.
- **Levels are loudness.** A track's `level` is added to what it takes to bring
  the preset's reference note to a common level, again per section. Where the
  fader cannot give that much, the song says so and names the plugin's volume
  as the place to turn.
- **Drum lanes come from the kit.** `kick`, `snare`, `hat` … resolve to the
  measured pads. A lane the kit does not have is reported instead of playing a
  silent note. An explicit `drum_map` still wins.

A preset that is not in the catalogue plays as written, and `save_song` lists
it as not measured. Without `calibrate` nothing of this applies.

### 6. A mix report: `mix_song`

`mix_song` plays the song and records the master tap for its whole length (the
tap's window is read out as it fills, so the length is not limited by it). The
report is rms and sample peak per bar and per section, the loudest bar, clipped
bars, and where the stream stalled. With `per_track: true` it also gives the
level of every track in every section: the table a balance is made from. It
plays each section once per track for that, or, in a session with a tap on
every track (8, below), reads all of them out in the one pass. The call
returns at once and `get_song_mix` gives the progress and the result.

### 7. A shorter document

- `plugin:` takes the probe id, or any name that identifies one plugin: its
  display name or a part of it. An ambiguous name is an error that lists the
  candidates.
- `session:` holds the options that were arguments of `author_song_session`
  (`track_taps`, `full_control`, `hardware`, and the new `presets`), so a song
  carries how its session is built and `load_song` needs no arguments. The
  tool's arguments still override.
- `get_song_plugin` shows what the catalogue knows next to each preset, and
  labels a parameter's value as the default patch's.

### 8. Taps that say who they are

A session with `track_taps` streamed every track, but the plugin sent no name:
each stream was known by the address it came from, the daemon could not tell
the tracks apart, and "the tap" was whichever connected last, which in such a
session is one track's as often as the master's. `mix_song` therefore measured
the tracks one at a time at the master, and the open session was known only
from AUM's screen.

Now the plugin's saved state carries a name and the plugin dials the daemon
under it. The daemon writes the name into every tap of a session it authors:
the session's id and the strip, `<session id>/master` and
`<session id>/<track id>`. From that:

- the song tools listen at the session's master tap, by name;
- the connected master tap says which session AUM has open. `check_song`
  asks it, and opens AUM's menu for the session's name only when no tap
  answers. The mixer is read either way, for the strips and for a node drawn
  as failed;
- `mix_song` reads every track's tap out during the pass through the song,
  so the table of tracks and sections costs no pass of its own;
- a tap that dials again under its name (a reconnect, the same session loaded
  anew) takes its stream over. The connection before it may end later
  without the tap counting as gone.

Nothing is taken away. A tap without a name works as before: an older build
of the plugin ignores the name in a new session, and the new build restores an
older session to a tap without one. The solo passes are still there, as the
fallback and on request (`solo: true`), because they measure a track after
the master's effects and the track's tap does not.

## What stays as it is

- How a song is played (the daemon keeps the time today) and recording to a
  file are separate work; these tools only use `play_song`'s player and the
  tap.
- A tap's name is the session's id at the time it was authored. A copy saved
  under another name in AUM keeps it, and the daemon then takes the copy for
  the session it was made from.
- What a preset sets a parameter to is not readable from outside the plugin.
  The catalogue records what a preset sounds like, not its parameter values.

## Order of work

1. `load_song`, `check_song`, the loaded-session knowledge, `session:` in the
   document.
2. The catalogue, `presets: all`, the lab and `audition_sound`.
3. `calibrate`, plugin names, the catalogue in `get_song_plugin`.
4. `mix_song`.
5. Taps with names, in the plugin and in the sessions the daemon authors.

Each step is usable without the next.
