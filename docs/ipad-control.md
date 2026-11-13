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
| `ipad_go_ios` | `ios` | go-ios CLI, used read-only to diagnose a missing tunnel or disk image and to resolve AUv3 Probe's team-prefixed bundle ID. Give an absolute path when the service's `PATH` lacks it. A host without it (a daemon that reaches WebDriverAgent on another host) resolves `app: probe` to the ID it remembers from the first `ipad_launch` with the full bundle ID. |

Nothing is contacted at startup; an unplugged iPad only shows up when a tool is
called. `ipad_status` then names the missing layer and the command that brings it
up.

### Daemon on a Mac, iPad on another host

macOS 15's Local Network privacy stops a launchd-started, unsigned program from
connecting to LAN addresses: the daemon gets `no route to host` for the iPad
host's WDA even though `curl` in a shell reaches it. A grant in System Settings
would not last, because every update replaces the binary. Keep the daemon on
loopback (the default `ipad_wda_url`), which needs no permission, and let a
launchd socket forward it with Apple's `nc`, which is exempt. Save this as
`~/Library/LaunchAgents/com.teemow.ipad-wda-proxy.plist`, with `<ipad-host>`
being the machine running `scripts/ipad wda`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.teemow.ipad-wda-proxy</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/nc</string>
		<string><ipad-host></string>
		<string>8100</string>
	</array>
	<key>inetdCompatibility</key>
	<dict>
		<key>Wait</key>
		<false/>
	</dict>
	<key>Sockets</key>
	<dict>
		<key>Listeners</key>
		<dict>
			<key>SockNodeName</key>
			<string>127.0.0.1</string>
			<key>SockServiceName</key>
			<string>8100</string>
		</dict>
	</dict>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.teemow.ipad-wda-proxy.plist
