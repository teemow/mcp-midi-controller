# A patch addresses entries by id and names its revision

A craft tool returns its change to a song as a merge patch in which tracks,
effects and every other entry with an id are keyed by that id, as scenes
already address them; a new effect carries its place (`first`, `last`,
`after: <id>`) and `null` removes an entry. The patch names the revision of
the song it was made against, and `patch_song` refuses it once the song has
moved on.

## Considered options

- **RFC 7386 JSON merge patch**: replaces lists whole, and tracks and effect
  chains are lists. One high-pass on one track would carry the whole `tracks`
  list, and two patches on different tracks would overwrite each other without
  a word.
- **RFC 6902 JSON Patch**: addresses list entries by index, which points at
  another track as soon as one is added or moved.
