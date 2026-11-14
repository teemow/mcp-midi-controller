# Craft tools: design

The song tools ([songs.md](songs.md), [song-authoring.md](song-authoring.md))
teach an agent the song document: how a pattern is written, how a session is
built, how a song is loaded, played and measured. They do not teach the work
of the people who make a song: the producer, the composer, the drummer, the
synth player, the conductor and the mixing and mastering engineers. That shows
in the songs. The first eight written with the tools were one song eight times
([A review of the song as music](song-authoring.md#9-a-review-of-the-song-as-music)),
and balancing a song's levels took up most of the session that wrote it.

This note designs a craft layer on top of the song tools. Each craft is an
agent that brings its judgement, its techniques as data, and tools that change
and check what it writes. The rule of the song feature holds: nothing AUM or a
plugin can do is taken away, every addition is optional, and a song written by
hand compiles to the same session and the same MIDI as before.

The design was reviewed on 2026-11-13, and this note is the revised version.
The review's decisions are four ADRs:

| ADR | Decision |
|-----|----------|
| [0001](adr/0001-transforms-not-generators.md) | Craft tools transform the agent's material; they do not generate it |
| [0002](adr/0002-patches-by-id-with-a-revision.md) | Patches address entries by id and name the song's revision |
| [0003](adr/0003-exact-settings-from-mapping-ranges.md) | Exact settings come from mapping ranges |
| [0004](adr/0004-crafts-as-subagents.md) | Crafts run as subagents, not as MCP prompts |

The terms (craft, lens, transform, capability, binding, …) are defined in
[GLOSSARY.md](../GLOSSARY.md). The work is the epic
https://github.com/teemow/mcp-midi-controller/issues/225.

## The crafts

| Craft | Agent | Knows | Owns in the song | Checks |
|-------|-------|-------|------------------|--------|
| `production` | `producer` | form, energy and contrast, the sound palette, references, when a song is done | the brief, the sections' energy, the arrangement, the palette | length, contrast between sections, where the song peaks, sounds the other songs already use, the song against its brief |
| `composition` | `composer` | harmony, voice leading, melody, motif and its development, bass lines | the pitched patterns | chord tones on strong beats, voice leading, notes of two tracks that clash, repeats without development |
| `drums` | `drummer` | grooves by style, feel (swing, ghost notes, a lane ahead of or behind the beat), fills, variation, the kit | drum patterns, play lists, a drum machine's own variation | a fill before a section that rises, a crash on its first beat, kick and bass together, the spread of velocities |
| `sound_design` | `sound_designer` | synthesis by archetype, each synth's controls and quirks, the measured presets, movement | an instrument's preset and parameters, modulators, automation of a sound | measured pitch, envelope and brightness against the archetype |
| `orchestration` | `conductor` | ranges, orchestration, balance between parts, dynamics and phrasing, tempo, entries | which track plays which voice, velocities, gate, level arcs, the tempo's shape | the register each track takes, ranges, flat dynamics, parts that never rest |
| `mixing` | `mixing_engineer` | gain staging, EQ and masking, dynamics, space, width, automation | track levels, effect chains, pan, sends, rides | levels and bands per track, tracks that mask each other, mud and harshness, wet and dry, width |
| `mastering` | `mastering_engineer` | loudness targets, tonal balance, limiting, translation | the master's chain | integrated loudness, true peak, peak to loudness, the tonal curve against a target, a mono low end |

The craft's id names its text (`docs/crafts/<craft>.md`), its technique cards
and its lens. The agent is named after the person who does the work.

## The layers

| Layer | Holds | Form |
|-------|-------|------|
| Craft agents | How a craft judges and works: what it listens for, its order of work, when it hands over | Subagents in a Claude Code plugin from this repository, generated from `docs/crafts/<craft>.md`, each with only its craft's tools; `produce_song` is a skill that runs them in order ([ADR 0004](adr/0004-crafts-as-subagents.md)) |
| Craft tools | The verbs: transform, review, apply, measure | Go: music theory (voice leading, onsets against the kick, development of a motif) and measurement (loudness, bands, stereo) |
| Technique cards | Recipes: "high-pass every track that is not bass", "a fill into a section that rises", "parallel compression on drums" | YAML. A card names the capabilities it needs, never a plugin, and how its result is checked |
| Plugin cards | What an installed plugin can do and how it is set: capability to parameter, write path, mapping range | YAML, a section of the plugin's knowledge entry (https://github.com/teemow/mcp-midi-controller/issues/167). General cards ship with the daemon; the rig's own cards and its order of preference live in the config dir |
| Target cards | What a mix or a master aims for: loudness and true peak, a tonal curve, each track role's level against an anchor | YAML, by name: `streaming`, `club`, `dynamic`, or a genre |

Between the cards sit the **capabilities**, a closed list in Go (below). A
technique says what it needs (`eq.highpass`); a plugin card says how one
plugin provides it; the rig's order of preference picks the plugin. That is
how a technique works with the plugins a rig has, and how a new plugin becomes
usable by every technique once it has a card.

Below the layers stays what is there: the song document, the sound catalogue,
the taps, `audition_sound`, `mix_song`, the player and the recorder.

## Rules

1. **Transforms, not generators.** A craft tool changes or checks material
   the agent wrote. It does not write a progression, a melody, a bass line, a
   groove or a song's plan from nothing
   ([ADR 0001](adr/0001-transforms-not-generators.md)).
2. **Patches, not rewrites.** A craft tool returns a patch to the song, the
   techniques it used and why. The patch addresses tracks, effects and other
   entries by id, and names the revision of the song it was made against.
   `patch_song` refuses a patch made against an older revision. With
   `apply: true` the tool stores the patched song through `save_song`, so the
   document is checked and reviewed as always. No craft tool writes a song
   another way ([ADR 0002](adr/0002-patches-by-id-with-a-revision.md)).
3. **Measured.** A technique says how its result is checked. With
   `verify: true` the tool plays what it changed and measures before and
   after; a result that misses is reported, not hidden.
4. **Capabilities, not plugin names.** Techniques stay true when the rig
   changes. A technique the rig has no plugin for says which capability is
   missing.
5. **Exact where it is asked for.** A setting reaches the precision its
   technique asks for, through a mapping range, or the tool says which plugin
   came closest and how far off it is
   ([ADR 0003](adr/0003-exact-settings-from-mapping-ranges.md)).
6. **Variety by design.** A seed picks among a card's variants, and sound
   choices rank what the rig's other songs have not used. A tool that
   proposes the same thing for every song is a defect.
7. **Knowledge as data.** What is learnt about a plugin or a technique goes
   into a card, where the next session finds it, and not into a transcript.

## New fields in the song document

```yaml
brief:                         # the producer's, read by every craft
  intent: "a night drive: restless verses, a chorus that opens up"
  genre: synthwave
  length: "3:45"
  loudness: streaming          # the mastering target
  reference: Night Drive 2026-11-12 1903   # a recording to compare with

tracks:
  - id: pad
    role: pad                  # bass, drums, percussion, chords, pad, lead, counter, texture, fx
    instrument: {…}
    effects:
      - id: hp
        plugin: …
        params: {"Band 1 Frequency": 80}
        range:  {"Band 1 Frequency": [20, 400]}   # the binding's mapping range

sections:
  verse1:  {bars: 16, energy: 4,      play: {…}}
  build:   {bars: 4,  energy: [4, 8], play: {…}}
  chorus1: {bars: 8,  energy: 8,      play: {…}}
```

- `brief` says what the song is for. The producer writes it, and every
  review holds the song against it.
- `energy` (1 to 10) on a section is the one number all crafts read: the
  drummer's density and fill intensity, the conductor's dynamics, the mixing
  engineer's rides, the producer's check of contrast and peak. Two numbers
  (`[4, 8]`) are a section that moves from the first to the second.
- `role` on a track is from a closed list. A `kind: drums` track defaults to
  `drums`. Techniques and lenses find tracks by role; one that needs a role
  the song does not have says so and does not guess.
- `range` on a parameter is the mapping range of its binding, below.

All four are optional. The compiler ignores `brief`, `energy` and `role`: a
song plays the same with or without them. A song without `range` compiles to
the same session and the same MIDI as before.

## Capabilities

A capability is one thing a plugin can do: `eq.highpass`, `eq.bell`,
`dynamics.compressor`, `limiter`, `reverb`, `delay`, … Each has a name,
parameters with a type, a unit and a range, and the measurement that
`profile_plugin` checks it with (a high-pass lowers the low band, a threshold
the crest factor). The list is closed and lives in Go. A new capability is a
code change, and a card that names an unknown capability or parameter fails
when it loads.

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
applies_to: track                    # track | section | master | song
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

- **Settings** insert or set capabilities in a chain, as above: what to set,
  `place`, `precision` and `verify`. They are data only.
- **Writing** techniques name a transform and the options they give it:
  `transform: add_fills` with `{phrase_bars: 8, intensity: section.energy}`.
  Grooves by style are writing cards; a seed picks among their variants.

A card's values use a small arithmetic: numbers, `+ - * /`, parentheses,
`min`, `max`, `clamp`, and what the song tells: `tempo`, `beat_ms`, `bar_ms`,
`lowest_note_hz`, `section.energy`. A delay time is then written as
`beat_ms * 0.75` and stays in time when the tempo changes. Values are parsed
and checked when the card loads; there is no general expression language.

The cards ship with the daemon (`internal/craft/techniques/`). The rig may add
its own in the config dir (`craft/techniques/`), where a card with the same id
replaces the shipped one.

## Plugin cards

A plugin card is a section of the plugin's knowledge entry
(https://github.com/teemow/mcp-midi-controller/issues/167), not a second record. It says which capabilities the plugin
provides and how each is set:

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
    range: {"Band 1 Frequency": [20, 400]}       # the binding's mapping range
    resolution: {"Band 1 Frequency": measured}   # by profile_plugin
notes:
  - "Its saved state is proprietary: settings go over CC."
```

The example is illustrative: the parameter names, the range and the
resolution are what `profile_plugin` reads and measures on a rig. A card has
no `roles`: what a plugin is for follows from the capabilities it provides.

### How a value reaches a plugin

A song's parameter values travel as a 7-bit Control Change spread over the
parameter's whole range (`paramWire` in `internal/song/plan.go`). For movement
that is enough. For a setting it may not be: a frequency exposed linearly from
10 Hz to 30 kHz has 128 steps about 235 Hz apart, and an 80 Hz high-pass cannot
be set.

So a binding narrows AUM's CC mapping to a fixed range, its **mapping range**,
and the 128 steps are spread over that range: from 40 Hz to 250 Hz they are
about 1.7 Hz apart, or along the scale a plugin shows the frequency on: on a
log scale about 1.2 Hz apart near 80 Hz (measured,
[songs.md](songs.md#mapping-ranges)). The range is fixed per binding and not derived from the
song's values, so a changed value still plays live without loading the
session again ([ADR 0003](adr/0003-exact-settings-from-mapping-ranges.md)).

A capability names its write path:

| Path | How | Precision |
|------|-----|-----------|
| `cc` | The song's `params`, as today, through the binding's mapping range | the 128 steps of the mapping range, measured by `profile_plugin` |
| `state` | A field of the plugin's saved state, where it is text (JSON, JUCE XML): capture-and-mutate, see [auv3-state-authoring.md](auv3-state-authoring.md). Only for a setting no parameter exposes | exact |
| `preset` | A factory preset, or a state captured from a node set by hand and named | exact, for the settings the preset holds |

A technique states the precision it needs. `apply_technique` takes the first
binding in the rig's order of preference whose path reaches it, and otherwise
says which plugin came closest and how far off it is.

### Where cards come from

- `profile_plugin` reads a plugin's probe dump, matches its parameters to
  capabilities by name, unit and range, and then measures: it sweeps a
  parameter at the tap and checks that the expected thing moves (a cutoff
  moves the spectral centroid, a high-pass the low band, a threshold the crest
  factor). It also measures each parameter's step size within its mapping
  range. The result is a card in the config dir that says what was measured
  and when.
- What the research notes found about a plugin (a synth that needs its gain
  staged first, a glide that only sounds legato, a drum machine's own
  variation) is the rest of the plugin's knowledge entry; the card's `notes`
  are the entry's, which `get_song_plugin` and the craft agents show.
- General cards for widely used plugins ship with the daemon. The rig's order
  of preference per capability is its own (`craft/prefer.yaml` in the config
  dir).

## Target cards

A target is what a mix or a master aims for:

```yaml
id: synthwave
loudness: {integrated: -14, true_peak: -1}     # LUFS, dBTP
tonal: [[30, -2], [120, 0], [1000, -3], [8000, -9], [16000, -14]]   # Hz, dB
levels:                                         # each role against the anchor, dB
  anchor: drums
  bass: -2
  pad: -9
  lead: -4
```

The brief's `loudness` and `genre` pick the targets: `master_song` meets the
loudness, `balance_mix` the levels, and both read the tonal curve. The first
targets are written by hand and checked with `measure_master` against
reference recordings. The values above are illustrative. Target cards ship
with the daemon, and a rig may add its own in the config dir, as with
technique cards (https://github.com/teemow/mcp-midi-controller/issues/232).

## The tools

All craft tools take the same arguments where they apply: `song`, `section` or
`sections`, `track`, `seed`, `apply` (default false), `verify`. They return the
same shape:

```yaml
craft: drums
techniques: [drums.fill_into_section]
revision: 7c1e…                      # the revision the patch was made against
patch:                               # a merge patch; entries with an id are keyed by it
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

In a patch, tracks, effects and every other entry with an id are keyed by
that id, as scenes already address effects. A new entry carries its place
(`first`, `last`, `after: <id>`, `before: <id>`), `null` removes an entry,
and a list of values (a play list, the `arrangement`) is replaced whole. Several entries
placed `first` (or `last`) keep the patch's key order, which a JSON object
need not keep, so an entry whose order matters is chained with `after: <id>`.
With `apply: true` the tool reads, patches and stores the song under one
lock. Its `revision` is still the one the patch was made against, so `patch`
and `revision` always belong together: handed to `patch_song`, an applied
answer is refused as stale instead of being applied twice, unless its patch
changed nothing and the stored revision is still the base. Its text also
names the stored revision (`revision: <stored> (from <revision>)`), so an
agent can report the revision before and after.

A tool that plays (a check with `verify`, `balance_mix`, `master_song`) runs
in the background like `mix_song`, counts as busy, and reports through
`get_craft_job`.

### Shared

| Tool | Does |
|------|------|
| `list_techniques` | The cards, by craft, what they apply to or capability, each with the plugins of the rig that can carry it |
| `get_technique` | One card in full, with the bindings it would get on this rig |
| `apply_technique` | Applies a card to a track, a section, the master or the song |
| `patch_song` | Applies a patch a tool returned without `apply`, through `save_song`; refuses a patch whose revision is no longer current |
| `review_song` | Reads a song through lenses without playing it: `production`, `composition`, `drums`, `orchestration`, each its craft's id. Every finding names the techniques that answer it. `save_song`'s review is its `production` lens |
| `get_craft_job` | The progress or the report of a craft tool that plays |

### Production

The producer writes the brief, the sections with their bars and energy, where
each part enters and leaves, and the palette. No tool writes them
([ADR 0001](adr/0001-transforms-not-generators.md)). The `production` lens of
`review_song` checks them against each other, and `compare_to_reference`
(mastering) serves the producer too.

### Composition

| Tool | Takes | Gives |
|------|-------|-------|
| `voice_progression` | chords the agent wrote, the track that plays them | The chords voiced with voice leading, in the range of that track |
| `lock_bass` | a bass line, the drum track and lane it meets | The bass line's onsets moved onto the kick, or a report of where they miss it |
| `develop_pattern` | a pattern, transforms (sequence, inversion, retrograde, augmentation, fragmentation, displacement, register, thinning, thickening) | Variants of it, so a section that returns does not return unchanged |

### Drums

| Tool | Takes | Gives |
|------|-------|-------|
| `add_fills` | phrase length, which sections, intensity (default: from energy) | Play lists that end a phrase in a fill, and a crash on the first beat of the next section |
| `vary_groove` | amount, moves (open hats, a displaced kick, ghost notes, ride, half time) | Variants across a phrase, so a four-bar loop differs in every bar; for a drum machine that varies itself, its variation parameters |

Grooves by style are writing technique cards, applied with
`apply_technique`; a seed picks among their variants. The feel of a lane
ahead of or behind the beat is a timing offset in the core, below.

### Sound design

| Tool | Takes | Gives |
|------|-------|-------|
| `find_sound` | role, register, attack, sustain, brightness | Presets ranked from the catalogue by what is measured and by what the rig's other songs have not used, and a plan to audition those not measured yet |
| `add_movement` | the target (filter, wavetable, pitch, an effect's mix), shape, rate, depth, sections | A modulator or automation |
| `profile_plugin` | a plugin | Its plugin card, measured |

The sound designer sets an instrument's parameters itself; no tool maps an
archetype onto a plugin. An **archetype** (sub bass, reese, acid, pluck,
supersaw, pad, brass, bell) is a technique card of `sound_design` that says
what such a sound measures as: its pitch, envelope and brightness.
`audition_sound` takes a `technique` and compares what it measured with the
card. For that, the sound catalogue gains a brightness per preset; today only
drum pads have a spectral centroid.

### Orchestration

| Tool | Takes | Gives |
|------|-------|-------|
| `orchestrate` | the material (chords, lines), the tracks, the texture (block, spread, doubled, counterpoint) | Voices spread over tracks by range, with doublings, the melody kept clear |
| `shape_dynamics` | an arc per section (or the energy), phrasing | Velocity arcs per phrase, crescendos and diminuendos as level ramps, articulation as gate |
| `shape_tempo` | a ritardando, a fermata, an accelerando | Needs a tempo map, below |

The `orchestration` lens of `review_song` is the conductor's check.

### Mixing

| Tool | Takes | Gives |
|------|-------|-------|
| `review_mix` | a `mix_song` report | Findings without playing again, per track and section: balance against an anchor track, tracks that mask each other, mud, harshness, low end outside the bass, a mix all dry or all wet, everything in the middle. Every finding names its techniques |
| `balance_mix` | an anchor track, a target, iterations | Levels set by measuring, adjusting and measuring again |

The rest of the craft is technique cards: high-pass on every track that is not
bass, carving out masking, bus glue, parallel compression, a pump in time with
the kick (a rhythmic gate such as Perforator, or a modulator on a level),
saturation for presence, reverb with a pre-delay in time, delay throws at the
end of a phrase, width, rides.

### Mastering

| Tool | Takes | Gives |
|------|-------|-------|
| `measure_master` | a recording | Integrated and short-term loudness (LUFS), loudness range, true peak, peak to loudness, tonal balance, stereo correlation, a mono low end, measured from the file. For a song, the same numbers are in `mix_song`'s report |
| `master_song` | a target (`streaming`, `club`, `dynamic`, or LUFS and true peak), character (transparent, warm, punchy) | The master's chain (EQ, glue, saturation where asked for, a limiter), its gain raised until the target is met. Where peak to loudness falls too low first, it stops and hands the song back to mixing with why |
| `compare_to_reference` | a song or a recording, a reference recording | Loudness, tonal curve, width and dynamics against the reference, and the EQ moves that close the gap |

## Craft agents and `produce_song`

Each craft is a craft agent: `producer`, `composer`, `drummer`,
`sound_designer`, `conductor`, `mixing_engineer`, `mastering_engineer`
([ADR 0004](adr/0004-crafts-as-subagents.md)). There are no MCP prompts.

- **The plugin.** The agents ship as a Claude Code plugin from this
  repository. Their definitions are generated at build time from
  `docs/crafts/<craft>.md`, the text that holds the craft's judgement in
  words: what it listens for, its order of work, its checklist, when it is
  done. The web UI's docs tab shows people the same text. Each agent gets
  only its craft's tools. The plugin's MCP config takes the daemon's URL from
  an environment variable, so no host is written into the repository.
- **What an agent may do.** It stores its own patches with `apply: true`,
  through `save_song`. It loads, plays and measures only the song it works
  on. Started alone, it asks the person for consent to load; started by
  `produce_song`, its task carries that consent.
- **What an agent reports.** The revision before and after, what it changed
  and why, what it checked, the findings it left open, and what it hands to
  which craft.

`produce_song` is a skill in the plugin. It asks the person once for consent
to load the song's session, then runs the agents one at a time, with the
producer looking at the song three times:

1. producer: the brief, the sections and their energy, the palette;
2. composer: harmony, parts, motifs and their development;
3. drummer: grooves, fills, variation;
4. sound designer: sounds for every part, movement;
5. conductor: orchestration, dynamics, tempo;
6. producer: `review_song` against the brief;
7. mixing engineer: `mix_song`, `review_mix`, `balance_mix`, techniques;
8. producer: the song as a whole, once more;
9. mastering engineer: `master_song`, then the recording and its upload.

It passes each agent's hand-over to the next agent's task, and never writes
the song itself. Any craft agent can be started on its own: a drummer for a
song written by hand, a mixing engineer for a song that is otherwise done.

## What the measurements need

| Measurement | For | From |
|-------------|-----|------|
| Loudness after ITU-R BS.1770: momentary, short-term, integrated, loudness range; true peak, oversampled; peak to loudness; tonal balance | mastering, the mix's levels | the master tap's PCM in `mix_song`'s pass; a recording's file in `measure_master` |
| Bands, crest factor and onsets per track and section | masking, mud and harshness, dynamics, kick and bass together | the track taps' PCM in `mix_song`'s pass; the tap already computes eight log-spaced bands per window |
| Stereo correlation and the share of the side signal, in total and below about 150 Hz | width, a mono low end | the taps' stereo PCM in `mix_song`'s pass |
| Onsets against the grid | the feel of a groove | a recording |
| Brightness per preset | archetypes, `find_sound` | the sound catalogue's audition of the preset |

`mix_song` already plays the whole song once, and the taps send raw float32
PCM on every channel, so the measuring happens in that one pass. `review_mix`
and `master_song`'s checks read its report.

## What the core needs

| Need | For | Where it lands |
|------|-----|----------------|
| Song revisions | patches by id | `get_song` and `save_song` return the revision; `patch_song` refuses a stale patch |
| Mapping ranges | mixing, mastering, sound design | `range` on a parameter; the session author narrows AUM's CC mapping to it |
| Pan per track | mixing, the conductor's seating | `pan` on a track and in scenes; a pan node class in the session library, captured from a session AUM saved |
| Send buses | shared reverb and delay | `sends` on a track and bus strips; AUM's bus send nodes. A bus strip counts against the strips a session holds |
| Timing offsets per drum lane | the drummer's feel | an offset in ms on a lane or a pattern, applied by the compiler |
| A tempo map | the conductor | a tempo per section and ramps, followed by both ways of [playing](songs.md#playing) |
| `brief`, `energy`, `role` | every craft | the song document, ignored by the compiler |

## The number of tools

The daemon serves more than 110 tools, and the crafts add about 22. Every
connected agent reads the whole list, so the craft tools form a set of their
own that the config switches on (`craft_tools: true`). Each craft agent sees
only its own craft's tools.

## What stays as it is

- The song document and how it compiles. A song that uses no craft field
  plays as before.
- `save_song` checks and reviews every song, whoever wrote it.
- Playing, measuring and recording use the existing player, taps and
  recorder, and count as busy for the update on the Mac.
- A technique or a craft tool never loads a session or replaces what AUM has
  open without `confirm: true`, like every AUM operation that loses state. A
  craft agent asks the person before it loads, unless its task carries
  `produce_song`'s consent.

## Order of work

The epic, https://github.com/teemow/mcp-midi-controller/issues/225, sets the
order. Each step is usable without the next.

1. **The review's decisions**
   (https://github.com/teemow/mcp-midi-controller/issues/226): this note,
   [GLOSSARY.md](../GLOSSARY.md) and ADRs 0001–0004.
2. **Capabilities, technique cards and plugin cards**
   (https://github.com/teemow/mcp-midi-controller/issues/227): the
   capabilities in Go, both card formats, the card arithmetic, the rig's
   order of preference, `list_techniques`, `get_technique`, and
   `craft_tools: true`.
3. **Song revisions and patches by id**
   (https://github.com/teemow/mcp-midi-controller/issues/228): the revision,
   `patch_song`, and the one shape every craft tool answers in.
4. **Mapping ranges**
   (https://github.com/teemow/mcp-midi-controller/issues/229): `range` on a
   parameter, and how AUM maps a CC onto it, measured.
5. **`apply_technique` and `profile_plugin` for effects**
   (https://github.com/teemow/mcp-midi-controller/issues/230), with
   `verify` and `get_craft_job`.
6. **Loudness, true peak and stereo in `mix_song`; `measure_master`**
   (https://github.com/teemow/mcp-midi-controller/issues/231).
7. **`master_song`, `compare_to_reference` and the mastering cards**
   (https://github.com/teemow/mcp-midi-controller/issues/232), and
   `docs/crafts/mastering.md`.
8. **Craft agents: the Claude Code plugin and `mastering_engineer`**
   (https://github.com/teemow/mcp-midi-controller/issues/233).
9. **Pan and send buses in songs**
   (https://github.com/teemow/mcp-midi-controller/issues/234).
10. **The mixing craft**
    (https://github.com/teemow/mcp-midi-controller/issues/235): per-track
    measurements, `review_mix`, `balance_mix`, the mixing cards and
    `mixing_engineer`.
11. **Brief, energy, track roles and the review lenses**
    (https://github.com/teemow/mcp-midi-controller/issues/236).
12. **The composer's and the drummer's transforms**
    (https://github.com/teemow/mcp-midi-controller/issues/237): the timing
    offsets, and the `producer`, `composer` and `drummer` agents.
13. **Sound design and orchestration**
    (https://github.com/teemow/mcp-midi-controller/issues/238), and the
    `sound_designer` and `conductor` agents.
14. **The tempo map**
    (https://github.com/teemow/mcp-midi-controller/issues/239) and
    `shape_tempo`.
15. **`produce_song`**
    (https://github.com/teemow/mcp-midi-controller/issues/240), the skill
    that runs the craft agents.

https://github.com/teemow/mcp-midi-controller/issues/166 overlaps: plugin
cards with its per-plugin knowledge entries
(https://github.com/teemow/mcp-midi-controller/issues/167), and
`profile_plugin`, `find_sound` and auditioning with its sound-testing tools
(https://github.com/teemow/mcp-midi-controller/issues/172) and expert use
(https://github.com/teemow/mcp-midi-controller/issues/173).

## Not known

- What the first target cards hold. They are written by hand and checked
  against reference recordings (https://github.com/teemow/mcp-midi-controller/issues/232).
- Which plugins take a sidechain in AUM, and how a session routes one. Until
  that is known, a pump in time with the kick is a rhythmic gate or a
  modulator.
- How a tempo change reaches the session's sequencer plugin and AUM's
  transport (https://github.com/teemow/mcp-midi-controller/issues/239).
