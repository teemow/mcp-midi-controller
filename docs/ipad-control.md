# Controlling and checking AUM over USB

The daemon reaches AUM two ways. **MIDI** (the ProbeMidiBrain LAN channel, BLE,
the `control_*` tools) is how it *plays* the rig. **USB** — the iPad's own user
interface, driven through [WebDriverAgent](https://github.com/appium/WebDriverAgent)
— is how it *looks at* AUM and operates it by touch. The two are independent,
which is the point: after the daemon sends MIDI into AUM, the iPad tools read
AUM's display back and prove the message landed.

```
agent ──MCP──▶ daemon ──MIDI (brain / BLE)──▶ AUM          (play)
                  └────USB (WebDriverAgent)──▶ AUM's UI     (look, touch, verify)
```

The device-side stack comes from the
[auv3-probe](https://github.com/teemow/auv3-probe) repository, which worked it
out: `scripts/ipad` runs it, and its `docs/ipad-automation.md` and
`docs/aum-ui-map.md` cover the setup and every AUM screen.

## Setup

On the host the iPad is plugged into (one-time steps are in auv3-probe's
`docs/ipad-automation.md`: go-ios, the developer disk image, WebDriverAgent,
Settings → Developer → Enable UI Automation):

```bash
scripts/ipad tunnel      # terminal 1, keep running
scripts/ipad mount-ddi   # once after every iPad restart
scripts/ipad wda         # terminal 2, keep running: WDA on 127.0.0.1:8100
```

The daemon needs nothing else when it runs on that host. In `config.yaml`:

| Key | Default | Meaning |
|---|---|---|
| `ipad_wda_url` | `http://127.0.0.1:8100` | WebDriverAgent endpoint. Point it at the iPad's host when the daemon runs elsewhere; `""` removes the iPad tools. |
| `ipad_go_ios` | `ios` | go-ios CLI, used read-only to diagnose a missing tunnel or disk image and to resolve AUv3 Probe's team-prefixed bundle ID. Give an absolute path when the service's `PATH` lacks it. |

Nothing is contacted at startup; an unplugged iPad only shows up when a tool is
called. `ipad_status` then names the missing layer and the command that brings it
up.

## Tools

| Tool | What it does |
|---|---|
| `ipad_status` | WDA reachable/ready, iPadOS version, screen size and scale, foreground app; diagnoses the stack when WDA is down. Opens no WDA session. |
| `ipad_screenshot` | The screen as an image. One image pixel = one point by default, so pixel positions are tap coordinates; `region` crops (in points), `pixels_per_point: 2` gives Retina detail. |
| `ipad_ui_tree` | An app's accessibility tree, one line per labelled element with its frame; `all`, `contains`, `limit`. |
| `ipad_tap` | Tap by `label`, by `x`+`y`, or by a named AUM `target` (`tempo`, `rewind`, `play`, `engine_info`, `navigator`, `midi_routing`, `menu`, `keyboard`). |
| `ipad_hold` / `ipad_swipe` | Long-press; drag as a touch (scroll AUM's mixer to strips that are off screen). |
| `ipad_launch` | Bring an app to the front, launching it when needed; a running app keeps its state. |
| `get_aum_ui` | AUM's state parsed from its UI: tempo, transport position, session name (while the main menu is open), DSP, start screen / menu open, and every strip with its number, title, fader dB and whether it is on screen. |
| `wait_for_aum_ui` | Re-read AUM (0.5–2 s per read, growing with the session) until expectations hold or `timeout_ms` passes: `tempo_bpm` / `not_tempo_bpm`, `session`, `transport` (`playing`/`stopped`), per-channel `level_db` / `not_level_db`. |

The tools take an `app` argument: `aum` (default), `probe` (AUv3 Probe), or any
bundle ID. Every call opens a WDA session bound to that app, which brings it to
the front, and closes it again with the app left running.

## Verifying MIDI against AUM

The loop is: read the baseline, send the MIDI, wait for AUM to show the result.

```text
get_aum_ui
  -> strip 2 "2: Synth" -1.0 dB, tempo 120
send_midi {kind: cc, channel: 1, controller: 7, value: 100}
  (or a control_* tool, set_transport, switch_aum_session, recall_scene)
wait_for_aum_ui {channels: [{channel: "2", not_level_db: -1.0}]}
  -> matched, or NOT matched: channel "2: Synth": level still -1.0 dB
```

Use exact expectations when the target value is known (`tempo_bpm: 128`,
`session: "<name> (mcp test)"` after `switch_aum_session`), and the `not_*`
forms when only a change is known (a CC's value curve into AUM's fader or tempo
range). AUM shows the session name only in its main menu, so open it first
(`ipad_tap target: menu`) when waiting for a session switch. The main meter's
accessibility label also starts with `SESSION: …`, but AUM does not refresh it
(it can still list channels of a session cleared long ago), so it is ignored. `transport: "playing"` holds when the position advances between two
reads. Not matching is a normal result (`matched: false` plus what still
differs), so a silent mapping shows up as a clear "level still -1.0 dB".

What AUM's tree does **not** carry, and how to check it instead:

| State | Check with |
|---|---|
| Mute, solo, record-arm | `ipad_screenshot` with `region` on the strip's `MUTE`/`SOLO`/`REC` frame (from `ipad_ui_tree`), before and after; the tree lists these buttons without an on/off value |
| Plugin parameters | the audio tap (`get_audio_tap`, `probe_sound`); plugin views are out-of-process |
| MIDI Control mappings | `ipad_tap target: menu`, then the MIDI CTRL icon; or read the session file with `get_aum_session` |

## Care

- **One WDA session at a time.** WDA serves a single session; a new one ends the
  previous. The daemon serializes its own calls, but another WDA client (a second
  agent running `scripts/ipad tap`) and the daemon can cut each other off
  mid-action. Use one driver at a time.
- **Taps can change or lose state.** CLEAR, a session row in FILES (loads it),
  SAVE SESSION, REMOVE CHANNEL, the eject `⏏`, MIDI Control's Clear All, and
  record (writes files; deliberately not a named target). Screenshot first near
  them. Per [AGENTS.md](../AGENTS.md), iterate on the ` (mcp test)` session copies,
  never on the originals.
- **Screenshots and trees are private.** They show session, plugin and device
  names. The daemon returns them to the caller and stores nothing; keep them out
  of this public repository.
- **Coordinates are points** in the app's current orientation. AUM rotates with
  the iPad: `get_aum_ui` reports the window and orientation; the named targets
  follow it.
