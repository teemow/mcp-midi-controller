import { useEffect, useRef, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import type { LogEntry } from "../mcp/McpProvider";
import { Panel } from "../components/Panel";
import { Oscilloscope } from "../components/Oscilloscope";

const LOGGER_COLORS: Record<string, string> = {
  inbound: "text-cyan-glow",
  "auv3-probe": "text-magenta-glow",
  "aum-session": "text-yellow-300",
};

export function ActivityTab() {
  const { logs, clearLogs } = useMcp();
  const [filter, setFilter] = useState("");
  const [autoscroll, setAutoscroll] = useState(true);
  const endRef = useRef<HTMLDivElement | null>(null);
  const energy = logs.length;

  const visible = logs.filter(
    (l) => !filter || l.logger.includes(filter) || JSON.stringify(l.data).includes(filter),
  );

  useEffect(() => {
    if (autoscroll) endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [logs.length, autoscroll]);

  return (
    <Panel
      title="Activity"
      bodyClassName="flex flex-col gap-3"
      actions={
        <>
          <input
            className="field w-40 py-0.5 text-xs"
            placeholder="filter"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          <label className="flex items-center gap-1 text-[0.6rem] text-cyan-100/50">
            <input type="checkbox" checked={autoscroll} onChange={(e) => setAutoscroll(e.target.checked)} />
            follow
          </label>
          <button className="btn py-0.5" onClick={clearLogs}>
            clear
          </button>
        </>
      }
    >
      <div className="relative h-16 overflow-hidden rounded border border-cyan-500/20 bg-ink-900/60">
        <Oscilloscope energy={energy} className="absolute inset-0 h-full w-full" />
      </div>
      <div className="min-h-0 flex-1 overflow-auto rounded bg-ink-900/70 p-2 font-mono text-xs">
        {visible.length === 0 && (
          <p className="text-cyan-100/30">
            No events yet. Inbound MIDI, AUv3 probe arrivals, and AUM session uploads stream here.
          </p>
        )}
        {visible.map((l) => (
          <LogLine key={l.id} entry={l} />
        ))}
        <div ref={endRef} />
      </div>
    </Panel>
  );
}

function LogLine({ entry }: { entry: LogEntry }) {
  const color = LOGGER_COLORS[entry.logger] ?? "text-cyan-100/70";
  const time = new Date(entry.ts).toLocaleTimeString();
  return (
    <div className="flex gap-2 border-b border-cyan-500/5 py-0.5">
      <span className="shrink-0 text-cyan-100/30">{time}</span>
      <span className={`shrink-0 uppercase ${color}`}>{entry.logger}</span>
      <span className="break-all text-cyan-100/60">{render(entry.data)}</span>
    </div>
  );
}

function render(data: unknown): string {
  if (typeof data === "string") return data;
  try {
    return JSON.stringify(data);
  } catch {
    return String(data);
  }
}
