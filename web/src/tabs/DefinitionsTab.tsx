import { useEffect, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { structured } from "../mcp/result";
import type { DefinitionSummary, DefinitionView } from "../mcp/types";
import { Panel } from "../components/Panel";
import { ToolList } from "../components/ToolCard";

export function DefinitionsTab() {
  const { callTool, status } = useMcp();
  const [selected, setSelected] = useState<string | null>(null);

  const list = useAsync(async () => {
    const r = await callTool("list_definitions");
    return structured<{ definitions: DefinitionSummary[] }>(r)?.definitions ?? [];
  });
  const detail = useAsync(async (id: string) => {
    const r = await callTool("get_definition", { id });
    return structured<DefinitionView>(r);
  });

  useEffect(() => {
    if (status === "ready") void list.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  const open = (id: string) => {
    setSelected(id);
    void detail.run(id);
  };

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
      <Panel
        title="Definitions"
        className="lg:col-span-1"
        actions={
          <button className="btn" disabled={list.loading} onClick={() => void list.run()}>
            refresh
          </button>
        }
      >
        {list.error && <p className="text-xs text-magenta-glow">{list.error}</p>}
        <div className="flex flex-col gap-1.5">
          {list.data?.map((d) => (
            <button
              key={d.id}
              onClick={() => open(d.id)}
              className={`rounded border px-3 py-2 text-left transition ${
                selected === d.id
                  ? "border-cyan-glow/60 bg-cyan-glow/10"
                  : "border-cyan-500/15 bg-ink-900/40 hover:border-cyan-glow/40"
              }`}
            >
              <div className="font-mono text-sm text-cyan-100">{d.name}</div>
              <div className="mt-1 flex flex-wrap gap-1 text-[0.6rem]">
                <span className="tag">{d.id}</span>
                <span className="tag">{d.transport}</span>
                <span className="tag">{d.controls} ctl</span>
                {d.usb && <span className="tag border-magenta-glow/40 text-magenta-glow">usb</span>}
              </div>
            </button>
          ))}
        </div>
      </Panel>

      <Panel title="Detail" className="lg:col-span-2">
        {!selected && <p className="text-xs text-cyan-100/40">Select a definition.</p>}
        {detail.loading && <p className="text-xs text-cyan-100/40">loading…</p>}
        {detail.data && <DefinitionDetail def={detail.data} />}
      </Panel>

      <Panel title="Author a definition" className="lg:col-span-3">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Start a draft, add controls one by one, then save to hot-load it into the registry.
        </p>
        <ToolList names={["create_device_definition", "add_control", "save_device_definition"]} />
      </Panel>
    </div>
  );
}

function DefinitionDetail({ def }: { def: DefinitionView }) {
  return (
    <div className="flex flex-col gap-3">
      <div>
        <h3 className="text-lg text-cyan-glow">{def.name}</h3>
        <div className="mt-1 flex flex-wrap gap-1.5 text-[0.65rem]">
          <span className="tag">{def.id}</span>
          {def.manufacturer && <span className="tag">{def.manufacturer}</span>}
          <span className="tag">{def.transport}</span>
          {def.usb && <span className="tag border-magenta-glow/40 text-magenta-glow">usb</span>}
        </div>
        {def.description && <p className="mt-2 text-xs text-cyan-100/50">{def.description}</p>}
      </div>
      <div className="overflow-auto">
        <table className="w-full border-collapse text-left text-xs">
          <thead>
            <tr className="text-[0.6rem] uppercase tracking-[0.2em] text-cyan-100/40">
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">control</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">type</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">address</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">value spec</th>
            </tr>
          </thead>
          <tbody>
            {def.controls.map((c) => (
              <tr key={c.name} className="align-top">
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 font-mono text-cyan-100">
                  {c.name}
                  {c.description && <div className="text-[0.6rem] text-cyan-100/40">{c.description}</div>}
                </td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/70">{c.type}</td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/60">{addressOf(c)}</td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/60">{specOf(c)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function addressOf(c: DefinitionView["controls"][number]): string {
  if (c.cc !== undefined) return `cc ${c.cc}`;
  if (c.nrpn !== undefined) return `nrpn ${c.nrpn}`;
  if (c.program !== undefined) return `pgm ${c.program}`;
  if (c.sysex) return "sysex";
  if (c.address) return c.address;
  return "—";
}

function specOf(c: DefinitionView["controls"][number]): string {
  const v = c.value;
  if (v.values && Object.keys(v.values).length > 0) {
    return `enum {${Object.keys(v.values).join(", ")}}`;
  }
  const parts: string[] = [];
  if (v.min !== undefined || v.max !== undefined) {
    parts.push(`${v.min ?? "?"}..${v.max ?? "?"}`);
  }
  if (v.unit) parts.push(v.unit);
  return parts.join(" ") || v.type || "—";
}
