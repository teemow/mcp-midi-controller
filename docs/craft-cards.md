# Craft cards: capabilities, technique cards and plugin cards

What the crafts of making a song know is kept as data the daemon reads
(design: [craft-tools.md](craft-tools.md)). A **technique card** says what a
craft does: "hold the true peak under -1 dBTP with a limiter". It names the
**capabilities** it needs (`dynamics.limiter`), never a plugin. A **plugin
card** says how one plugin provides a capability: which of its parameters
carry it and how a value reaches them. The rig's **order of preference** picks
the plugin. A technique so stays true when the rig changes, and a new plugin
is usable by every technique once it has a card.

The code is `internal/craft`; the shared tools registered so far are
`list_techniques`, `get_technique` and `patch_song`.

## Switching the tools on

Every connected agent reads the whole tool list, so the craft tools are a set
of their own. In `config.yaml`:

```yaml
craft_tools: true
```

## Where cards live

| What | Shipped with the daemon | The rig's own, in the config dir |
|------|-------------------------|----------------------------------|
| Technique cards | `internal/craft/cards/techniques/` | `craft/techniques/*.yaml` |
| Plugin cards | `internal/craft/cards/plugins/` | `craft/plugins/*.yaml` |
| Order of preference | | `craft/prefer.yaml` |

One card per file. A rig's card with the id of a shipped one replaces it (a
technique by its `id`, a plugin card by its component tuple). The cards are
read on every call, so an edit counts at once.

Every card is checked against the capabilities when it loads: an unknown
capability, parameter, field, craft, `applies_to` value or measurement is an
error, and so is a value outside its parameter's range. A card that does not
load is left out and named in the tools' `problems`; the others stay usable,
and a shipped card stays when the rig's card of the same id is broken.

## Capabilities

Capabilities are a closed list in Go (`internal/craft/capability.go`). Each
has parameters with a unit and a range, and the check `profile_plugin` uses to
tell that a plugin provides it: it sweeps one parameter and reads a
measurement at the tap, which has to move the way the capability says. A new
capability is a code change.

| Capability | Parameters | Check: sweep, read, moves |
|------------|-----------|---------------------------|
| `eq.highpass` | `freq` 10..2000 Hz, `slope` 6..96 dB/oct | `freq`; band level below `freq`; falls |
| `eq.lowpass` | `freq` 200..22000 Hz, `slope` 6..96 dB/oct | `freq`; brightness; rises |
| `eq.low_shelf` | `freq` 20..2000 Hz, `gain` -24..24 dB | `gain`; band level below `freq`; rises |
| `eq.high_shelf` | `freq` 1000..20000 Hz, `gain` -24..24 dB | `gain`; band level above `freq`; rises |
| `eq.bell` | `freq` 20..20000 Hz, `gain` -24..24 dB, `q` 0.1..30 Q | `gain`; band level around `freq`; rises |
| `dynamics.compressor` | `threshold` -60..0 dB, `ratio` 1..20 :1, `attack` 0.01..500 ms, `release` 1..5000 ms, `makeup` 0..30 dB | `threshold`; crest factor; rises |
| `dynamics.limiter` | `ceiling` -12..0 dBFS, `gain` 0..24 dB, `release` 1..2000 ms | `ceiling`; true peak; rises |
| `color.saturation` | `drive` 0..36 dB, `mix` 0..100 % | `drive`; brightness; rises |
| `stereo.width` | `width` 0..200 % (100 leaves it as it is) | `width`; side share; rises |
| `stereo.mono_bass` | `freq` 20..500 Hz | `freq`; side share 20..150 Hz; falls |
| `level.gain` | `gain` -24..24 dB | `gain`; level; rises |

The measurements, for a check and for a card's `verify`: `band_level` (dB, of
a band), `level` (rms, dBFS), `true_peak` (dBTP), `loudness` (integrated,
LUFS), `loudness_range` (LU), `peak_to_loudness` (dB), `crest_factor` (dB),
`brightness` (spectral centroid, Hz), `side_share` (%, optionally of a band),
`correlation` (-1..1).

