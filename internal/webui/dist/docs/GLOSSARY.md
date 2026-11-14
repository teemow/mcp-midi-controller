# Glossary

These are the words of the craft tools ([docs/craft-tools.md](docs/craft-tools.md)),
as the design review of 2026-11-13 settled them. The decisions behind them
are the ADRs in [docs/adr](docs/adr/). Where a term means one thing here and
another elsewhere, this meaning holds in the craft tools, their cards, their
issues and their code.

## Binding

How a capability is carried on this rig: the plugin that carries it, the
parameters its plugin card maps the capability to, the write path, and for a
`cc` path the mapping range. `apply_technique` takes the first binding in the
rig's order of preference whose write path reaches the technique's precision,
and otherwise names the closest plugin and how far off it is. `get_technique`
shows the bindings a technique would get.

## Brief

The song's optional `brief`: what the song is for (`intent`), its `genre`,
its `length`, its loudness target and a reference recording. The producer
writes it. Every craft reads it, and the `production` lens holds the song
against it. The compiler ignores it.

## Capability

A thing a plugin can do, such as `eq.highpass`. A capability has a name,
typed parameters with units and ranges, and the measurement that
`profile_plugin` checks it with. Capabilities are a closed list in Go, so a
new capability is a code change. Technique cards ask for capabilities and
plugin cards provide them, and neither names the other.

## Craft

A kind of work that goes into a song. Each craft has one id: `production`,
`composition`, `drums`, `sound_design`, `orchestration`, `mixing`,
`mastering`. The id names the craft's text (`docs/crafts/<craft>.md`), its
technique cards and its lens.

## Craft agent

A subagent that does one craft's work. It is named after the person who does
that work: `producer`, `composer`, `drummer`, `sound_designer`, `conductor`,
`mixing_engineer`, `mastering_engineer`. The agents ship as a Claude Code
plugin from this repository. Their definitions are generated from
`docs/crafts/<craft>.md`, and each agent gets only its craft's tools. A craft
agent is not an MCP prompt; the daemon serves no prompts.
[ADR 0004](docs/adr/0004-crafts-as-subagents.md).

## Energy

A section's optional `energy`, from 1 to 10. It is one number (`6`), or two
(`[4, 8]`) for a section that moves from the first to the second. Every craft
reads it: the drummer for density and fills, the conductor for dynamics, the
mixing engineer for rides, the producer for contrast and where the song
peaks. The compiler ignores it.

## Lens

One craft's reading of a song by `review_song`, done without playing the
song. A lens carries its craft's id: `production`, `composition`, `drums`,
`orchestration`. Every finding names the techniques that answer it.
`save_song`'s review is the `production` lens. Mixing reads measurements
instead: `review_mix` reads a `mix_song` report.

## Mapping range

The part of a parameter's range that AUM's CC mapping spans for one binding.
It is written as `range` on the parameter in the song. The 128 steps of a
7-bit CC are spread over the mapping range rather than over the parameter's
whole range, so a setting such as an 80 Hz high-pass can be reached. The range
is fixed per binding, not derived from the song's values, so a changed value
still plays live without loading the session again.
[ADR 0003](docs/adr/0003-exact-settings-from-mapping-ranges.md).

## Patch

A change to a song. It is a JSON merge patch (RFC 7386) in which tracks,
effects and every other entry with an id are keyed by that id, the way scenes
already address effects. A new entry carries its place (`first`, `last`,
`after: <id>`), `null` removes an entry, and a list of values (a play list,
the `arrangement`) is replaced whole. A patch names the revision it was made
against. Every craft tool returns one. `patch_song` stores it through
`save_song`, and so does the tool itself when it is called with `apply: true`.
[ADR 0002](docs/adr/0002-patches-by-id-with-a-revision.md).

## Plugin card

What one plugin can do and how it is set. The card lists the capabilities
the plugin provides, and maps each one to the plugin's own parameters, with
a write path, a mapping range and `notes` on the plugin's quirks. A card
matches its plugin by the component tuple. `profile_plugin` writes a card by
measuring. General cards ship with the daemon; a rig's own cards live in its
config dir.

## Revision

The identity of a song's stored content. `get_song` and `save_song` return
it. `patch_song` refuses a patch made against a revision that is no longer
current, so a craft agent never overwrites a change it has not seen.
[ADR 0002](docs/adr/0002-patches-by-id-with-a-revision.md).

## Rig

The setup one daemon plays: the host that runs AUM, the plugins installed
there, and the hardware and MIDI around them. What belongs to one rig lives in
the daemon's config dir, not in this repository: its plugin cards, its order
of preference per capability (`craft/prefer.yaml`) and its own technique
cards.

## Technique

One technique of one craft, written as data in a technique card: "high-pass
every track that is not bass", "a fill into a section that rises". A card
names the capabilities it needs and never a plugin. There are two kinds:

- A **settings** card says what to set, where in the chain (`place`), the
  `precision` a binding must reach, and how the result is checked (`verify`).
- A **writing** card names a transform and its options. Grooves by style are
  writing cards, and a seed picks among their variants.

A card's values use a small arithmetic: numbers, `+ - * /`, parentheses,
`min`, `max`, `clamp`, and what the song tells (`tempo`, `beat_ms`,
`bar_ms`, `lowest_note_hz`, `section.energy`). An example is
`clamp(0.7 * lowest_note_hz, 40, 250)`.

## Track role

A track's optional `role`, from a closed list: `bass`, `drums`,
`percussion`, `chords`, `pad`, `lead`, `counter`, `texture`, `fx`. A
`kind: drums` track defaults to `drums`. Techniques and lenses find tracks by
their role. One that needs a role the song does not have says so rather than
guessing. The compiler ignores it.

## Transform

A craft tool that changes or checks material the agent wrote, rather than
writing it from nothing: `voice_progression`, `lock_bass`, `develop_pattern`,
`add_fills`, `vary_groove`. The craft tools have no generators.
[ADR 0001](docs/adr/0001-transforms-not-generators.md).

## Write path

How a binding's value reaches the plugin:

- `cc`: the song's `params`, through AUM's CC mapping and the binding's
  mapping range.
- `state`: a field of the plugin's saved state. It is used only for a setting
  that no parameter exposes.
- `preset`: a factory preset, or a state captured from a node that was set by
  hand and then named.
