# Crafts run as subagents

Each craft runs as a craft agent of its own, a subagent named after the
person who practises it (`drummer`, `mixing_engineer`), so the agent's whole
context is the craft's judgement and not the conversation around it. The
agents ship as a Claude Code plugin from this repository, generated from
`docs/crafts/<craft>.md`, each with only its craft's tools. `produce_song` is a
skill in the same plugin that runs them one at a time and never writes the
song itself. A craft agent stores its own patches through `save_song` and
reports back; it may load, play and measure only the song it works on, with
the person's consent asked once by `produce_song`.

## Considered options

- **MCP prompts and a `get_craft` tool**: work in any MCP client, but a prompt
  is invoked by a person, and the craft's text lands in a context full of
  other work.
- **The daemon calls a model itself**: needs credentials and a tool loop in
  the daemon.

## Consequences

- Clients other than Claude Code get the craft tools, not the craft agents.
- The plugin's MCP config takes the daemon's URL from an environment variable,
  so the public repository names no host.
