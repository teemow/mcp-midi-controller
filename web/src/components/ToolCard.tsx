import { useState } from "react";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { useMcp } from "../mcp/McpProvider";
import { ToolRunner } from "./ToolRunner";

// ToolCard is a collapsible panel wrapping one tool's runner. Defaults closed
// so a tab full of tools stays scannable.
export function ToolCard({ tool, open: openInit = false }: { tool: Tool; open?: boolean }) {
  const [open, setOpen] = useState(openInit);
  return (
    <div className="panel">
      <button
        className="flex w-full items-center justify-between gap-2 px-4 py-2 text-left"
        onClick={() => setOpen((o) => !o)}
      >
        <span className="font-mono text-sm text-cyan-glow">{tool.name}</span>
        <span className="text-cyan-100/30">{open ? "−" : "+"}</span>
      </button>
      {open && (
        <div className="border-t border-cyan-500/20 p-4">
          <ToolRunner tool={tool} />
        </div>
      )}
    </div>
  );
}

// ToolList renders a ToolCard for each tool whose name is in `names` (in that
// order) and that is currently registered. Missing tools are silently skipped,
// which is exactly how write-gated USB tools should behave: they only appear
// when the daemon has registered them.
export function ToolList({ names, openFirst }: { names: string[]; openFirst?: boolean }) {
  const { tools } = useMcp();
  const present = names
    .map((n) => tools.find((t) => t.name === n))
    .filter((t): t is Tool => Boolean(t));

  if (present.length === 0) {
    return <p className="text-xs text-cyan-100/40">None of these tools are currently available.</p>;
  }

  return (
    <div className="flex flex-col gap-2">
      {present.map((t, i) => (
        <ToolCard key={t.name} tool={t} open={openFirst && i === 0} />
      ))}
    </div>
  );
}

// ToolListByPrefix renders every registered tool whose name starts with any of
// the given prefixes (sorted). Used for dynamic per-device tool families (e.g.
// usb_<logical>_* editor/readback tools).
export function ToolListByPrefix({
  prefixes,
  exclude = [],
}: {
  prefixes: string[];
  exclude?: string[];
}) {
  const { tools } = useMcp();
  const present = tools
    .filter((t) => prefixes.some((p) => t.name.startsWith(p)) && !exclude.includes(t.name))
    .sort((a, b) => a.name.localeCompare(b.name));

  if (present.length === 0) {
    return <p className="text-xs text-cyan-100/40">No matching tools registered.</p>;
  }

  return (
    <div className="flex flex-col gap-2">
      {present.map((t) => (
        <ToolCard key={t.name} tool={t} />
      ))}
    </div>
  );
}
