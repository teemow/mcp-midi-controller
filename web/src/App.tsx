import { useEffect, useRef, useState } from "react";
import { useMcp } from "./mcp/McpProvider";
import { Oscilloscope } from "./components/Oscilloscope";
import { DevicesTab } from "./tabs/DevicesTab";
import { DefinitionsTab } from "./tabs/DefinitionsTab";
import { ControlTab } from "./tabs/ControlTab";
import { WidiTab } from "./tabs/WidiTab";
import { ScenesTab } from "./tabs/ScenesTab";
import { UsbTab } from "./tabs/UsbTab";
import { IpadTab } from "./tabs/IpadTab";
import { ActivityTab } from "./tabs/ActivityTab";
import { ToolsTab } from "./tabs/ToolsTab";
import { DocsTab } from "./tabs/DocsTab";

const TABS = [
  { id: "devices", label: "Devices", el: <DevicesTab /> },
  { id: "definitions", label: "Definitions", el: <DefinitionsTab /> },
  { id: "control", label: "Control", el: <ControlTab /> },
  { id: "widi", label: "WIDI", el: <WidiTab /> },
  { id: "scenes", label: "Scenes", el: <ScenesTab /> },
  { id: "usb", label: "USB", el: <UsbTab /> },
  { id: "ipad", label: "iPad", el: <IpadTab /> },
  { id: "activity", label: "Activity", el: <ActivityTab /> },
  { id: "tools", label: "Tools", el: <ToolsTab /> },
  { id: "docs", label: "Docs", el: <DocsTab /> },
] as const;

type TabId = (typeof TABS)[number]["id"];

export function App() {
  const { status, error, serverInfo, logs, reconnect, tools } = useMcp();
  const [active, setActive] = useState<TabId>("devices");

  // Pulse the header oscilloscope whenever a new log arrives.
  const [energy, setEnergy] = useState(0);
  const lastLen = useRef(0);
  useEffect(() => {
    if (logs.length > lastLen.current) setEnergy((e) => e + 1);
    lastLen.current = logs.length;
  }, [logs.length]);

  return (
    <div className="mx-auto flex h-full max-w-[1400px] flex-col p-4">
      <header className="panel relative mb-3 overflow-hidden">
        <Oscilloscope energy={energy} className="absolute inset-0 h-full w-full opacity-40" />
        <div className="relative flex items-center justify-between gap-4 px-5 py-4">
          <div>
            <h1 className="text-2xl font-bold uppercase tracking-[0.4em] text-cyan-glow drop-shadow-[0_0_8px_rgba(34,211,238,0.6)]">
              signalwave
            </h1>
            <p className="mt-1 text-[0.65rem] uppercase tracking-[0.3em] text-cyan-100/40">
              mcp-midi-controller · in-browser mcp client
            </p>
          </div>
          <div className="flex flex-col items-end gap-1 text-xs">
            <ConnIndicator status={status} onReconnect={reconnect} />
            {serverInfo && (
              <span className="text-cyan-100/40">
                {serverInfo.name} v{serverInfo.version}
              </span>
            )}
            <span className="text-cyan-100/30">{tools.length} tools</span>
          </div>
        </div>
      </header>

      {error && status === "error" && (
        <div className="mb-3 rounded border border-magenta-glow/40 bg-magenta-glow/5 px-4 py-2 text-xs text-magenta-glow">
          connection error: {error}
        </div>
      )}

      <nav className="mb-3 flex flex-wrap gap-1.5">
        {TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setActive(t.id)}
            className={`rounded border px-3 py-1.5 text-xs uppercase tracking-[0.2em] transition ${
              active === t.id
                ? "border-cyan-glow/70 bg-cyan-glow/10 text-cyan-glow shadow-glow-cyan"
                : "border-cyan-500/20 bg-ink-800/60 text-cyan-100/50 hover:border-cyan-glow/40 hover:text-cyan-100"
            }`}
          >
            {t.label}
          </button>
        ))}
      </nav>

      <main className="min-h-0 flex-1 overflow-auto pb-4">
        {TABS.map((t) => (
          <div key={t.id} className={active === t.id ? "block h-full" : "hidden"}>
            {t.el}
          </div>
        ))}
      </main>
    </div>
  );
}

function ConnIndicator({ status, onReconnect }: { status: string; onReconnect: () => void }) {
  const map: Record<string, { dot: string; label: string }> = {
    connecting: { dot: "bg-yellow-400 animate-pulse", label: "connecting" },
    ready: { dot: "bg-cyan-glow shadow-glow-cyan", label: "online" },
    error: { dot: "bg-magenta-glow", label: "offline" },
  };
  const m = map[status] ?? map.error;
  return (
    <span className="flex items-center gap-2 uppercase tracking-[0.2em]">
      <span className={`h-2 w-2 rounded-full ${m.dot}`} />
      <span className="text-cyan-100/60">{m.label}</span>
      {status === "error" && (
        <button className="btn btn-magenta ml-1 py-0.5" onClick={onReconnect}>
          retry
        </button>
      )}
    </span>
  );
}
