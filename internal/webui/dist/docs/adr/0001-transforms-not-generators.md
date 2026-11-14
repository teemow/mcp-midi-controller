# ADR 0001: Craft tools transform the agent's material; they do not generate it

- **Status:** accepted
- **Date:** 2026-11-13, in the design review of
  [craft-tools.md](../craft-tools.md)
  (https://github.com/teemow/mcp-midi-controller/pull/224)
- **Epic:** https://github.com/teemow/mcp-midi-controller/issues/225
- **Terms:** [GLOSSARY.md](../../GLOSSARY.md)

## Context

The first design gave the producer, the composer and the drummer generators.
`plan_song` wrote a song's skeleton from a brief. `write_progression`,
`write_melody`, `write_bassline` and `write_groove` wrote material from
options and a seed.

The songs written with the song tools came out alike
([A review of the song as music](../song-authoring.md#9-a-review-of-the-song-as-music)).
The reason was not that the agent cannot write music. Every session wrote its
song after the songs it had read. A generator moves that sameness into code:
the same options give the same kind of song, and the taste of the generator
is fixed in Go. The design had already counted "a tool that proposes the same
thing for every song" as a defect. It had also left open how much a generator
decides by itself and how much it leaves to options. And an agent that
calls a generator stops writing the material it is then asked to judge.

## Decision

Craft tools change and check material that the agent wrote. They do not write
it from nothing.

- `plan_song`, `write_progression`, `write_melody`, `write_bassline` and
  `write_groove` are dropped.
- `voice_progression` voices chords the agent wrote, with voice leading, in
  the range of the track that plays them.
- `lock_bass` moves a bass line's onsets onto the kick, or reports where
  they miss it.
- `develop_pattern`, `add_fills` and `vary_groove` stay. Each one changes a
  pattern or a play list that already exists.
- Grooves by style become writing technique cards. A seed picks among a
  card's variants.
- The producer agent writes the brief and the song's skeleton itself, and
  the `production` lens of `review_song` checks them against each other.

## Consequences

- The music is the agent's. What the crafts know lives in their texts
  (`docs/crafts/<craft>.md`) and in technique cards, not in a generator.
- The checks carry the weight: the lenses, `lock_bass`'s report, and the
  measurements. A craft agent writes, checks and changes its own work.
- A seed only chooses among the options a tool or a card already offers,
  such as the variants of a groove. It keeps that choice repeatable.
- The rule on variety still holds for what a tool proposes. `find_sound`
  ranks what the rig's other songs have not used, and a transform that does
  the same thing to every song is a defect.
- There are fewer craft tools: about 23 rather than about 26.