curl -s http://127.0.0.1:8100/status    # WDA's status, through the proxy
```

launchd hands each connection to its own `nc`. A read of AUM takes about twice
as long this way (around 4 s). WDA has no authentication, and `ios forward`
listens on every interface of the iPad host, so anyone on that LAN can drive
the iPad.

## Tools

### Gestures

| Tool | What it does |
|---|---|
| `ipad_status` | WDA reachable/ready, iPadOS version, screen size and scale, foreground app, whether the iPad is locked; diagnoses the stack when WDA is down. Opens no WDA session. |
| `ipad_screenshot` | The screen as an image. One image pixel = one point by default, so pixel positions are tap coordinates; `region` crops (in points), `pixels_per_point: 2` gives Retina detail. |
| `ipad_ui_tree` | An app's accessibility tree, one line per labelled element with its frame; `all`, `contains`, `limit`. |
| `ipad_tap` | Tap by `label`, by `x`+`y`, or by a named AUM `target` (`tempo`, `rewind`, `play`, `engine_info`, `navigator`, `midi_routing`, `menu`, `keyboard`). `gesture: double` double-taps; `gesture: twice` taps twice 0.5 s apart in one session, which AUM's two-step buttons need. |
| `ipad_hold` / `ipad_swipe` | Long-press; drag as a quick touch (scroll AUM's mixer, faders, knobs). |
| `ipad_drag` | Press, move through several points (resting at each), hold, release — for gestures AUM reads only while the finger is down: reordering nodes and channels, moving plugin windows. |
| `ipad_text` | `fields` lists the text fields and their values; `type` clears one and types into it (`enter` presses Return); `enter` presses Return. |
| `ipad_launch` | Bring an app to the front, launching it when needed; a running app keeps its state. Without `app` it brings AUM back. |

These mirror auv3-probe's `scripts/ipad` (`tap-at`, `dtap`, `confirm`, `hold`,
`swipe`, `drag`, `type`, `enter`, `fields`) request for request.

### AUM operations

Each `aum_*` tool takes an `action` and runs it in one WDA session: it finds
its target in AUM's accessibility tree (by label where AUM labels it, by
shape or by the positions auv3-probe's `docs/aum-ui-map.md` measured
otherwise), sends the gestures, re-reads the tree and reports `verified`
with what it saw, the gestures it sent, and AUM's state. `verified: false` is
a normal result, not a tool error.

| Tool | Actions |
|---|---|
| `aum_panel` | `open` a `panel` (`menu`, `files`, `save`, `settings`, `midi_control`, `midi_routing`, `navigator`, `engine_info`, `tempo`, `clock_options`, `keyboard`, `notes`, `help`, `about`), `close` the front-most thing (an alert via Cancel, a popover by tapping outside it, the routing matrix, a side panel, the Navigator by a tap on the mixer, the tempo bar, the keyboard), `read` every open popover, panel and alert (`path` taps rows first), `set` a row of the front-most one (`setting`, `value`: Clock Options' Pre-roll and Sync Quantum, a mapping editor, a filter). |
| `aum_channel` | `add_audio`, `add_midi` (the add tile: right of the last strip, or in the middle of the start screen that CLEAR leaves), `restore` (the last removed channel), `import` (`name`: a session), `edit`, `rename` (`name`), `bookmark`, `duplicate`, `remove`¹, `move` (`target` strip), `set_level` (`level_db`: a tap on the dB label opens the Set level dialog), `reset_level` (double-tap the fader), `mute` / `solo` / `arm` (`on`, or flip), `scroll_to`. Whatever lies over the mixer is closed first, see [Touching the mixer under a panel](#touching-the-mixer-under-a-panel). |
| `aum_node` | `open`, `menu`, `select`, `replace`, `reload`, `pre_fader`, `post_fader`, `swap`, `copy_here` / `move_here` (an empty slot, after `select`), `add_slot`, `remove_slot`, `bypass` / `unbypass` (swipe), `eject`¹, `move` (`steps` slots), `meter` (main meter source), `name` (a bus or MIDI-bus node's short name), `window_close` / `window_mode` (the front plugin window). `node` is `source`, `output`, an insert slot number or a node label. |
| `aum_transport` | `state`, `play`, `stop`, `toggle`, `rewind`, `locate` (`bars`), `set_tempo` (`bpm`, through Edit Value), `nudge_tempo` (`steps` of 0.1 BPM), `metronome`, `record`¹ (starts, and on a second call stops, a take of every armed channel), `unarm_all`. |
| `aum_files` | `list` (FILES → Sessions: the whole folder `path` names, the root `/` without one, read page by page), `recordings` (FILES → Recordings; `path` into a session's folder; the page that shows), `load`¹ (`name` in the folder `path`: found through the whole list, then a tap on the session's row; see [Listing and loading sessions](#listing-and-loading-sessions)), `save`¹ (SAVE SESSION, and its OVERWRITE? for a name that exists; proved by the saved time of the session's row, see [Saving a session](#saving-a-session)), `rename`¹, `clear`¹ (CLEAR → Sure?), `notes`. |
| `aum_midi_routing` | `read` (sources, destinations and the connected cells), `connect` / `disconnect` (`source`, `destination`), `destination` (its panel: MIDI sources, All Notes Off, the filter; `path` taps further). |
| `aum_midi_control` | `read` (`path` walks into a collection or a control's mapping editor), `filter` (ALL, ASSIGNED, UNUSED), `learn`, `menu` (a section's `⋮`: Set MIDI Channels¹, Save, Load¹, Copy, Paste¹, Clear All¹, Delete All¹), `add` (ADD TEMPO, ADD PRESET), `main_parameter`. |
| `aum_settings` | `read` every setting (scrolling the panel), `set` a `setting` to a `value` as AUM prints it (ON, INT 24, 48 kHz, …). Settings are global: set them back afterwards. |
| `aum_file_player` | `open` / `read` (selected file, format, options), `set` (Loop, Sync, Normalize), `load` (`name`, `path` in SOUNDFILES), `play_enable`. |

¹ Changes or loses state: needs `confirm: true`. Iterate on a scratch session
or the ` (mcp test)` copies ([AGENTS.md](../AGENTS.md)), never an original.

### Touching the mixer under a panel

While a side panel is open, AUM does not pass touches on to the mixer beside
it. Seen on the iPad (AUM 1.4.8) on a session of one empty channel:

- a tap on the mixer closes the panel and does nothing else. Under the main
  menu and under FILES the tap on the dB label opened no Set level dialog;
  the same tap sent again, with the panel gone, opened it. Under the main
  menu a tap on MUTE did not mute;
- a swipe on the fader under the main menu left the level where it was, and
  the menu open.

This is the state the file operations leave: `aum_files load` ends with the
main menu open (it reads the session's name there) and `aum_files save` with
FILES open (it reads the session's row there). `set_level` right after a save
answered "the Set level dialog did not open", on a channel that had taken the
same call twice before the save.

So every `aum_channel` action first closes what lies over the mixer: a side
panel by its close icon, a popover by a tap outside it, an alert by Cancel,
the routing matrix by its top-bar button. The gestures are listed in the
result. An open EDIT CHANNEL stays for `edit`, `rename`, `bookmark`,
`duplicate` and `remove`, which work in it. The tempo bar and the built-in
keyboard are left alone: with either open a tap on the dB label opened the
dialog (the mixer shrinks above the keyboard).

With `ipad_tap`, `ipad_swipe` and `ipad_drag` on the mixer nothing is closed
for you: run `aum_panel close` first.

### Listing and loading sessions

`aum_files list` and `load` take `path`, the folders below the root of
Sessions (`["Songs"]`; none for the root, which AUM prints as `/`). Both open
FILES from the menu anew and walk to that folder from the root, as `save`
does: what FILES showed before, and where its list was scrolled to, makes no
difference ([FILES → Sessions, as the tool reads
it](#files--sessions-as-the-tool-reads-it)).

**`list` returns the whole folder.** Only the rows on screen can be read, so
the tool reads it a page at a time, from the top to the end, and returns
every row once, in the order AUM shows them (the sort picked above the list).
With the sort on last opened a session moves up when it is loaded, so the
order differs from one call to the next.
`data` is the entries: `name`, `detail` (for a session the time it was saved),
`folder: true` for a folder's row, and `frame`, which only the rows of the
last page carry: the others are no longer where they were read. The message
says "the whole list" and how many pages it took. A list that still goes on
after 60 pages (about 480 rows) comes back `verified: false`, "NOT the whole
list", with what was read.

It takes its time. On the iPad a page of eight rows took 3.7 to 3.9 s and the
end of a list two reads more, after 23 s to reach the folder (FILES from the
menu, up to the root, down two folders). A folder of 32 rows, four pages, took
44 s with the daemon on the iPad's host and 75 s with it on another machine.

**`load` finds the session wherever the folder lists it** and taps its row,
which loads it at once and replaces the open session. It taps

- only a session's row: a folder of that name has a chevron and is not it,
- only once the row is on screen whole: a row the list's edge cuts is read
  again on the next page, and a tap aimed at it could land beside the list.

A name the folder does not hold is an error that lists the folder, and
nothing is tapped. After the tap the tool waits for AUM to finish loading
and reads the session's name in the main menu: `verified: true` means the
menu names the session that was asked for.

`recordings` is not read this way: it lists the page of FILES → Recordings
that shows. Recordings names its folders as Sessions does: the title stays
`RECORDINGS` and a header row reads `/` or the folder's name. Its `path` is
still walked by the title, so each folder costs a wait of 5 s for a title
that does not change before its rows are listed (13 s for one folder on the
iPad). For the same reason a FILES that reopens inside a folder of Recordings
is taken for its root: `recordings` then lists that folder, or answers that
the folder of `path` is missing (to be fixed:
https://github.com/teemow/mcp-midi-controller/issues/199).

### Saving a session

`aum_files save` saves the open session under `name` and says whether AUM
wrote the file. What AUM 1.4.8 does, as seen on the iPad:

- AUM saves into the folder its SAVE panel shows (`/Songs` for a session
  loaded from there, `/` after CLEAR), under the name in the panel's field.
- **A name the folder does not hold** is written by the first press of SAVE
  SESSION.
- **A name it holds** makes the button read `OVERWRITE?` for a second or two
  (it still did 1.2 s after the press and no longer at 3.0 s). A press in
  that time writes the file. After it the button reads SAVE SESSION again,
  nothing was written, and the next press is a first one again. Return in the
  name field is a press of the button, with the same second.
- **After it has written, AUM closes the SAVE panel by itself.** After a
  press that wrote nothing the panel stays open.
- **The session's name in the main menu proves nothing.** Saving the open
  session over itself leaves it as it was, whether AUM wrote or not.

One read of AUM's tree takes 0.5 to 2 s, so reading the button's label and
then tapping comes too late. The tool does this instead:

1. It opens SAVE and reads the folder.
2. It reads the session's row in that folder of FILES → Sessions, going
   through the whole list. Now it knows whether the name exists.
3. It opens SAVE again, types `name` when the field holds another, and reads
   the field in the panel once more before it presses anything.
4. One press of SAVE SESSION for a new name; for one that exists both
   presses as one gesture, half a second apart (`ipad_tap gesture: twice`).
5. It waits for the panel to close, opens FILES again and reads the row.

**The proof is the row's saved time**: the second line of a session's row is
the time its file was written, to the second (`12. Nov 2026 at 21:38:05`).
`verified: true` means the time has moved, or that the row is new and the
panel closed. `data` holds `folder`, `existed`, `saved_before`, `saved_after`
and `panel_closed`. When the time has not moved the result is `verified:
false` and begins "NOT saved", with the time that stayed and what the button
read. Save again, and look at the SAVE panel (`aum_panel open save`,
`ipad_screenshot`) when it repeats.

The tool leaves these cases alone and says so, since pressing on would
overwrite something it has not seen:

- a name AUM asks to overwrite although the list did not show it,
- a list it could not read whole (below), which stops the save before
  anything is pressed.

A save takes 35 to 80 s, most of it the two readings of the list. From the
host the iPad is plugged into, `ios fsync --app=com.kymatica.AUM pull
--srcPath="/Documents/Songs/<name>.aumproj" --dstPath=<dir>` fetches the
session file for a look at its size and content.

#### FILES → Sessions, as the tool reads it

- Every folder of Sessions is titled `SESSIONS`. The folder is named in a
  header row above the list: `/` for the root, the folder's name inside one.
  A folder's row opens it and the back arrow goes up one folder. The house
  icon above the list goes to FILES, whose Sessions row opens the root.
- FILES opened from the menu shows the folder it showed last, from the top.
  A panel that is left open stays scrolled where it was.
- So `list`, `load` and `save` open FILES from the menu, go up to `/` and
  down the folder that was asked for (for `save` the one SAVE names),
  checking the header at each step, every time they read a list.
- A list shows twelve rows below the header and fourteen or fifteen further
  down, in the order picked above it (name, last saved, last opened). Its
  tree holds more cells than that, marked hidden: Recordings, just opened,
  had one for every row below the fold, and Sessions, scrolled to its end,
  kept those of the eighteen rows above, four of them with another row's
  name and saved time. So only the cells that show are read, a page at a
  time.
- The tool drags the list 420 points in 0.9 s for the next
  page, which moved it by 404 to 406 points: six rows are read twice. A drag
  of 584 points in 0.5 s ran on after the finger lifted (674 points) and
  carried a row past unread. A page that shares no row with the one before is
  therefore an error, not "no such session". A drag at the end of the list
  pulls it past its end and it springs back: the rows of the last page were
  read up to 9 points above where they came to rest, which a tap on the
  name, in a row of 51 points, still meets.
- A row cut by the list's edge shows one of its two lines, or both with a
  part of the row out of sight. A row counts once it lies inside the list
  and both lines can be read; until then it is neither listed nor tapped.
  (A row cut by the list's top edge can show its saved time alone, which
  would read as a name.)
- A folder's row carries a chevron at its right; a session's does not, and
  a tap on it loads that session at once. A folder and a session can share a
  name, so the tool taps only rows with the chevron on its way down, and
  reads the saved time, and loads, only from a row without one.
- The FILES root has a row "Restore Previous State". The tool taps rows by
  their text and never that one.

### Reading AUM

| Tool | What it does |
|---|---|
| `get_aum_ui` | AUM's state parsed from its UI: tempo, transport position, session name (while the main menu is open), DSP, main meter source, start screen / tempo bar / plugin windows, every open popover, side panel and alert (title, rows of text → value, buttons, text fields), and every strip with its number, title (custom titles too), fader dB, mute / solo / record-arm, its nodes (source, insert slots, output; empty or bypassed) and whether it is on screen. |
| `wait_for_aum_ui` | Re-read AUM (0.5–2 s per read, growing with the session) until expectations hold or `timeout_ms` passes: `tempo_bpm` / `not_tempo_bpm`, `session`, `transport` (`playing`/`stopped`), per-channel `level_db` / `not_level_db`, `muted`, `soloed`, `armed` and node `bypassed`, `strip_count`, `overlay` (a title) / `no_overlay`, `plugin_window`. |

How AUM shows state in its tree (AUM 1.4.8):

- **Mute, solo, record-arm**: the `MUTE`, `SOLO` and `REC` elements carry the
  accessibility trait *Selected* when lit. A muted strip's `MUTE` widens over
  `SOLO`, which then leaves the tree.
- **Strips** are found by shape (a full-height container with a wide title
  button at its bottom), so custom titles, which replace AUM's `n: <source>`,
  count too. Select them by part of the title or by position (`#3`).
