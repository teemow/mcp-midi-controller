# Craft tools: design

The song tools ([songs.md](songs.md), [song-authoring.md](song-authoring.md))
teach an agent the song document: how a pattern is written, how a session is
built, how a song is loaded, played and measured. They do not teach the work
of the people who make a song: the producer, the composer, the drummer, the
sound designer, the conductor, the mixing and the mastering engineer. That
shows in the songs. The first eight written with the tools were one song eight
times ([A review of the song as music](song-authoring.md#9-a-review-of-the-song-as-music)),
and balancing a song's levels took up most of the session that wrote it.

This note designs a craft layer on top of the song tools. Each craft works as
an agent of its own, brings its techniques as data, and has tools that change
and check what the agent wrote. The rule of the song feature holds: nothing
AUM or a plugin can do is taken away, every addition is optional, and a song
written by hand compiles to the same session and the same MIDI as before.

The words used here are defined in [GLOSSARY.md](../GLOSSARY.md). The
decisions that shaped the design are recorded as ADRs in [adr/](adr/). The
work is tracked in the epic
https://github.com/teemow/mcp-midi-controller/issues/225.

## The crafts

| Craft | Craft agent | Knows | Owns in the song | Checks |
|-------|-------------|-------|------------------|--------|
| `production` | `producer` | form, energy and contrast, the sound palette, references, when a song is done | the brief, the sections and their energy, the track roles, the arrangement, the palette | length, contrast between sections, where the song peaks, sounds the other songs already use, the song against its brief |
| `composition` | `composer` | harmony, voice leading, melody, motif and its development, bass lines | the pitched patterns | chord tones on strong beats, voice leading, notes of two tracks that clash, repeats without development |
| `drums` | `drummer` | grooves by style, feel (swing, ghost notes, a lane ahead of or behind the beat), fills, variation, the kit | drum patterns, play lists, a drum machine's own variation | a fill before a section that rises, a crash on its first beat, kick and bass together, the spread of velocities |
| `sound_design` | `sound_designer` | synthesis by archetype, each synth's controls and quirks, the measured presets, movement | an instrument's preset and parameters, modulators, automation of a sound | measured pitch, envelope and brightness against the archetype |
| `orchestration` | `conductor` | ranges, orchestration, balance between parts, dynamics and phrasing, tempo, entries | which track plays which voice, velocities, gate, level arcs, the tempo's shape | the register each track takes, ranges, flat dynamics, parts that never rest |
| `mixing` | `mixing_engineer` | gain staging, EQ and masking, dynamics, space, width, automation | track levels, effect chains, pan, sends, rides | levels and bands per track, tracks that mask each other, mud and harshness, wet and dry, width |
| `mastering` | `mastering_engineer` | loudness targets, tonal balance, limiting, translation | the master's chain | integrated loudness, true peak, peak to loudness, the tonal curve against a target, a mono low end |

A craft has one id, used by technique cards, by the answers of the craft tools
and by the lenses of `review_song`. Its craft agent is named after the person
who practises it.

## The layers

| Layer | Holds | Form |
|-------|-------|------|
| Craft agents | How a craft judges and works: what it listens for, its order of work, when it hands over | Claude Code subagents, one per craft, generated from `docs/crafts/<craft>.md`, and the `produce_song` skill that runs them in order |
| Craft tools | Transforms that change and check what the agent wrote, and measurements | Go: music theory (voicing, voice leading, harmonic analysis, grooves) and measurement (loudness, bands, stereo) |
| Capabilities | The functions plugins provide, such as `eq.highpass`, with their parameters in units and ranges | Go: a closed list, and how `profile_plugin` checks each one |
| Technique cards | Recipes: "high-pass every track that is not bass", "a fill into a section that rises", "a reese bass" | YAML. A card names the capabilities it needs, never a plugin, and how its result is checked |
| Plugin cards | What an installed plugin can do and how it is set: capability to parameter, write path, mapping range | YAML, a section of the plugin's knowledge entry (https://github.com/teemow/mcp-midi-controller/issues/167). General cards ship with the daemon; the rig's own and its order of preference live in the config dir |
| Target cards | What a mix or a master aims for: loudness and true peak, a tonal curve, each track role's level against an anchor | YAML, by name (`streaming`, `club`, `dynamic`, a genre) |

A technique says what it needs (`eq.highpass`); a plugin card says how one
plugin provides it; the rig's order of preference picks the plugin. That is
how a technique works with the plugins a rig has, and how a new plugin becomes
usable by every technique once it has a card.

Below the layers stays what is there: the song document, the sound catalogue,
the taps, `audition_sound`, `mix_song`, the player and the recorder.

## Rules

1. **The agent writes, the tools transform.** A craft tool changes or checks
   material the agent wrote: it develops a pattern, voices a progression,
   locks a bass line to the kick, adds fills. None invents chords, melodies,
   bass lines, grooves or a song's plan
   ([ADR 0001](adr/0001-craft-tools-transform-not-generate.md)).
2. **Patches, not rewrites.** A craft tool returns a patch to the song, the
   techniques it used and why. With `apply: true` it stores the patched song
   through `save_song`, so the document is checked and reviewed as always. No
   craft tool writes a song another way.
3. **Measured.** A technique says how its result is checked. With
   `verify: true` the tool plays what it changed and measures before and
   after; a result that misses is reported, not hidden.
4. **Capabilities, not plugin names.** Techniques stay true when the rig
   changes. A technique the rig has no plugin for says which capability is
   missing.
5. **Variety by design.** A card with variants takes a `seed`; sound choices
   rank what the rig's other songs have not used. A tool that proposes the
   same thing for every song is a defect.
6. **Knowledge as data.** What is learnt about a plugin or a technique goes
   into a card, where the next session finds it, and not into a transcript.

## The song document

Four optional fields:

```yaml
brief:                         # the producer's, read by every craft
  intent: "a night drive: restless verses, a chorus that opens up"
  genre: synthwave             # picks a target card where one exists
  length: "3:45"
  loudness: streaming          # the mastering target
  reference: Night Drive 2026-11-12 1903   # a recording to compare with

tracks:
  - id: bass
    role: bass                 # bass drums percussion chords pad lead counter texture fx
    instrument: {plugin: mono, preset: Deep Bass}
    effects:
      - id: hp
        plugin: Pro-Q 3
        params: {"Band 1 Frequency": 80 Hz}
        range: {"Band 1 Frequency": [20 Hz, 500 Hz]}   # the mapping range

sections:
  verse1:   {bars: 16, energy: 4, play: {…}}
  prechorus: {bars: 8, energy: [5, 8], play: {…}}    # rises across the section
  chorus1:  {bars: 8,  energy: 8, play: {…}}
```

- `brief` says what the song is for. The producer writes it, and every craft
  holds the song against it.
- `energy` (1 to 10) on a section is the one number all crafts read: the
  drummer's density and fill intensity, the conductor's dynamics, the mixing
  engineer's rides, the producer's check of contrast and peak. A pair is the
  start and the end of the section, with a straight line between.
- `role` says a track's part in the music. A `kind: drums` track is `drums`
  unless it says otherwise. A technique that needs a role it cannot find says
  so; it does not guess.
- `range` narrows the AUM mapping of a parameter: see
  [How a value reaches a plugin](#how-a-value-reaches-a-plugin).

The compiler ignores `brief`, `energy` and `role`: a song plays the same with
or without them. `range` changes only the session's mapping, and a song
without it compiles as before.

## Capabilities

A capability is defined in Go: its name (`eq.highpass`), its parameters with
unit and range (`freq` in Hz, `slope` in dB per octave), and what
`profile_plugin` measures to check a plugin that claims it (a high-pass
lowers the low band). Technique cards and plugin cards are checked against the
list when they load; an unknown capability or parameter is an error, like an
unknown key in a song. A new capability is a change to the code; a new plugin
or technique stays data.

## Technique cards

A card is one technique of one craft:

```yaml
id: mixing.highpass_non_bass
craft: mixing
title: High-pass every track that is not bass
intent: Leave the low end to the kick and the bass.
when: A track whose role is not bass has energy below its lowest note.
when_not:
  - A sub pad or an 808 that is meant to fill the low end.
  - Low strings in an orchestral piece.
targets: track                       # track | section | master | song
needs: [eq.highpass]
set:
  eq.highpass:
    freq: "clamp(0.7 * lowest_note_hz, 40, 250)"
    slope: 12
precision: {freq: "10%"}             # what a binding must reach
place: first                         # in the track's chain
verify:
  measure: track_bands
  band: [20, 80]
  expect: "falls by 6 dB or more"
```

There are two kinds:

- **Settings** insert or set capabilities in a chain, as above. They are data
  only.
- **Writing** techniques name the transform that does the work and the options
  they give it: `transform: add_fills` with `{phrase_bars: 8, intensity: energy}`.
  A groove by style is a writing card with variants, and the `seed` picks one.

An **archetype** of sound design (sub bass, reese, acid, pluck, supersaw, pad,
brass, bell) is a card of the `sound_design` craft that says what such a sound
measures as: its pitch, envelope and brightness. `audition_sound` takes a
technique and compares what it measured with the card.

A card's values are a small arithmetic: numbers, `+ - * /`, parentheses,
`min`, `max`, `clamp`, and what the song tells: `tempo`, `beat_ms`, `bar_ms`,
`lowest_note_hz`, `section.energy`. A delay time is then written as
`beat_ms * 0.75` and stays in time when the tempo changes. Values are parsed
and checked when the card loads.

The cards ship with the daemon (`internal/craft/techniques/`). The rig may add
its own in the config dir (`craft/techniques/`), where a card with the same id
replaces the shipped one.

## Plugin cards

A plugin card is a section of the plugin's knowledge entry
(https://github.com/teemow/mcp-midi-controller/issues/167). It says which
capabilities the plugin provides and how each is set:

```yaml
plugin: aumf/FQ3p/FabF               # matched by the component tuple
name: Pro-Q 3
capabilities:
  eq.highpass:
    set:                             # parameter names as the probe dump has them
      "Band 1 Used": on
      "Band 1 Shape": Low Cut
      "Band 1 Frequency": "{freq}"
      "Band 1 Slope": "{slope} dB/oct"
    write: cc
    range: {"Band 1 Frequency": [20 Hz, 500 Hz]}
    resolution: {"Band 1 Frequency": measured}   # by profile_plugin
```

The example is illustrative: the parameter names and the resolution are what
`profile_plugin` reads and measures on a rig. What else is known about the
plugin (a synth that needs its gain staged first, a glide that only sounds
legato, a proprietary saved state) is the rest of its knowledge entry, which
`get_song_plugin` and the craft agents read.

A **binding** is one capability of a technique matched to one plugin on the
rig, with its write path and the precision that path reaches.
`apply_technique` takes, per capability, the first plugin in the rig's order
of preference whose binding reaches the technique's precision, and otherwise
says which plugin came closest and how far off it is. `get_technique` shows
the bindings a technique would get.

### How a value reaches a plugin

A song's parameter values travel as a 7-bit Control Change. Spread over the
parameter's whole range (`paramWire` in `internal/song/plan.go`) that is enough
for movement, but not for a setting: a frequency exposed linearly from 10 Hz
to 30 kHz has 128 steps about 235 Hz apart, and an 80 Hz high-pass cannot be
set. AUM's MIDI mapping has an input range, so a binding fixes a **mapping
range** per parameter, and the session maps the 128 steps over it: 20 Hz to
500 Hz in steps of about 4 Hz. The range is fixed per binding and not taken
from the song's values, so changing a value still plays live without
reloading the session ([ADR 0003](adr/0003-exact-settings-from-mapping-ranges.md)).

| Path | How | Precision |
|------|-----|-----------|
| `cc` | The song's `params`, mapped over the binding's range | the 128 steps of the range, measured by `profile_plugin` |
| `state` | A field of the plugin's saved state, where it is text (JSON, JUCE XML): capture-and-mutate, see [auv3-state-authoring.md](auv3-state-authoring.md). Only for settings no parameter exposes | exact |
| `preset` | A factory preset, or a state captured from a node set by hand and named | exact, for the settings the preset holds |

### Where cards come from

- `profile_plugin` reads a plugin's probe dump, matches its parameters to
  capabilities by name, unit and range, and then measures: it sweeps a
  parameter at the tap and checks that the expected thing moves (a cutoff
  moves the spectral centroid, a high-pass the low band, a threshold the crest
  factor). It also measures each parameter's step size. The result is a card
  in the config dir that says what was measured and when.
- General cards for widely used plugins ship with the daemon. The rig's order
  of preference per capability is its own (`craft/prefer.yaml` in the config
  dir).

## Target cards

A target is what a mix or a master aims for:

```yaml
id: synthwave
loudness: {integrated: -14, true_peak: -1}     # LUFS, dBTP
tonal: [[30, -2], [120, 0], [1000, -3], [8000, -9], [16000, -14]]   # Hz, dB
levels:                                         # each role against the anchor
  anchor: drums
  bass: -2
  pad: -9
  lead: -4
```

The brief's `loudness` and `genre` pick the targets: `master_song` meets the
loudness, `balance_mix` the levels, and both read the tonal curve. The first
targets are written by hand and checked with `measure_master` against
reference recordings.

## Patches

All craft tools take the same arguments where they apply: `song`, `section` or
`sections`, `track`, `seed`, `apply` (default false), `verify`. They return the
same shape:

```yaml
craft: drums
techniques: [drums.fill_into_section]
revision: 6f1c…                       # the song the patch was made against
patch:
  patterns:
    fill_into_chorus: {drums: {kick: "x.......x.......", snare: "....x.x.xxxxXXXX"}}
    crash_beat:       {drums: {kick: "x...x...x...x...", snare: "....x.......x...", crash: "x..............."}}
  sections:
    verse2:  {play: {drums: [beat, beat, beat, beat, beat, beat, beat, fill_into_chorus]}}
    chorus2: {play: {drums: [crash_beat, beat, beat, beat, beat, beat, beat, beat]}}
why:
  - "verse2 (energy 4) leads into chorus2 (energy 8): a one-bar snare build, a crash on its first beat"
checks:
  - "review_song, drums: a fill before 3 of 3 sections that rise"
applied: false
```

A patch is a merge patch in which tracks, effects and every other entry with
an id are keyed by that id, as scenes already address them. A new effect
carries its place, and `null` removes an entry:

```yaml
patch:
  tracks:
    pad:
      effects:
        hp: {plugin: Pro-Q 3, place: first, params: {…}, range: {…}}
        old_eq: null
```

Lists of values (play lists, `arrangement`) are replaced whole. `get_song` and
`save_song` return the song's revision; `patch_song` refuses a patch whose
revision is no longer current and says to run the tool again. With
`apply: true` a tool reads, patches and stores under one lock
([ADR 0002](adr/0002-patch-by-id-with-revision.md)).

A tool that plays (a check with `verify`, `balance_mix`, `master_song`) runs
in the background like `mix_song`, counts as busy, and reports through
`get_craft_job`.

## The tools

### Shared

| Tool | Does |
|------|------|
| `list_techniques` | The cards, by craft, target or capability, each with the plugins of the rig that can carry it |
| `get_technique` | One card in full, with the bindings it would get on this rig |
| `apply_technique` | Applies a card to a track, a section, the master or the song |
| `patch_song` | Applies a patch a tool returned without `apply` |
| `review_song` | Reads a song through the lenses `production`, `composition`, `drums` and `orchestration` without playing it. Every finding names the techniques that answer it. `save_song`'s review is the `production` lens |
| `get_craft_job` | The progress or the report of a craft tool that plays |

### Production

The producer writes the brief, the sections with their bars and energy, the
track roles, and where each part enters and leaves. Its check is the
`production` lens of `review_song`; its palette comes from `find_sound`.
`compare_to_reference` (mastering) serves it too.

### Composition

| Tool | Takes | Gives |
|------|-------|-------|
| `voice_progression` | chords the agent wrote, the track that plays them | The chords voiced in that track's range, with voice leading |
| `lock_bass` | a bass pattern, a drum track | The bass line's onsets moved onto the kick, or where they miss |
| `develop_pattern` | a pattern, transforms (sequence, inversion, retrograde, augmentation, fragmentation, displacement, register, thinning, thickening) | Variants of it, so a section that returns does not return unchanged |

### Drums

| Tool | Takes | Gives |
|------|-------|-------|
| `add_fills` | phrase length, which sections, intensity (default: from energy) | Play lists that end a phrase in a fill, and a crash on the first beat of the next section |
| `vary_groove` | amount, moves (open hats, a displaced kick, ghost notes, ride, half time) | Variants across a phrase, so a four-bar loop differs in every bar; for a drum machine that varies itself, its variation parameters |

Grooves by style are writing cards, applied with `apply_technique`.

### Sound design

| Tool | Takes | Gives |
|------|-------|-------|
| `find_sound` | role, register, attack, sustain | Presets ranked from the catalogue by what is measured and by what other songs have not used, and a plan to audition those not measured yet |
| `add_movement` | the target (filter, wavetable, pitch, an effect's mix), shape, rate, depth, sections | A modulator or automation |
| `profile_plugin` | a plugin | Its plugin card, measured |

The sound designer sets an instrument's parameters itself and checks them with
`audition_sound` against an archetype card. The catalogue gains a brightness
per preset for that.

### Orchestration

| Tool | Takes | Gives |
|------|-------|-------|
| `orchestrate` | the material (chords, lines), the tracks, the texture (block, spread, doubled, counterpoint) | Voices spread over tracks by range, with doublings, the melody kept clear |
| `shape_dynamics` | an arc per section (or the energy), phrasing | Velocity arcs per phrase, crescendos and diminuendos as level ramps, articulation as gate |
| `shape_tempo` | a ritardando, a fermata, an accelerando | Needs a tempo map, below |

### Mixing

| Tool | Takes | Gives |
|------|-------|-------|
| `review_mix` | a `mix_song` report | Per track and section: balance against an anchor track, tracks that mask each other, mud, harshness, low end outside the bass, a mix all dry or all wet, everything in the middle. Every finding names its techniques. It does not play |
| `balance_mix` | an anchor track, a target, iterations | Levels set by measuring, adjusting and measuring again |

The rest of the craft is technique cards: carving out masking, bus glue,
parallel compression, a pump in time with the kick (a rhythmic gate such as
Perforator, or a modulator on a level), saturation for presence, reverb with a
pre-delay in time, delay throws at the end of a phrase, width, rides.

### Mastering

| Tool | Takes | Gives |
|------|-------|-------|
| `measure_master` | a recording | Its loudness, true peak, tonal balance and stereo, measured from the file. A song's are in its `mix_song` report |
| `master_song` | a target (streaming, club, dynamic, or LUFS and true peak), character (transparent, warm, punchy) | The master's chain (EQ, glue, saturation where asked for, a limiter), its gain raised until the target is met. Where peak to loudness falls too low first, it stops and hands the song back to mixing with why |
| `compare_to_reference` | a song or a recording, a reference recording | Loudness, tonal curve, width and dynamics against the reference, and the EQ moves that close the gap |

## Craft agents and the order of work

Each craft runs as a craft agent: a subagent whose whole context is the craft,
not the conversation around it
([ADR 0004](adr/0004-crafts-run-as-subagents.md)). The agents ship as a
Claude Code plugin from this repository. An agent's text lives in
`docs/crafts/<craft>.md`, so the web UI's docs tab shows people what it tells
the agent, and the build generates the agent's definition from it, with only
its craft's tools. The plugin's MCP config takes the daemon's URL from an
environment variable, so the repository names no host.

A craft agent:

- writes its craft's material and stores its own patches with `apply: true`,
  through `save_song`;
- loads, plays and measures only the song it works on. Started alone, it asks
  the person before it loads, like every AUM operation that loses state;
  started by `produce_song`, its task carries that consent;
- reports the revision before and after, what it changed and why, what it
  checked, the findings it left open, and what it hands to which craft.

`produce_song` is a skill in the same plugin. It asks the person once whether
it may load the song's session, then runs the craft agents one at a time,
with the producer looking at the song three times:

1. producer: the brief, the form, the energy, the roles;
2. composer: harmony, parts, motifs and their development;
3. drummer: grooves, fills, variation;
4. sound designer: sounds for every part, movement;
5. conductor: orchestration, dynamics, tempo;
6. producer: `review_song` against the brief;
7. mixing engineer: `mix_song`, `review_mix`, `balance_mix`, techniques;
8. producer: the song as a whole, once more;
9. mastering engineer: `master_song`, then the recording and its upload.

It passes each agent's hand-over on in the next agent's task and never writes
the song itself. Crafts never run side by side: the rig plays one thing at a
time, and two patches on one song collide on its revision. Any craft agent
can be called on its own: a drummer for a song written by hand, a mix for a
song that is otherwise done. A client other than Claude Code gets the craft
tools, not the craft agents.

## What the measurements need

The taps send raw PCM on every channel, and `mix_song` already plays the whole
song once. Its pass measures everything below; nothing else plays to measure.

| Measurement | For | From |
|-------------|-----|------|
| Loudness after ITU-R BS.1770: momentary, short-term, integrated, loudness range; true peak, oversampled | mastering, the mix's levels | the master tap during `mix_song`, and a recording's file in `measure_master` |
| Bands, crest factor and onsets per track and section | masking, mud and harshness, dynamics, kick and bass together | the track taps during `mix_song` (`per_track`) |
| Stereo correlation and the share of the side signal, in total and below about 150 Hz | width, a mono low end | the taps' channels during `mix_song` |
| Onsets against the grid | the feel of a groove | a recording |
| Brightness per preset | archetypes, `find_sound` | the sound catalogue's measurements |

## What the core needs

| Need | For | Where it lands |
|------|-----|----------------|
| Mapping ranges | mixing, mastering, sound design | `range` on a parameter; the session author narrows AUM's mapping to it |
| A revision per song, patches by id | every craft tool | `get_song`, `save_song`, `patch_song` |
| Pan per track | mixing, the conductor's seating | `pan` on a track and in scenes; a pan node class in [aum-session-go](https://github.com/teemow/aum-session-go), which has none yet |
| Send buses | shared reverb and delay | `sends` on a track and bus strips; AUM's bus send nodes. A bus strip counts against the strips a session holds |
| Saved state per track | settings no parameter exposes | a plugin's text state per track in a song (capture-and-mutate) |
| Timing offsets per drum lane | the drummer's feel | an offset in ms on a lane or a pattern, applied by the compiler |
| A tempo map | the conductor | a tempo per section and ramps, followed by both ways of [playing](songs.md#playing) |
| `brief`, `energy`, `role` | every craft | the song document, ignored by the compiler |

## The number of tools

The daemon serves 112 tools, and the crafts add about 22. Every connected
agent reads the whole list, so the craft tools form a set of their own that
the config switches on (`craft_tools: true`). Each craft agent sees only its
craft's tools.

## What stays as it is

- The song document and how it compiles. A song that uses no craft field
  plays as before.
- `save_song` checks and reviews every song, whoever wrote it.
- Playing, measuring and recording use the existing player, taps and
  recorder, and count as busy for the update on the Mac.
- A technique, a craft tool or a craft agent never loads a session or replaces
  what AUM has open without the person's consent, like every AUM operation
  that loses state.

## Order of work

The epic https://github.com/teemow/mcp-midi-controller/issues/225 holds the
steps as issues.

1. **Cards and mastering.** Capabilities and the cards
   (https://github.com/teemow/mcp-midi-controller/issues/227), revisions and
   patches (https://github.com/teemow/mcp-midi-controller/issues/228), mapping
   ranges (https://github.com/teemow/mcp-midi-controller/issues/229),
   `apply_technique` and `profile_plugin` for effects
   (https://github.com/teemow/mcp-midi-controller/issues/230), loudness in
   `mix_song` (https://github.com/teemow/mcp-midi-controller/issues/231),
   `master_song` and the target cards
   (https://github.com/teemow/mcp-midi-controller/issues/232), and the plugin
   with the `mastering_engineer` agent
   (https://github.com/teemow/mcp-midi-controller/issues/233). It needs
   neither pan nor sends, is fully measurable, and carries the card design
   from end to end.
2. **Mixing.** Pan and send buses
   (https://github.com/teemow/mcp-midi-controller/issues/234), then the
   per-track measurements, `review_mix`, `balance_mix`, the mixing cards and
   the `mixing_engineer` agent
   (https://github.com/teemow/mcp-midi-controller/issues/235).
3. **Production, composition, drums.** `brief`, `energy`, `role` and the
   review lenses (https://github.com/teemow/mcp-midi-controller/issues/236),
   then the composer's and the drummer's transforms, the timing offsets and
   three craft agents
   (https://github.com/teemow/mcp-midi-controller/issues/237). This is what
   keeps songs from coming out alike.
4. **Sound design and orchestration.** `profile_plugin` for instruments,
   `find_sound`, archetypes, `add_movement`, `orchestrate`, `shape_dynamics`
   and two craft agents
   (https://github.com/teemow/mcp-midi-controller/issues/238). The tempo map
   and `shape_tempo` follow once it is known how a tempo change reaches the
   rig (https://github.com/teemow/mcp-midi-controller/issues/239).
5. **`produce_song`**, once the craft agents it runs are there
   (https://github.com/teemow/mcp-midi-controller/issues/240).

Each step ships its craft's text and craft agent with its tools, and each step
is usable without the next.

## Not known

- How AUM maps a CC onto a mapping range of a parameter whose plugin shows it
  on a log scale: linearly in the parameter's units, or along the plugin's own
  taper. `profile_plugin` measures it per parameter.
- Which plugins take a sidechain in AUM, and how a session routes one. Until
  that is known, a pump in time with the kick is a rhythmic gate or a
  modulator.
- How a tempo change reaches the session's sequencer plugin and AUM's
  transport.
- What the first target cards hold. They are written by hand and checked
  against reference recordings.
