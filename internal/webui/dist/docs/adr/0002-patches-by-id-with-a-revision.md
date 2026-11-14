# ADR 0002: Patches address entries by id and name the song's revision

- **Status:** accepted
- **Date:** 2026-11-13, in the design review of
  [craft-tools.md](../craft-tools.md)
  (https://github.com/teemow/mcp-midi-controller/pull/224)
- **Epic:** https://github.com/teemow/mcp-midi-controller/issues/225
- **Terms:** [GLOSSARY.md](../../GLOSSARY.md)

## Context

In the first design, a craft tool returned a JSON merge patch (RFC 7386) on
the song document. A merge patch replaces a list whole. A song's tracks and
effect chains are lists (`tracks`, a track's `effects`, `master`), so a
patch that changes one parameter of one effect has to carry the whole list of
tracks. A patch like that also undoes, without saying so, any change that
another craft made to those tracks since it was read.

Several craft agents work on a song one after another, and a person may edit
it in between. Scenes already address a track's effects by their ids
(`scenes.<scene>.<track>.effects.<effect id>`), not by their place in the
chain.

## Decision

- **A revision.** `get_song` and `save_song` return the song's revision, the
  identity of its stored content.
- **Keyed by id.** A patch is a merge patch in which tracks, effects and
  every other entry with an id are keyed by that id. A new entry carries its
  place (`first`, `last`, `after: <id>`), and `null` removes an entry. Lists
  of values, such as a play list or the `arrangement`, are replaced whole.
- **Against a revision.** A patch names the revision it was made against.
  `patch_song` applies a patch through `save_song`, and refuses one whose
  revision is no longer current.
- **One answer.** Every craft tool answers in the same shape: `craft`,
  `techniques`, `patch`, `revision`, `why`, `checks`, `applied`. With
  `apply: true` it reads the song, patches it and stores it under one lock.

```yaml
revision: 7c1e…                      # the revision the patch was made against
patch:
  tracks:
    pad:
      effects:
        hp:   {place: first, plugin: …, params: {…}}
        verb: {params: {Mix: 25%}}
        chorus: null                 # removed
  sections:
    verse2: {play: {drums: [beat, beat, beat, fill]}}   # a list of values, replaced whole
```

## Consequences

- A patch carries only what it changes, and it reads the way a scene does.
- A stale patch fails, and it says so. The agent reads the song again and
  makes the patch again. Nothing is merged silently.
- The patch is not a plain RFC 7386 patch on the stored YAML. `patch_song`
  resolves the ids to list positions before it applies the patch, so an
  entry that has an id needs one that is unique within its list. `save_song`
  already requires that of tracks and of the effects in a chain, where an
  effect's id defaults to its plugin's.
- Everything still goes through `save_song`, so a patched song is checked
  and reviewed like any other song.
