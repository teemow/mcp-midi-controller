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