- **Nodes**: a loaded node has an inner button (value `1`) over its square
  button; an empty slot (`+`) has none. A bypassed node slides about 55 points
  left of the strip's wire and leaves an eject `⏏` in its place.
- **Popovers** are elements of type `Popover` titled by their navigation bar
  (a long-press menu has none); **side panels** (menu, Settings, MIDI Control,
  FILES) are a full-height column at the right edge with a 60-point header,
  closed by the icon at its top right. A panel opened from the main menu is
  pushed over it: for a moment the menu's header stays in the tree, hidden
  and slid above the screen, ahead of the new one, so the title is the
  visible header at the panel's top edge. A closed menu lingers the same
  way: the first read after its close icon was tapped still has its page,
  hidden and slid out past the right edge. Either leftover carries the
  menu's `SESSION: …` line as well, so the menu counts as open, and the
  session name as on screen, only while the menu itself is the panel at the
  right edge. The Navigator has a navigation bar
  instead, and its top-right icon is the bookmark filter: it closes on a tap
  on the mixer, which AUM swallows. The Set level and Edit Value dialogs are
  alerts with a text field and Cancel / Set. Their field is filled without
  a tap and Set is pressed as an element: after a tap on the field (which
  brings up the on-screen keyboard), AUM ignored a tap on Set.
