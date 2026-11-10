# AUv3 plugins in AUM: what a full inspection showed

Every AUv3 that AUM 1.4.8's pickers offered was loaded on its own into a muted
channel and inspected: 60 instruments, 92 effects and music effects (17 of them
Apple's built-in AUs) and 30 MIDI processors. For each one the inspection recorded
the window, presets, MIDI Controls list, MIDI routing ports, reported latency and
bus layout. It drove AUM through WebDriverAgent (the `scripts/ipad` helper of
[auv3-probe](https://github.com/teemow/auv3-probe)) and merged the AUv3 Probe
scan, which read 184 units, for units and ranges.

This note keeps what holds for any AUM setup. The per-plugin results (the plugin
list, parameter lists, presets and screenshots) describe one installation and are
in `docs/private/aum-inspection/`, which is not part of this repository.

## What it means for CC maps

- **AUM's MIDI Controls list is not the AU parameter tree.** For 31 of 60
  instruments AUM listed fewer parameters than the probe read from the tree: 1,594
  against 7,359 in total, with a median of 60 % per plugin. A CC map written from
  the probe's tree can name parameters AUM doesn't offer for mapping. Check each
  mapped parameter against AUM's MIDI Controls list, not only against the probe
  JSON.
- **Some plugins expose no parameters to AUM at all.** 11 of the 30 MIDI
  processors show an empty MIDI Controls list. They can't be controlled through an
  AUM mapping; only their own MIDI input reaches them.
- **Plain effects take no MIDI.** No effect (`aufx`, 0 of 60) has a MIDI input in
  AUM's routing matrix. Every instrument (`aumu`, 60) and every music effect that
  loaded (`aumf`, 31 of 32) has one. So an `aufx` effect is controllable only through an AUM
  MIDI Controls mapping, never by routing MIDI to the plugin. MIDI outputs: 31 of
  60 instruments, 9 of 92 effects; 5 of the 30 MIDI processors have more than one
  (up to 17).
- **AUM's Presets list shows only what the AU exposes.** 39 of 92 effects and 14 of
  60 instruments show no presets in AUM. Some of them keep their presets inside
  their own UI instead. Anything that selects presets through AUM's list can't
  reach those. For 4 instruments the probe found presets that AUM didn't list.
- **The main parameter is optional.** AUM prints a main-parameter label next to a
  node's knob for some plugins only; 43 of 60 instruments show none.

## Latency, buses and registrations

- **Latency:** AUM's Engine Info → Node Statistics → Latency lists only nodes that
  report latency: 21 of 92 effects, from +0.1 ms to +100 ms, and none of the
  instruments. For 18 of them the probe read a latency within 4 ms of AUM's. Two
  Apple AUs (AUNewPitch, AUSoundIsolation) reported 0 to the probe but 85 ms and
  76 ms to AUM, so the probe's figure can't stand in for AUM's.
- **Multi-bus:** AUM names each multi-bus layout under Inserts & Effects →
  Multi-bus Audio Unit Instances (for example 2-in / 1-out for a sidechain). 19
  effects and 3 instruments have one.
- **One plugin, several registrations:** 18 of the 30 MIDI processors are also
  registered as an instrument or effect, often with the same subtype, and one
  instrument is registered twice under codes that differ only in case. Keying probe
  records by subtype alone loses units: the receiver's staging dropped 26 of 184
  that way (fixed by staging by the whole component id,
  https://github.com/teemow/mcp-midi-controller/pull/124).
- **What the pickers list:** AUM's source picker listed no AUv3 generators, and it
  didn't list Apple's AUMIDISynth and AUSampler, which the probe sees, although
  Apple plugins were on. Inter-App Audio apps are listed separately; they are apps,
  not plugins.

## Driving AUM while plugins are open

- No plugin crashed AUM. One failed to load ("Scene was invalidated", and RELOAD
  failed too).
- Apple's filter and EQ AUs with a graph view (AUBandpassFilter, AUHighPassFilter,
  AUHighShelfFilter, AUNBandEQ, AUParametricEQ) stall WebDriverAgent. With any
  Apple AU window open, element queries hang, while coordinate taps still work.
  Once a Presets or MIDI Controls popover opens over one of those graph views, taps
  stall too, and only restarting AUM helps.
- AUM exposes every MIDI Controls row to UI automation, the off-screen ones too, so
  one accessibility-tree dump reads all of a plugin's parameters. Other long lists
  (presets, pickers) expose only their visible rows: scroll and read each page.
- A parameter row reads `<name>\n<min> → <max>  [<value>]`. In the MIDI routing
  matrix the sources are vertical labels and the destinations horizontal ones,
  which is how the inspection told MIDI inputs from outputs.

AUM's screens, gestures and automation traps are mapped in auv3-probe's
`docs/aum-ui-map.md` and `docs/ipad-automation.md`.
