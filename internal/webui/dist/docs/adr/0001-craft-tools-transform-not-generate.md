# Craft tools transform and check; the agent writes the material

The first songs written with the song tools came out alike because agents
copied the stored songs, not because they could not write chords, melodies or
grooves ([song-authoring.md](../song-authoring.md#9-a-review-of-the-song-as-music)).
So craft tools change and check material the agent wrote (develop a pattern,
voice a progression, lock a bass line to the kick, add fills, vary a groove,
spread voices over tracks) and none invents it: there is no generator of
progressions, melodies, bass lines, grooves or a song's plan. Grooves by style
are writing technique cards, and a seed picks among a card's variants.

## Considered options

- **Seeded generators in Go** (`write_progression`, `write_melody`,
  `write_bassline`, `write_groove`, `plan_song`), the first design in
  [craft-tools.md](../craft-tools.md): a lot of music theory code whose output
  tends to the generic, and it moves the musical choices from the agent that
  read the brief into a tool's options.
