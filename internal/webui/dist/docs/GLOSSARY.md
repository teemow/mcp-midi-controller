# mcp-midi-controller

A daemon that lets an agent write songs as documents and play them on the rig:
it builds the AUM session that hosts a song's instruments and effects, plays the
song's MIDI into it, and listens to what comes out. The craft layer on top
teaches the work of the people who make a song.

## Language

### The rig

**Rig**:
The one physical setup the daemon drives: the iPad with AUM and the plugins
installed in it, the Mac, and the devices around them.
_Avoid_: setup, studio

### The song

**Track role**:
A track's part in the music, from a closed list: bass, drums, percussion,
chords, pad, lead, counter, texture, fx.
_Avoid_: role for what a plugin does (that is a capability), kind (`kind: drums`
is how a track's notes are written)

**Brief**:
What a song is for: its intent, genre, length, loudness target and a
reference recording. Every craft holds the song against it.
_Avoid_: spec, goal

**Energy**:
A section's intensity on a scale of 1 to 10, either held for the whole section
or moving in a straight line from a start to an end.
_Avoid_: intensity, level (a level is a track's loudness)

**Revision**:
The identity of a stored song's content at one moment. A song changes revision
whenever it is stored.
_Avoid_: version

**Patch**:
A change to a song made against one revision, in which tracks, effects and
other entries are addressed by their ids.
_Avoid_: diff, rewrite

### Crafts

**Craft**:
One discipline of making a song, with one id: production, composition, drums,
sound design, orchestration, mixing, mastering.
_Avoid_: role, harmony or ensemble (as craft names), arrangement (that is the
song's order of sections)

**Craft agent**:
A subagent that practises one craft and nothing else, named after the person
who does: producer, composer, drummer, sound_designer, conductor,
mixing_engineer, mastering_engineer.
_Avoid_: craft prompt, persona

**Lens**:
One craft's reading of a song without playing it. A lens carries its craft's
id.
_Avoid_: view, check

**Transform**:
A craft tool that changes or checks material the agent wrote, and never
invents it.
_Avoid_: generator

### Techniques and plugins

**Capability**:
A named function a plugin can provide, such as `eq.highpass`, with its
parameters in their units and ranges. The vocabulary techniques and plugin
cards share.
_Avoid_: feature, role

**Technique**:
One recipe of one craft: the capabilities it needs and what it sets, or the
transform it runs, and how its result is checked. Kept as a technique card.
_Avoid_: recipe, preset

**Archetype**:
A kind of sound, such as a sub bass, a reese or a pluck, kept as a technique
of sound design that says what such a sound measures as.
_Avoid_: character (it is not measured)

**Target**:
A named aim for a mix or a master, such as `streaming` or a genre: its
loudness and true peak, its tonal curve, and each track role's level against
an anchor.
_Avoid_: reference (that is a recording to compare with)

**Knowledge entry**:
Everything known about one installed plugin that is not obvious from its name,
its interface or its parameter labels.
_Avoid_: plugin notes, plugin doc

**Plugin card**:
The part of a plugin's knowledge entry that says what the plugin provides: its
capabilities and, for each, the plugin's own parameters, write path and
mapping range.
_Avoid_: plugin profile

**Binding**:
One capability of a technique matched to one plugin on the rig, with its write
path and the precision that path reaches.
_Avoid_: mapping (a mapping is AUM's link from a MIDI message to a parameter)

**Write path**:
How a binding's value reaches a plugin: as a mapped Control Change (`cc`), as
a field of the plugin's saved state (`state`), or as a preset (`preset`).

**Mapping range**:
The part of a parameter's span that a Control Change's 128 steps cover in a
song's session. A binding fixes it.
_Avoid_: resolution (that is how far apart the steps are)