## Technique cards

A card belongs to one craft: `production`, `composition`, `drums`,
`sound_design`, `orchestration`, `mixing` or `mastering`. Its id is the craft,
a dot and a name: `mastering.ceiling`. `applies_to` says what it is applied
to: `track`, `section`, `master` or `song` (one, or a list). `intent`, `when` and
`when_not` say what it is for and when it is not.

There are two kinds.

### Settings: what to set, where, how precisely, and how it is checked

```yaml
id: mixing.highpass_non_bass
craft: mixing
title: High-pass every track that is not bass
intent: Leave the low end to the kick and the bass.
when: A track whose role is not bass has energy below its lowest note.
when_not:
  - A sub pad or an 808 that is meant to fill the low end.
applies_to: track
needs: [eq.highpass]                 # the capabilities, in chain order
set:
  eq.highpass:
    freq: "clamp(0.7 * lowest_note_hz, 40, 250)"
    slope: 12
precision:                           # what a binding has to reach
  eq.highpass: {freq: "10%"}
place: first                         # first, last (default), "before <capability>", "after <capability>"
verify:
  measure: band_level
  band: [20, 80]                     # Hz; band_level needs one
  falls: 6                           # or rises, at_most, at_least
```

- `set` gives values to parameters of capabilities in `needs`; a capability
  in `needs` without values goes in as its plugin has it.
- `precision` is a share of the value (`"10%"`) or an amount in the
  parameter's unit (`0.1`).
- `verify` measures what the card is applied to before and after: `falls` and `rises` compare
  after with before (by at least the amount), `at_most` and `at_least` read
  the result.

### Writing: a transform and its options

```yaml
id: composition.lock_bass_to_kick
craft: composition
title: Lock the bass to the kick
applies_to: section
transform: lock_bass
options:
  amount: "clamp(section.energy * 10, 20, 90)"
```

