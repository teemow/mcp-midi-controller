# ADR 0004: Crafts run as subagents, not as MCP prompts

- **Status:** accepted
- **Date:** 2026-11-13, in the design review of
  [craft-tools.md](../craft-tools.md)
  (https://github.com/teemow/mcp-midi-controller/pull/224)
- **Epic:** https://github.com/teemow/mcp-midi-controller/issues/225
- **Terms:** [GLOSSARY.md](../../GLOSSARY.md)

## Context

The first design gave each craft an MCP prompt (`producer`, `composer`, …),
and `produce_song` was a prompt that ran the crafts in order. A prompt is
text added to the one conversation that does all the work. That
conversation would then hold the judgement of every craft, every craft's
tools, and the song's whole history. Every connected agent also reads the
daemon's whole list of tools: more than 110, and the craft tools add about
23.

A subagent, by contrast, has a context of its own, its own instructions, and
a list of the tools it may use. It reports back to whoever started it.

## Decision

- **One craft, one agent.** Each craft is a *craft agent*, named after the
  person who does the work. Each craft also has one id, which names its text,
  its technique cards and its lens.

  | Craft id | Agent |
  |----------|-------|
  | `production` | `producer` |
  | `composition` | `composer` |
  | `drums` | `drummer` |
  | `sound_design` | `sound_designer` |
  | `orchestration` | `conductor` |
  | `mixing` | `mixing_engineer` |
  | `mastering` | `mastering_engineer` |

- **A Claude Code plugin.** The agents ship as a Claude Code plugin from this
  repository. Their definitions are generated at build time from
  `docs/crafts/<craft>.md`, the same text the web UI's docs tab shows. Each
  agent gets only its craft's tools. The plugin's MCP config takes the
  daemon's URL from an environment variable, so no host is written into the
  repository.
- **What an agent may do.** A craft agent stores its own patches with
  `apply: true`, through `save_song`. It loads, plays and measures only the
  song it works on. When it is started alone, it asks the person for consent
  to load. When `produce_song` starts it, its task carries that consent.
- **What an agent reports.** The revision before and after, what it changed
  and why, what it checked, the findings it left open, and what it hands to
  which craft.
- **`produce_song` is a skill** in the plugin. It runs the craft agents one at
  a time, in order: producer, composer, drummer, sound designer, conductor,
  producer, mixing engineer, producer, mastering engineer. It asks the person
  once for consent to load the song's session. It passes each agent's
  hand-over to the next agent's task. It never writes the song itself.
- **No MCP prompts.** The daemon serves tools only.

## Consequences

- Each craft works with only its own judgement and its own tools in view, and
  the conversation that starts it receives a report, not the craft's whole
  transcript.
- The craft's text has one source. People read it in the docs tab, and
  agents are generated from it.
- The craft tools stay MCP tools, which any client can call. The agents and
  `produce_song` need Claude Code. Another client uses the tools, and can
  read `docs/crafts/<craft>.md` for itself.
- The agents run one at a time, so no two of them patch a song at once.
  Revisions ([ADR 0002](0002-patches-by-id-with-a-revision.md)) catch a
  person's edit in between.
- A craft's tools land before its agent. The first agent is
  `mastering_engineer`.