- **Plugin windows** have a 32-point title bar of buttons: the title
  `<plugin> @<node>`, followed by ` - <strip title>` when the strip has a
  custom title (`UA Battalion @A2 - Battalion`), then the window mode and ✕
  last. The window-mode menu marks the current mode with ✓ and the default
  one with ‧ (`MAXIMIZED ✓`, `DEFAULT ‧`). A side panel or popover opened
  over the window marks its bar hidden: `get_aum_ui` lists the window only
  while nothing covers its bar. Side panels open over a plugin window, so
  `aum_panel`, `aum_files` and `aum_midi_control` leave it open.
- **Under a side panel or the routing matrix** AUM marks the whole mixer
  invisible; its states still read. Its touches do not arrive ([Touching the
  mixer under a panel](#touching-the-mixer-under-a-panel)).
- **Unlabelled controls** carry labels iOS glues from the texts around them
  (`-∞, DSP:  2 %, -∞, Arp, Arp` on a MIDI node; the main meter's stale
  `SESSION: …` on a panel's back button); the parser strips those texts.
- **Steppers**: Clock Options' values (Time Signature, Pre-roll, Sync
  Quantum, …) sit between unlabelled `‹` and `›` buttons, and tapping the
  value itself does nothing. `set` steps `›`, then `‹`, and steps back to
  where the row was when it does not offer the value.
- **The add-channel tile** is an unlabelled 150-point square button in a
  column of its own right of the last strip; its AUDIO / MIDI / IMPORT menu
  stacks over it once tapped and closes by itself after a few seconds. While
  the menu is open the tile is a container of those three buttons.
- **The start screen** (a fresh AUM, or after CLEAR) has no strip. A mixer
  narrower than the window is centred, so the tile's column stands in the
  middle (the tile at 530,372), and LAST STATE and DEFAULT are in that column
  below the tile. The column is no strip, although DEFAULT sits where a
  strip's title would: `get_aum_ui` lists no channel there, and `add_audio`
  and `add_midi` tap the tile in the middle. With one channel the strip is
  at x 363 and the column at 605.
- **The tempo bar** shows the tempo as a button between `‹` and `›`; **the
  keyboard** takes the bottom of the window and the mixer shrinks above it (a
  strip is 596 points high instead of 764); it is closed with its `HIDE↓`
  button.
- **FILES** lists cells: a name and a detail line below it (for a session
  the time it was saved, to the second); a folder's cell has a chevron
  button as well. It reopens on the view last shown, and only the rows on
  screen can be read (the cells of the others are hidden, some with stale
  contents): `aum_files list`, `load` and `save` scroll a list of
  Sessions page by page, `recordings` reads the page that shows (see
  [FILES → Sessions, as the tool reads
  it](#files--sessions-as-the-tool-reads-it) for how Sessions names its
  folder).
- **MIDI routing connections** are not in the tree: `aum_midi_routing` reads
  them from a screenshot (a connected cell's fill is lighter).
- Opening something from the top bar (Navigator, Engine Info, routing, tempo
  bar, keyboard) first closes the panels and popovers in front: they cover
  the top bar, and a tap outside a popover only closes it.

The tools take an `app` argument: `aum` (default), `probe` (AUv3 Probe), or any
bundle ID. Every call opens a WDA session bound to that app, which brings it to
the front, and closes it again with the app left running and in front.

**Another app in front lets the iPad lock.** AUM keeps the iPad awake. Other
apps need not, and AUv3 Probe does not: left in front, the iPad locks itself
after its auto-lock time, and with a passcode set the rig is out of reach
until the code is entered on the device. So work in another app ends with
`ipad_launch` (no `app`), which brings AUM back. For as long as the last app
the daemon brought forward is not AUM, every one of these tools, `ipad_status`
and `ipad_screenshot` end their result with a note that says so: which app,
for how long, and how AUM comes back. `load_song` brings AUM back by itself
when its pull step fails with the probe app in front (`docs/songs.md`,
"Loading").

**A locked iPad** is named as that. The daemon asks WDA (`GET /wda/locked`,
no session): `ipad_status` reports it (`locked` in its structured result), and
a tool that fails on a locked iPad says "the iPad is locked" first and WDA's
own words after it ("Unable to launch … because the device was not, or could
not be, unlocked"). The daemon never calls WDA's unlock and never types a
code; the iPad is unlocked on the device.

**An app that went away** leaves the Home Screen in front, and that keeps the
iPad awake no more than another app does. WDA fails every further call of the
session that drove the app ("The application under test with bundle id '…' is
not running, possibly crashed"). A tool that gets this says what it means: the
app crashed or was closed, what it had open went with it (in AUM the session,
which has to be loaded again), the iPad is on its Home Screen and locks
itself, and `ipad_launch` without `app` starts AUM. The tool's gestures are
not made again. `ipad_status` and `ipad_screenshot` carry a note that the
Home Screen is in front until AUM is brought forward. Every session bound to
an app starts it when it is not running, so the next call that drives AUM
finds it running, with no session loaded. `load_song` goes further at its
load step: it starts AUM again and loads the session once more
(`docs/songs.md`, "Loading"). An app that is not up after its start fails the
new session instead ("Cannot launch … application"); what was in front before
still is, and that error is passed on as it is.

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
`session: "<name> (mcp test)"` after `switch_aum_session`, `muted: true`
after a CC mapped to a strip's Mute, a node's `bypassed` after its Bypass
mapping, `plugin_window` after Show & Front Plugin), and the `not_*` forms
when only a change is known (a CC's value curve into AUM's fader or tempo
range). AUM shows the session name only in its main menu, so open it first
(`aum_panel open menu`) when waiting for a session switch. The main meter's
accessibility label also starts with `SESSION: …`, but AUM does not refresh it
(it can still list channels of a session cleared long ago), so it is ignored.
`transport: "playing"` holds when the position advances between two reads.
Not matching is a normal result (`matched: false` plus what still differs), so
a silent mapping shows up as a clear "level still -1.0 dB".

What AUM's tree does **not** carry, and how to check it instead:

| State | Check with |
|---|---|
| Plugin parameters | the audio tap (`get_audio_tap`, `probe_sound`); plugin views are out-of-process |
| MIDI routing connections | `aum_midi_routing read` (from a screenshot) |
| Pre/post-fader wiring, the SELECT glow, the metronome | `ipad_screenshot` |
| MIDI Control mappings | `aum_midi_control read` with a `path`, or the session file (`get_aum_session`) |

## Care

- **One WDA session at a time.** WDA serves a single session; a new one ends the
  previous. The daemon serializes its own calls, but another WDA client (a second
  agent running `scripts/ipad tap`) and the daemon can cut each other off
  mid-action. Use one driver at a time; an `aum_*` operation holds its session
  for several seconds.
- **Taps can change or lose state.** CLEAR, a session row in FILES (loads it),
  SAVE SESSION, REMOVE CHANNEL, the eject `⏏`, MIDI Control's Clear All and
  Delete All, and record (writes files; deliberately not a named target of
  `ipad_tap`). The `aum_*` tools refuse these without `confirm: true`.
  Screenshot first near them. Per [AGENTS.md](../AGENTS.md), iterate on the
  ` (mcp test)` session copies, never on the originals.
- **Timing.** Each WDA action takes about 0.8 s. AUM's two-step buttons (CLEAR → Sure?, SAVE
  SESSION → OVERWRITE?, REMOVE CHANNEL → TAP TO CONFIRM) take the
  second tap only for about a second (`ipad_tap gesture: twice`), and the add
  tile's AUDIO / MIDI / IMPORT menu closes by itself after a few seconds, so
  `aum_channel` taps the item right after the tile, without a read in between.
- **Plugin windows can hang WebDriverAgent** (seen with an Apple AU effect's
  window open: tree reads time out, then every request bound to AUM hangs until
  `scripts/ipad wda` restarts). Close plugin windows (`aum_node window_close`)
  before reading the tree again.
- **Plugin windows slow every read.** A plugin's view adds its own elements
  to AUM's tree: with UA Battalion's window open one read took 12 to 15
  seconds (about 3 without a window), so an `aum_*` call takes tens of
  seconds. A wait re-reads at least once after its deadline, as one read can
  outlast it. Close the window when it is not needed.
- **Screenshots and trees are private.** They show session, plugin and device
  names. The daemon returns them to the caller and stores nothing; keep them out
  of this public repository. The captured trees under `internal/ipad/testdata`
  are trimmed and have the names of private sessions replaced.
- **Coordinates are points** in the app's current orientation. AUM rotates with
  the iPad: `get_aum_ui` reports the window and orientation; the named targets
  follow it. Settings → Show Status Bar moves AUM down by about 32 points.
