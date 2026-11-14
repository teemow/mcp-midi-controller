# ADR 0003: Exact settings come from mapping ranges

- **Status:** accepted
- **Date:** 2026-11-13, in the design review of
  [craft-tools.md](../craft-tools.md)
  (https://github.com/teemow/mcp-midi-controller/pull/224)
- **Epic:** https://github.com/teemow/mcp-midi-controller/issues/225
- **Terms:** [GLOSSARY.md](../../GLOSSARY.md)

## Context

A song's parameter value reaches a plugin as a 7-bit Control Change, spread
over the parameter's whole range (`paramWire` in `internal/song/plan.go`).
That is enough for movement, but not for a setting. Take a frequency that is
exposed linearly from 10 Hz to 30 kHz: its 128 steps are about 235 Hz apart,
so an 80 Hz high-pass cannot be set.

The first design reached exact values through a second write path, the
plugin's saved state. That path has costs:

- A plugin keeps its own format, and many of them are not text.
- The state needs code per plugin.
- A change to the state needs the session to load again, so it does not
  play live.

AUM's CC mapping can be narrowed to a part of a parameter's range, but the
song's session author never narrows it.

## Decision

- **`range` on a parameter.** A parameter in the song may carry an optional
  `range`. The session author narrows AUM's mapping to that range, and the
  128 steps are spread over it.
- **Fixed per binding.** The range belongs to the binding. It is not derived
  from the values the song sets, so a changed value still plays live without
  loading the session again.
- **Plugin cards carry it.** A plugin card gives the mapping range for the
  parameters of each capability it provides. A technique's `precision`
  decides whether a binding, with its range, reaches the value.
- **State as a last resort.** A plugin's saved state is written only for a
  setting that no parameter exposes.
- **Unchanged without it.** A song without `range` compiles to the same
  session and the same MIDI as before.
- **Measured.** How AUM maps a CC onto a range has to be measured: linearly in
  the parameter's value, or along the plugin's own taper. It is measured on a
  plugin that shows its frequency on a log scale. `profile_plugin` measures
  each binding's step size.

```yaml
effects:
  - id: hp
    plugin: …
    params: {"Band 1 Frequency": 80}
    range:  {"Band 1 Frequency": [40, 250]}   # 128 steps about 1.7 Hz apart
```

## Consequences

- Settings reach the precision of a mix and a master through the path that
  already plays live, and without code per plugin.
- A value outside its binding's range cannot be reached. Moving the range
  changes the session, so the session loads again.
- The state path stays, but only for settings that no parameter exposes. For
  the rest a plugin needs no parsing of its state.
- Until AUM's mapping onto a range is measured, a binding's precision is the
  measured step size, not the computed one.
