# Exact settings come from mapping ranges, not saved state

A song's parameter values reach a plugin as a 7-bit Control Change. Mapped
over a parameter's whole range the steps are too coarse for a setting: a
frequency from 10 Hz to 30 kHz has steps about 235 Hz apart, and an 80 Hz
high-pass cannot be set. AUM's MIDI mapping has an input range, so a binding
fixes a mapping range per parameter and the song's session maps the 128 steps
over it. The range is fixed per binding, not taken from the song's values, so
changing a value still plays live without reloading the session. A plugin's
saved state is written only for settings no parameter exposes.

## Considered options

- **The plugin's saved state for every exact setting** (capture-and-mutate):
  exact, but only for plugins whose state is text, and every changed value
  rebuilds the session and needs a reload.
