import { useMemo, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import { Panel } from "../components/Panel";
import { ToolCard } from "../components/ToolCard";

// ToolsTab is the generic explorer/tester: every registered tool, filterable,
// each rendered as a schema-driven form via ToolCard/ToolRunner.
export function ToolsTab() {
  const { tools, refreshTools } = useMcp();
  const [filter, setFilter] = useState("");

  const filtered = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return [...tools]
      .filter(
        (t) =>
          !f ||
          t.name.toLowerCase().includes(f) ||
          (t.description ?? "").toLowerCase().includes(f),
      )
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [tools, filter]);

  return (
    <Panel
      title={`All tools (${tools.length})`}
      bodyClassName="flex flex-col gap-3"
      actions={
        <>
          <input
            className="field w-48 py-0.5 text-xs"
            placeholder="filter tools"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          <button className="btn py-0.5" onClick={() => void refreshTools()}>
            refresh
          </button>
        </>
      }
    >
      {filtered.length === 0 && <p className="text-xs text-cyan-100/40">No matching tools.</p>}
      <div className="flex flex-col gap-2">
        {filtered.map((t) => (
          <ToolCard key={t.name} tool={t} />
        ))}
      </div>
    </Panel>
  );
}
