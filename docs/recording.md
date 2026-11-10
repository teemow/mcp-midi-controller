# Recording and Google Drive

The daemon can write what an audio tap hears into a file, and put that file
into your Google Drive. Together with [songs](songs.md) that closes the loop:
an agent writes a song, plays it, and hands you a link to listen to.

| Step | Tool | |
|------|------|-|
| Record a song | `play_song` with `record: true` | Starts the recording, plays the song, and ends the recording once the song is over and its tail has rung out. |
| Record anything else | `start_recording`, `stop_recording` | From now until stopped, or for a number of `seconds`. |
| Look | `list_recordings` | The one running and the finished ones: file, length, levels, Drive link. |
| Sign in, once | `connect_google_drive`, `get_google_drive` | See [Google Drive](#google-drive). |
| Upload | `upload_recording` | Returns the file's link. |

## Recording

A ProbeAudioTap streams its strip's audio to the daemon: every sample, as
32-bit float at the session's rate. A recording writes that stream to disk as
it arrives. Nothing is fetched in pieces and joined afterwards, so a recording
has no seams.

`play_song` with `record: true` records the tap on the song's master strip:

- The recording starts before the opening sound is set and follows the song.
  When the first note goes out, whatever the tap heard until then (a tail still
  ringing from the last playback, a preset loading) stops counting as music.
  A song played by the sequencer in its session sends no note through the
  daemon; there the same happens shortly before the lead-in ends.
- After the last bar it waits for the tail: until the tap has been silent for
  1.5 seconds, for 10 seconds at most.
- `stop_song` ends the song and, with it, the recording. `stop_recording` ends
  only the recording.
- The master's tap is found by its name, `<session id>/master`, where the
  session's taps dial in under their names (see [songs.md](songs.md), "Taps").
  A tap that gives no name is not told from another: the recording then takes
  the most recently connected tap, which in a session with `track_taps` may be
  a track's.

`start_recording` records on its own: `seconds` sets a length (the recording is
then exactly that long), otherwise it runs until `stop_recording`, for 30
minutes at most. `tap` picks a tap by name when several are streaming. One
recording runs at a time.

### The cut

When a recording ends, the silence around the sound is cut (`trim: false` keeps
everything):

- It begins 20 ms before the first sample that reaches the silence level.
- It ends one second after the last such sample, and that second is faded
  out. A reverb tail drops under the silence level long before it is gone; the
  fade lets it end instead of stopping short, and touches nothing louder.
- A recording that was still sounding when it ended (stopped by hand, or a
  tail longer than the wait) is faded over its last 400 ms.

The silence level is -60 dBFS (`silence_db`). A session with a noise floor
above that never falls silent for the recorder: raise the level, or the
lead-in stays and the tail runs to its limit.

### Formats

| | | Why |
|-|-|-----|
| `flac` (default) | 24 bit, lossless | About two thirds the size of the same audio as PCM, tagged with its title and date, and what a music library or a DAW takes as a master. Samples over full scale are stored at full scale; the record counts them. |
| `wav` | 32-bit float | The stream's own samples, bit for bit, levels over full scale included. For when the capture itself is wanted. |

Both are written by the daemon itself, so a recording needs no program on the
host. That is also why there is no MP3 or AAC: an encoder for either is a
program the host may not have (`lame`, `ffmpeg`), or a library under a
copyleft licence.

### What is kept with a recording

Each recording has a record next to it (`<name>.json`), and `list_recordings`
shows it:

- length, sample rate, channels, size;
- peak and mean level in dBFS, over all channels;
- what was cut before the first sound and after the last, and the fade;
- **stalls**: every place where the tap's stream arrived more than 200 ms
  late, or the tap dropped and dialled in again. A recording without stalls
  is the stream as the tap sent it. Where there are stalls, audio may be
  missing: the tap drops what it cannot send. The record holds how much audio
  arrived and how long that took, and `list_recordings` says whether the take
  is whole. A busy Wi-Fi is the usual cause; a host that dozes would be
  another, so the daemon holds its host awake while it records;
- the Google Drive link, once uploaded.

A recording is named after its song (or the `name` given) and the time it was
started: `Night Drive 2026-11-10 1347.flac`.

## Google Drive

`upload_recording` puts a recording into your Drive and returns its link. The
file is private to you until you share it.

The daemon signs in with an OAuth client that is **yours**: created in your
own Google Cloud project, read from a file on the daemon's host. It asks for
one scope, `drive.file`: it can create files and see the files it created, and
nothing else in your Drive.

### Set it up, once

`scripts/setup-google-drive.sh` walks through it: it opens each page of the
Google Cloud console, says what to click, and installs the client file where
the daemon reads it (on this machine, or on the host the daemon runs on):

```bash
scripts/setup-google-drive.sh              # the daemon runs here
scripts/setup-google-drive.sh my-rig-host  # the daemon runs there (an ssh host)
```

By hand, the same steps:

1. **A project.** <https://console.cloud.google.com/projectcreate>, signed in
   with the account whose Drive the recordings go to. No billing is needed.
2. **The API.** Enable the Google Drive API for it:
   <https://console.cloud.google.com/apis/library/drive.googleapis.com>.
3. **The app.** <https://console.cloud.google.com/auth/overview> → *Get
   started*: an app name and your address, audience *External*, your address as
   contact, agree, *Create*. No logo and no scopes.
4. **Let yourself in.** A new app is in "Testing", and only its test users
   may sign in: <https://console.cloud.google.com/auth/audience> → *Test
   users* → *Add users* → your address → *Save*. See
   [Testing or published](#testing-or-published) for what that status costs.
5. **The client.** <https://console.cloud.google.com/auth/clients> → *Create
   client* → application type **TVs and Limited Input devices** → *Create* →
   *Download JSON*. Google shows the client secret only at this point.
6. **Install it.** Save the JSON as
   `$XDG_CONFIG_HOME/mcp-midi-controller/google-drive-client.json` on the
   daemon's host, readable by its owner only (`chmod 600`). If the config dir
   is a git repository, keep this file out of it.

### Testing or published

In "Testing", Google ends a sign-in seven days after it was approved. The
daemon then says it is no longer connected, and `connect_google_drive` has to
be run and approved again: once a week.

*Publish app* on the Audience page lifts that. For a new app on a personal
account the console did not offer it: the button stays disabled until the
Branding page is complete. What Google asks of an app open to accounts outside
an organisation, and what verification follows, is in its own documentation:
[Manage OAuth App Branding](https://support.google.com/cloud/answer/15549049)
and [Manage App Audience](https://support.google.com/cloud/answer/15549945).

A project that belongs to a Google Workspace organisation can make the app
*Internal* instead, which has no seven-day limit; a personal account cannot.

### Sign in, once

The daemon has no browser, so it signs in the way a TV does:

1. `connect_google_drive` returns a link and a short code.
2. Open the link on any device, enter the code, pick the account and approve.
   While the app is in "Testing", Google first says it has not verified the
   app: continue, it is yours. Then it names your app and what it asks for: to
   see, edit, create and delete only the Drive files used with this app.
3. `get_google_drive` says when the sign-in has arrived, and whose Drive it is.

The code is valid for 30 minutes. The sign-in lasts until you end it (or, in
"Testing", for seven days): `connect_google_drive` with `disconnect: true`
revokes it at Google and deletes it on the host. You can also remove the
app's access in your Google account; the daemon notices at its next call and
asks to be connected again.

### Upload

`upload_recording` sends the newest recording, or the one named. The upload is
resumable: the file goes up in 8 MiB pieces, and after a failed request the
daemon asks Drive how much arrived and carries on from there. At the end the
file's checksum in the Drive is compared with the local one.

- `folder` in the config is where files go; `folder` on the call overrides it.
  With `drive.file` the daemon only sees folders it made itself, so it creates
  the folder on first use, and a folder of the same name that you made by hand
  is not found. The daemon remembers the folder it made: rename it or move it
  anywhere in your Drive and uploads keep going into it. Once it is in the
  bin (or deleted), the next upload goes to a folder of the configured name
  where the config says: one the daemon made there before, or a new one.
- A recording that is already in the Drive is not sent again (`again: true`
  makes a second file).
- `name` sets the file's name in the Drive; by default it is the recording's.

An upload happens when it is asked for: `upload_recording`, or `upload: true`
on `play_song` or `start_recording`, which uploads that recording when it is
finished. With `auto_upload` in the config every recording of a song is
uploaded by itself.

### Where the secrets are

| | Where | |
|-|-------|-|
| OAuth client (id and secret) | `google-drive-client.json` in the config dir | Yours to create and to keep; never part of this repository. |
| Sign-in (refresh and access token) | `google-drive-token.json` in the state dir | Written by the daemon, mode 0600. |

Neither is ever logged or returned by a tool. A tool shows the sign-in link,
the code you type, and the account that is signed in.

## Configuration

In `config.yaml`:

```yaml
recordings_keep: 50      # recordings kept in the state dir; older ones are deleted (-1: all)

google_drive:
  folder: Songs          # Drive folder for uploads; "A/B" for a folder inside another. Unset: the top level
  auto_upload: false     # upload every recording of a song once it is finished
```

The config is read when a tool needs it; changing it needs no restart.

| | Where |
|-|-------|
| Recordings | `recordings/` in the state dir: `<name>.flac` or `.wav`, and `<name>.json` |
| OAuth client | `$XDG_CONFIG_HOME/mcp-midi-controller/google-drive-client.json` |
| Sign-in | `google-drive-token.json` in the state dir |
| The upload folders the daemon made | `google-drive-folders.json` in the state dir |

Recordings are a private rig signal, like every captured audio: they live in
the state dir and are never committed.

## Status

The test suite covers:

- the capture: every sample in order across far more audio than the rolling
  window holds, the first and last sound, stalls and reconnects, the length
  limit;
- the files: FLAC decoded again and compared sample by sample with what went
  in (and run through the reference decoder where the host has one), the WAV
  header and its samples bit for bit, the cut and both fades, the levels;
- the tools: a recording on its own, a timed one, a song recorded through
  `play_song` (the lead-in cut at the first note, the tail waited for), a
  stopped song, the retention;
- Google Drive against a stand-in server that keeps Google's rules (a valid
  access token on every call, chunks in multiples of 256 KiB): the sign-in
  approved, declined and expired, an access token replaced when it has run out,
  a revoked sign-in noticed, the folder made once and found again (also after
  it was renamed and moved, and not after it went into the bin), uploads
  through server errors, rate limits, a connection dropped mid-chunk or gone
  dead, a chunk taken only in part and a session Drive forgot, and that no
  token, client secret or device code appears in a tool's answer, an error or
  the log.

Checked on a real Google account (a personal one, the app in "Testing"):

- the sign-in with a client of the type "TVs and Limited Input devices" and
  the `drive.file` scope: the code entered, the account chosen, the access
  approved, the sign-in arrived at the daemon;
- `get_google_drive` naming the account (Drive's `about` answers with this
  scope);
- the upload folder made on first use;
- a 28.8 MB FLAC (a five-track song of two minutes) uploaded in four pieces in
  11 seconds, its checksum in the Drive equal to the local one, the link
  opening the file in Drive with Drive's own audio player.

Not checked on a real account yet: the upload folder found again by its id
after a rename or a move, an access token replaced after its hour, an upload
resumed after a real connection loss, a sign-in ended by Google after seven
days, and a published app.