Like a capability, a transform is code with typed options. None exists yet: the
composer's and the drummer's transforms
(https://github.com/teemow/mcp-midi-controller/issues/237) add theirs, and a
writing card loads once its transform does.

### Values

A value is a number, one of an enumerated parameter's labels, or arithmetic:
numbers, `+ - * /`, parentheses, `min(a, b, …)`, `max(a, b, …)`,
`clamp(x, low, high)` and `log2(x)` (for a plugin card whose plugin takes a
frequency as its octaves above 1 Hz, as Pro-Q 3 does: `value: "log2(freq)"`),
with what the song tells:

| Name | Is |
|------|----|
| `tempo` | beats per minute |
| `beat_ms` | one beat in ms |
| `bar_ms` | one bar in ms |
| `lowest_note_hz` | the lowest note of what the card is applied to, in Hz |
| `section.energy` | the section's energy, 1 to 10 |

A delay of `beat_ms * 0.75` stays in time when the tempo changes. Values are
parsed and checked when the card loads; a constant outside its parameter's
range, or one that is not a finite number, is an error there, and one that
follows the song is checked when it is worked out. Before a song is known it
is checked over the span a song can give it: `clamp(0.5 * lowest_note_hz, 20,
35)` is 20..35 Hz, and a plugin binds only if it reaches all of that. There is
nothing more: no conditions, no other functions.

## Plugin cards

A plugin is matched by its component tuple, `type/subtype/manufacturer` (four
characters each), the codes its probe dump carries:

```yaml
plugin: aufx/Eq1x/Test
name: Test EQ
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: {param: "Band 1 Frequency", range: [10, 400]}
      slope: {fixed: 12}             # it cannot be set; the plugin holds it
    enable:                          # set for the capability to work
      "Band 1 Used": on
      "Band 1 Shape": Low Cut
  eq.high_shelf:
    write: cc
    params:
      freq: "Band 4 Frequency"       # short for {param: …}
      gain: {param: "Band 4 Gain", value: "gain / 2", range: [-12, 12]}
notes:
  - Its saved state is not text; exact values come from a CC or a preset.
```

- `params` map a capability's parameters to the plugin's own, named as the
  probe dump names them (identifier or display name, as a song's `params`).
- `write` is the write path: `cc` (a Control Change through AUM's mapping of
  the parameter), `state` (a `field` of the plugin's saved state, where it is
  text) or `preset` (the `preset` that holds the settings, with the values it
  holds as `fixed`).
- `range` is the **mapping range**, in the plugin's units: the binding narrows
  AUM's mapping of the parameter to it, so the 128 steps of a CC fall where
  the capability is used. Without it a CC spans the parameter's whole range.
- `value` converts the capability's value into the plugin's units; it may use
  the capability's parameters by name (`attack / 1000` for a plugin in
  seconds). Without it the number is passed on as it is.
- `notes` hold what is learnt about the plugin, for the next session.
- `measured` says what `profile_plugin` measured of the plugin and when
  (`at`, `by`, and under `checks` a line per sweep and per parameter's step
  size); a card written by hand has none.

There is no `roles` field: what a plugin is for is the capabilities it
provides.

## The order of preference

`craft/prefer.yaml` orders, per capability, the plugins by tuple, most
preferred first:

```yaml
dynamics.limiter: [aufx/LimA/Test, aufx/LimB/Test]
eq.highpass: aufx/Eq1x/Test
```

Every plugin it names needs a card that provides the capability. The plugins
it leaves out come after the ones it names, by name.

## The tools

- **`list_techniques`** lists the cards, filtered by `craft`, `applies_to`,
  `capability`, `kind` or a word (`query`). With each, per capability it
  needs, the plugin of this rig that would carry it, or that none can.
- **`get_technique`** shows one card in full, the capabilities it needs with
  their parameters, the song values it uses, and its **bindings** on this rig:
  per capability every plugin with a card for it in the rig's order, the one
  it would use, how each parameter reaches it, and why any other cannot.
- **`patch_song`** applies an id-keyed merge patch
  ([ADR 0002](adr/0002-patches-by-id-with-a-revision.md)) to a stored song
  through `save_song`, refusing a patch whose `revision` is no longer current.
  Tracks, a track's `effects` and `modulators`, `master` and `stems.<stem>.effects`
  are addressed by id (`place`: `first`, `last`, `after: <id>`, `before: <id>`;
  `null` removes an entry; several entries placed `first` or `last` keep the
  patch's key order); lists of values (play lists, `arrangement`) are
  replaced whole. Every craft tool answers in one `craft.Answer` shape (`craft`,
  `techniques`, `patch`, `revision`, `why`, `checks`, `applied`), whose
  `revision` is the one the patch was made against, applied or not; an
  applied answer's text names the stored revision too.

- **`apply_technique`** applies a settings card to a track, one section of a
  track or the master (the effect declared bypassed, a scene switching it on
  there), or the master or the song. It works out the card's values for the
  song (its tempo, beat and bar, the lowest note of what it is applied to),
  binds every capability with them, converts each value into the plugin's
  units and places the effect as the card says: `before`/`after` a
  capability is before or after the effect whose plugin provides it, before
  one the chain lacks is last, after one it lacks is first, and an effect the
  chain has already (the card applied again) is set where it is; applied to
  the whole track or the master, it plays everywhere again, though a
  section's card declared it bypassed. Without
  `apply` the patch is checked only; with it the song is stored. When a
  capability has no plugin that carries it, it changes nothing and names the
  closest plugin and how far off it comes. `verify: true` (with `apply` and
  `confirm`) plays a pass of what the card is applied to, stores the change,
  loads the session again, plays the same pass, and judges the card's
  `verify` on what the master tap read before and after: a miss is reported
  in the answer's `checks`, and the change stays.
- **`profile_plugin`** measures an effect and writes its plugin card. It
  matches the probe dump's parameters to the capabilities by name, unit and
  range (a time in seconds gets `value: "attack / 1000"`, a share of 1 `mix /
  100`, and a parameter whose range is wider than the capability's gets the
  capability's part as its mapping range), or takes the capabilities an
  agent passes as `card` (a multi-band EQ's band and shape). Then a lab song
  (`plugin_lab`) plays white noise in bursts through the plugin, a section
  per value of each capability's sweep (a high-pass's cutoff at 40, 160 and
  640 Hz, a compressor's threshold at -40, -20 and 0 dB …), and the
  capability's measurement has to move the way it says. The capabilities
  that pass are written to `craft/plugins/` with `measured`, which also gives
  each parameter's step size over its mapping range (along its travel); the
  others are reported with their readings. A card of the plugin the rig has
  already is replaced, but keeps its notes and the capabilities that were not
  swept, with what was measured of them. `dry_run` only proposes the card.
- **`get_craft_job`** follows a craft tool that plays (`verify`,
  `profile_plugin`) and gives its report. Such a tool runs in the background
  like `mix_song`, one at a time, counts as busy and holds the host awake;
  `stop_song` ends it.

A plugin carries a capability when it is on the rig (its probe dump is staged,
`import_auv3_probe`), its card names parameters the dump has with the values
its `enable` sets, and it can set what the card sets. For a value that is a
number in the plugin's units, the value has to lie within the mapping range,
and the step of a CC there, along the parameter's travel (linear, or the log
scale its plugin shows it on, [songs.md](songs.md#mapping-ranges)), has to
reach the card's precision carried through the card's `value` conversion: a
ceiling of -1 dBFS within 0.1 dB is out of reach of a CC over 30 dB (steps of
0.24 dB), and in reach over 6 dB (0.05 dB). The step is the one near the
value: a parameter its plugin shows on a log scale moves along it, so its
steps grow with the value, and an Apple high-pass's cutoff over its whole
range (10 Hz to 22050 Hz) moves in steps of about 5 Hz near 80 Hz and 94 Hz
near 1500 Hz. Where the value follows the song, every value of its span is
checked, and the coarsest step is shown. A conversion that fails is a
problem of that plugin, not a binding. A plugin whose only trouble is its
reach has a **closeness**: its worst setting's error as a multiple of the
precision asked for (`off` in the bindings), and the binding names the
closest such plugin when none carries the capability.

## The shipped cards

The first mastering cards, for the master's chain in its order:

| Card | Sets | Checked by |
|------|------|------------|
| `mastering.subsonic_highpass` | `eq.highpass` at half the lowest note, 20 to 35 Hz, 24 dB/oct, first | 10..20 Hz falls by 6 dB |
| `mastering.mono_low_end` | `stereo.mono_bass` below 120 Hz, after the high-pass | side share 20..120 Hz at most 5 % |
| `mastering.air` | `eq.high_shelf` +1.5 dB at 12 kHz, before the compressor | 10..20 kHz rises by 1 dB |
| `mastering.glue` | `dynamics.compressor` 2:1 from -18 dB, 30 ms attack, release half a beat (50 to 400 ms), before the limiter | crest factor falls by 1 dB |
| `mastering.warmth` | `color.saturation` 3 dB of drive at 30 % mix, before the limiter | crest factor falls by 0.5 dB |
| `mastering.ceiling` | `dynamics.limiter` ceiling -1 dBFS, release 100 ms, its gain raised to the target, last | true peak at most -1 dBTP |

No plugin card ships yet: a card names a plugin's own parameters, which are
read from a probe dump and measured by `profile_plugin` on a rig, into the
rig's `craft/plugins/`.

## Not known

- When the per-plugin knowledge entry of
  https://github.com/teemow/mcp-midi-controller/issues/167 lands. A plugin card
  is to become a section of it, not a second record (decided 2026-11-14);
  until then the cards live in `craft/plugins/`.
