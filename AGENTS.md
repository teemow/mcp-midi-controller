# Agent instructions

## AUM sessions: iterate on copies, never on the originals

The author→sync→load workflow (staging mirror, auto-download, auto-import,
session switching) is not proven end-to-end yet. Until it is, the user's
original `.aumproj` sessions are read-only masters:

- Feature implementations and tests operate **only** on the ` (mcp test)`
  copies in the session staging dir, never on an original session id.
- Copies are plain byte-identical file copies generated from the originals;
  regenerate them when an original changes or a copy needs a clean reset.
- The session-switch registry (the session load list) pins the copies; the
  user is instructed to wire AUM's "Session Load" actions to the copy files.

Process details: `docs/aum-brain-control.md` ("Testing process — iterate on
copies"). The rig-specific list of sessions and copy ids is private:
`docs/private/aum-projects.md`.

## Private docs live in their own repository

`docs/private/` is gitignored here because this repository is public. It is a
clone of the private repository `teemow/mcp-midi-controller-private`: clone it
there, and commit rig snapshots, captures and inspection data to it (through a
PR), never to this repository. Work left only in a local `docs/private/` is lost
with the machine.

## A locked iPad: a person runs `scripts/ipad unlock`

The iPad locks itself when an app other than AUM is left in front (AUv3 Probe
does not keep it awake), and the iPad tools then say "the iPad is locked"
(`docs/ipad-control.md`). Only its passcode unlocks it, and only a person types
it. Ask them to run auv3-probe's
[`scripts/ipad unlock`](https://github.com/teemow/auv3-probe/blob/main/scripts/ipad-unlock)
in a terminal on the host the iPad is plugged into (the one running
`scripts/ipad wda`), for example in a terminal tab they can type into;
`--dry-run` first shows that the prompt and its keys are found. Its use is
described in auv3-probe's `docs/ipad-automation.md`, "When the iPad has locked
itself". The script takes the iPad lock `~/.cache/go-ios/ipad.lock` itself and
refuses while anyone holds it, so release yours before it runs. Never ask for
the code in chat, never type, store or pass it, and never run the script a
second time after it reports the code was not accepted: iPadOS counts wrong
codes and locks the device out.

## The Mac follows main by itself

The rig's macOS host runs the daemon under launchd and updates itself from
`main` within about 10 minutes of a merge while it is awake
(`scripts/deploy-mac.sh --update` every 5 minutes on the clock and once on
wake; it sleeps when idle, so a merge can wait for the next wake; see the
README's macOS section). Don't build or copy binaries onto it by hand: merge to
`main`, then confirm the version on the first line of
`~/Library/Logs/mcp-midi-controller.log`. If it doesn't move, read
`~/Library/Logs/mcp-midi-controller-update.log` (a clone with local changes or
on another branch, or a failed build, stops the update).
