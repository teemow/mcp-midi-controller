# Craft tools: design

The song tools ([songs.md](songs.md), [song-authoring.md](song-authoring.md))
teach an agent the song document: how a pattern is written, how a session is
built, how a song is loaded, played and measured. They do not teach the work
of the people who make a song: the producer, the composer, the drummer, the
synth player, the conductor, the mixing and the mastering engineer. That shows
in the songs. The first eight written with the tools were one song eight times
([A review of the song as music](song-authoring.md#9-a-review-of-the-song-as-music)),
and balancing a song's levels took up most of the session that wrote it.

This note designs a craft layer on top of the song tools. Each craft brings
its knowledge as prompts, its techniques as data, and its skills as tools that
write, develop, check and apply. The rule of the song feature holds: nothing
AUM or a plugin can do is taken away, every addition is optional, and a song
written by hand compiles to the same session and the same MIDI as before.

## The crafts

| Craft | Knows | Owns in the song | Checks |
|-------|-------|------------------|--------|
| Producer | form, energy and contrast, the sound palette, references, when a song is done | the brief, the sections' energy, the arrangement, the palette | length, contrast between sections, where the song peaks, sounds the other songs already use, the song against its brief |
| Composer | harmony, voice leading, melody, motif and its development, bass lines | the pitched patterns | chord tones on strong beats, voice leading, notes of two tracks that clash, repeats without development |
| Drummer | grooves by style, feel (swing, ghost notes, a lane ahead of or behind the beat), fills, variation, the kit | drum patterns, play lists, a drum machine's own variation | a fill before a section that rises, a crash on its first beat, kick and bass together, the spread of velocities |
| Sound designer | synthesis by archetype, each synth's controls and quirks, the measured presets, movement | an instrument's preset and parameters, modulators, automation of a sound | measured pitch, envelope and brightness against what was asked for |
| Conductor | ranges, orchestration, balance between parts, dynamics and phrasing, tempo, entries | which track plays which voice, velocities, gate, level arcs, the tempo's shape | the register each track takes, ranges, flat dynamics, parts that never rest |
| Mixing engineer | gain staging, EQ and masking, dynamics, space, width, automation | track levels, effect chains, pan, sends, rides | levels and bands per track, tracks that mask each other, mud and harshness, wet and dry, width |
| Mastering engineer | loudness targets, tonal balance, limiting, translation | the master's chain | integrated loudness, true peak, peak to loudness, the tonal curve against a target, a mono low end |

## Four layers

| Layer | Holds | Form |
|-------|-------|------|
| Craft prompts | How a craft judges and works: what it listens for, its order of work, when it hands over | MCP prompts, one per craft, and `produce_song`, which runs the crafts in order |
| Craft tools | The verbs: write, develop, review, apply | Go: music theory (voice leading, grooves, harmonic analysis) and measurement (loudness, bands, stereo) |
| Technique cards | Recipes: "high-pass every track that is not bass", "a fill into a section that rises", "parallel compression on drums" | YAML. A card names the capabilities it needs, never a plugin, and how its result is checked |
| Plugin cards | What an installed plugin can do and how it is set: capability to parameter, value mapping, write path, quirks | YAML. General cards ship with the daemon; the rig's own cards and its order of preference live in the config dir |

A technique says what it needs (`eq.highpass`); a plugin card says how one
plugin provides it; the rig's order of preference picks the plugin. That is
how a technique works with the plugins a rig has, and how a new plugin becomes
usable by every technique once it has a card.

Below the layers stays what is there: the song document, the sound catalogue,
the taps, `audition_sound`, `mix_song`, the player and the recorder.

## Rules

1. **Patches, not rewrites.** A craft tool returns a patch to the song
   document, the techniques it used and why. With `apply: true` it stores the
   patched song through `save_song`, so the document is checked and reviewed
   as always. No craft tool writes a song another way.
2. **Measured.** A technique says how its result is checked. With
   `verify: true` the tool plays what it changed and measures before and
   after; a result that misses is reported, not hidden.
3. **Capabilities, not plugin names.** Techniques stay true when the rig
   changes. A technique the rig has no plugin for says which capability is
   missing.
4. **Variety by design.** Generators take a `seed`; sound choices rank what
   the rig's other songs have not used. A tool that proposes the same thing
   for every song is a defect.
5. **Knowledge as data.** What is learnt about a plugin or a technique goes
   into a card, where the next session finds it, and not into a transcript.

## Two fields in the song document

```yaml
brief:                         # the producer's, read by every craft
  intent: "a night drive: restless verses, a chorus that opens up"
  genre: synthwave
  length: "3:45"
  loudness: streaming          # the mastering target
  reference: Night Drive 2026-11-12 1903   # a recording to compare with

sections:
  verse1:  {bars: 16, energy: 4, play: {…}}
  chorus1: {bars: 8,  energy: 8, play: {…}}
```

- `brief` says what the song is for. `plan_song` writes it, and every review
  holds the song against it.
- `energy` (1 to 10) on a section is the one number all crafts read: the
  drummer's density and fill intensity, the conductor's dynamics, the mixing
  engineer's rides, the producer's check of contrast and peak.

Both are optional, and the compiler ignores them: a song plays the same with
or without them.

## Technique cards

A card is one technique of one craft:

```yaml
id: mix.highpass_non_bass
craft: mix
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
    freq: "0.7 * lowest_note_hz"     # clamped to 40..250 Hz
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
- **Writing** techniques (a groove, a fill, a voicing, a development of a
  motif) name the generator that does the work and the options they give it:
  `generator: add_fills` with `{phrase_bars: 8, intensity: energy}`.

A card's values may use what the song tells: `tempo`, `beat_ms`, `bar_ms`,
`lowest_note_hz`, `section.energy`. A delay time is then written as
`beat_ms * 0.75` and stays in time when the tempo changes.

The cards ship with the daemon (`internal/craft/techniques/`). The rig may add
its own in the config dir (`craft/techniques/`), where a card with the same id
replaces the shipped one.

## Plugin cards

A card says which capabilities one plugin provides and how each is set:

```yaml
plugin: aumf/FQ3p/FabF               # matched by the component tuple
name: Pro-Q 3
roles: [eq]
capabilities:
  eq.highpass:
    set:                             # parameter names as the probe dump has them
      "Band 1 Used": on
      "Band 1 Shape": Low Cut
      "Band 1 Frequency": "{freq}"
      "Band 1 Slope": "{slope} dB/oct"
    write: cc
    resolution: {"Band 1 Frequency": measured}   # by profile_plugin
notes:
  - "Its saved state is proprietary: exact values come from CC or a preset."
```

The example is illustrative: the parameter names and the resolution are what
`profile_plugin` reads and measures on a rig.

### How a value reaches a plugin

A song's parameter values travel as a 7-bit Control Change spread over the
parameter's whole range (`paramWire` in `internal/song/plan.go`). For movement
that is enough. For a setting it may not be: a frequency exposed linearly from
10 Hz to 30 kHz has 128 steps about 235 Hz apart, and an 80 Hz high-pass cannot
be set. So a capability names its write path:

| Path | How | Precision |
|------|-----|-----------|
| `cc` | The song's `params`, as today | the 128 steps of the parameter's range, measured by `profile_plugin` |
| `state` | A field of the plugin's saved state, where it is text (JSON, JUCE XML): capture-and-mutate, see [auv3-state-authoring.md](auv3-state-authoring.md) | exact |
| `preset` | A factory preset, or a state captured from a node set by hand and named | exact, for the settings the preset holds |

A technique states the precision it needs. `apply_technique` takes the first
plugin in the rig's order whose path reaches it, and otherwise says which
plugin came closest and how far off it is.

### Where cards come from

- `profile_plugin` reads a plugin's probe dump, matches its parameters to
  capabilities by name, unit and range, and then measures: it sweeps a
  parameter at the tap and checks that the expected thing moves (a cutoff
  moves the spectral centroid, a high-pass the low band, a threshold the crest
  factor). It also measures each parameter's step size. The result is a card
  in the config dir that says what was measured and when.
- What the research notes found about a plugin (a synth that needs its gain
  staged first, a glide that only sounds legato, a drum machine's own
  variation) goes into the card's `notes`, which `get_song_plugin` and the
  craft prompts show.
- General cards for widely used plugins ship with the daemon. The rig's order
  of preference per capability is its own (`craft/prefer.yaml` in the config
  dir).

## The tools

All craft tools take the same arguments where they apply: `song`, `section` or
`sections`, `track`, `seed`, `apply` (default false), `verify`. They return the
same shape:

```yaml
craft: drums
techniques: [drums.fill_into_section]
patch:                               # a JSON merge patch (RFC 7386) on the song
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

A tool that plays (a check with `verify`, `balance_mix`, `master_song`) runs
in the background like `mix_song`, counts as busy, and reports through
`get_craft_job`.

### Shared

| Tool | Does |
|------|------|
| `list_techniques` | The cards, by craft, target or capability, each with the plugins of the rig that can carry it |
| `get_technique` | One card in full, with how it would be bound on this rig |
| `apply_technique` | Applies a card to a track, a section, the master or the song |
| `patch_song` | Applies a patch a tool returned without `apply` |
| `review_song` | Reads a song through lenses (`production`, `harmony`, `drums`, `ensemble`) without playing it. Every finding names the techniques that answer it. `save_song`'s review is its `production` lens |
| `get_craft_job` | The progress or the report of a craft tool that plays |

### Producer

| Tool | Takes | Gives |
|------|-------|-------|
| `plan_song` | a brief, a seed | A skeleton: sections with bars and energy, where each part enters and leaves, key and tempo, a palette ranked by the brief and by what other songs have not used. Patterns are left to the other crafts |

`compare_to_reference` (mastering) serves the producer too.

### Composer

| Tool | Takes | Gives |
|------|-------|-------|
| `write_progression` | key, mode, bars, function (verse, chorus, bridge), harmonic rhythm, cadence, colour (borrowed chords, secondary dominants, suspensions) | Chords with voice leading, voiced in the range of the track that plays them |
| `write_melody` | the chords or section it goes over, range, motif, contour, phrase (`4+4`, `8+8`) | A melody with chord tones on strong beats, approach tones and a high point placed in the phrase |
| `write_bassline` | the chords, style (root, fifths, octaves, walking, pedal, syncopated), `lock_to` a drum track | A bass line that follows the chords and meets the kick |
| `develop_pattern` | a pattern, transforms (sequence, inversion, retrograde, augmentation, fragmentation, displacement, register, thinning, thickening) | Variants of it, so a section that returns does not return unchanged |

### Drummer

| Tool | Takes | Gives |
|------|-------|-------|
| `write_groove` | style, feel (swing, ghost notes, hat dynamics, a lane's offset in ms) | A groove on the kit's measured pads |
| `add_fills` | phrase length, which sections, intensity (default: from energy) | Play lists that end a phrase in a fill, and a crash on the first beat of the next section |
| `vary_groove` | amount, moves (open hats, a displaced kick, ghost notes, ride, half time) | Variants across a phrase, so a four-bar loop differs in every bar; for a drum machine that varies itself, its variation parameters |

### Sound designer

| Tool | Takes | Gives |
|------|-------|-------|
| `find_sound` | role (bass, pad, lead, pluck, keys, bell, texture), character, register, attack, sustain | Presets ranked from the catalogue, and a plan to audition those not measured yet |
| `design_sound` | plugin, archetype (sub bass, reese, acid, pluck, supersaw, pad, brass, bell), a preset to start from | Parameters mapped from the archetype onto the plugin's own, checked with `audition_sound` (pitch, envelope, brightness) |
| `add_movement` | the target (filter, wavetable, pitch, an effect's mix), shape, rate, depth, sections | A modulator or automation |
| `profile_plugin` | a plugin | Its plugin card, measured |

### Conductor

| Tool | Takes | Gives |
|------|-------|-------|
| `orchestrate` | the material (chords, lines), the tracks, the texture (block, spread, doubled, counterpoint) | Voices spread over tracks by range, with doublings, the melody kept clear |
| `shape_dynamics` | an arc per section (or the energy), phrasing | Velocity arcs per phrase, crescendos and diminuendos as level ramps, articulation as gate |
| `shape_tempo` | a ritardando, a fermata, an accelerando | Needs a tempo map, below |

The `ensemble` lens of `review_song` is the conductor's check.

### Mixing engineer

| Tool | Takes | Gives |
|------|-------|-------|
| `review_mix` | a song | `mix_song`'s report and, per track and section, bands, crest factor and stereo: balance against an anchor track, tracks that mask each other, mud, harshness, low end outside the bass, a mix all dry or all wet, everything in the middle. Every finding names its techniques |
| `balance_mix` | an anchor track, targets (or a genre's), iterations | Levels set by measuring, adjusting and measuring again |

The rest of the craft is technique cards: carving out masking, bus glue,
parallel compression, a pump in time with the kick (a rhythmic gate such as
Perforator, or a modulator on a level), saturation for presence, reverb with a
pre-delay in time, delay throws at the end of a phrase, width, rides.

### Mastering engineer

| Tool | Takes | Gives |
|------|-------|-------|
| `measure_master` | a song or a recording | Integrated and short-term loudness (LUFS), loudness range, true peak, peak to loudness, tonal balance against the target, stereo correlation, a mono low end. A recording is measured from its file |
| `master_song` | a target (streaming, club, dynamic, or LUFS and true peak), character (transparent, warm, punchy) | The master's chain (EQ, glue, saturation where asked for, a limiter), its gain raised until the target is met. Where peak to loudness falls too low first, it stops and hands the song back to the mix with why |
| `compare_to_reference` | a song or a recording, a reference recording | Loudness, tonal curve, width and dynamics against the reference, and the EQ moves that close the gap |

## Prompts and the order of work

Each craft has an MCP prompt: `producer`, `composer`, `drummer`,
`sound_designer`, `conductor`, `mixing_engineer`, `mastering_engineer`. A
prompt holds the craft's judgement in words (what it listens for, its order of
work, its checklist, when it is done), the song's brief and the findings of
the craft's lens, and the tools in the order the craft uses them. The text
lives in `docs/crafts/<craft>.md`, so the web UI's docs tab shows people what
the prompt tells an agent.

`produce_song` runs them in order, with the producer looking at the song
twice:

1. producer: the brief, `plan_song`;
2. composer: harmony, parts, motifs and their development;
3. drummer: grooves, fills, variation;
4. sound designer: sounds for every part, movement;
5. conductor: orchestration, dynamics, tempo;
6. producer: `review_song` against the brief;
7. mixing engineer: `review_mix`, `balance_mix`, techniques;
8. producer: the song as a whole, once more;
9. mastering engineer: `master_song`, then the recording and its upload.

Any craft can be called on its own: a drummer for a song written by hand, a
mix for a song that is otherwise done.

## What the measurements need

| Measurement | For | From |
|-------------|-----|------|
| Loudness after ITU-R BS.1770: momentary, short-term, integrated, loudness range; true peak, oversampled | mastering, the mix's levels | the master tap during `mix_song`, and a recording's file |
| Bands, crest factor and onsets per track and section | masking, mud and harshness, dynamics, kick and bass together | the track taps; the tap already computes eight log-spaced bands per window |
| Stereo correlation and the share of the side signal, in total and below about 150 Hz | width, a mono low end | the taps' stereo stream |
| Onsets against the grid | the feel of a groove | a recording |

## What the core needs

| Need | For | Where it lands |
|------|-----|----------------|
| Pan per track | mix, the conductor's seating | `pan` on a track and in scenes; AUM's pan node in the session |
| Send buses | shared reverb and delay | `sends` on a track and bus strips; AUM's bus send nodes. A bus strip counts against the strips a session holds |
| Exact settings | mix, mastering, sound design | a plugin's text state per track in a song (capture-and-mutate), captured presets |
| Timing offsets per drum lane | the drummer's feel | an offset in ms on a lane or a pattern, applied by the compiler |
| A tempo map | the conductor | a tempo per section and ramps, followed by both ways of [playing](songs.md#playing) |
| `brief`, `energy` | every craft | the song document, ignored by the compiler |

## The number of tools

The daemon serves 112 tools, and the crafts add about 26. Every connected
agent reads the whole list, so the craft tools form a set of their own that
the config switches on (`craft_tools: true`). The prompts name the tools a
craft uses.

## What stays as it is

- The song document and how it compiles. A song that uses no craft field
  plays as before.
- `save_song` checks and reviews every song, whoever wrote it.
- Playing, measuring and recording use the existing player, taps and
  recorder, and count as busy for the update on the Mac.
- A technique or a craft tool never loads a session or replaces what AUM has
  open without `confirm: true`, like every AUM operation that loses state.

## Order of work

1. **Cards and mastering.** The two card formats, `list_techniques`,
   `get_technique`, `apply_technique`, `patch_song`, `profile_plugin` for
   effects, the loudness meter, `measure_master` and `master_song`. It is
   small and fully measurable, and it carries the card design from end to
   end.
2. **Mixing.** Pan and send buses in the core, the per-track measurements,
   `review_mix`, `balance_mix`, the mixing cards.
3. **Producer, composer, drummer.** `brief` and `energy`, `plan_song`,
   `review_song`, the composer's and the drummer's generators, the timing
   offsets. This is what keeps songs from coming out alike.
4. **Sound designer and conductor.** `profile_plugin` for instruments,
   `find_sound`, `design_sound`, `add_movement`, `orchestrate`,
   `shape_dynamics`, the tempo map.
5. **The prompts** for each craft and `produce_song`, written as the tools of
   that craft land.

Each step is usable without the next.

## Not known

- How AUM maps a CC onto a parameter whose plugin shows it on a log scale:
  linearly in the parameter's units, or along the plugin's own taper.
  `profile_plugin` measures it per parameter.
- Which plugins take a sidechain in AUM, and how a session routes one. Until
  that is known, a pump in time with the kick is a rhythmic gate or a
  modulator.
- How a tempo change reaches the session's sequencer plugin and AUM's
  transport.
- Where the targets of a genre (tonal balance, loudness, the levels of kick
  and bass) come from. The start is a few written by hand, checked against
  reference recordings.
- How much a generator decides by itself and how much it leaves to options.
  The seed keeps a choice repeatable; the options keep it open.
