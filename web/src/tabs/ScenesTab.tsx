import { useEffect, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { resultText, structured } from "../mcp/result";
import { Panel } from "../components/Panel";
import { ToolList } from "../components/ToolCard";

export function ScenesTab() {
  const { callTool, status } = useMcp();
  const [msg, setMsg] = useState<string | null>(null);

  const scenes = useAsync(async () => {
    const r = await callTool("list_scenes");
    return structured<{ scenes: string[] }>(r)?.scenes ?? [];
  });
  const recall = useAsync(async (name: string, mode: string) => {
    const r = await callTool("recall_scene", { name, mode });
    setMsg(resultText(r));
    return r;
  });

  useEffect(() => {
    if (status === "ready") void scenes.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <Panel
        title="Saved scenes"
        actions={
          <button className="btn" disabled={scenes.loading} onClick={() => void scenes.run()}>
            refresh
          </button>
        }
      >
        {scenes.error && <p className="text-xs text-magenta-glow">{scenes.error}</p>}
        {(scenes.data?.length ?? 0) === 0 && !scenes.loading && (
          <p className="text-xs text-cyan-100/40">No scenes saved yet.</p>
        )}
        <div className="flex flex-col gap-2">
          {scenes.data?.map((name) => (
            <div key={name} className="flex items-center justify-between rounded border border-cyan-500/20 bg-ink-900/40 p-2.5">
              <span className="font-mono text-sm text-cyan-100">{name}</span>
              <div className="flex gap-1.5">
                <button className="btn py-0.5" disabled={recall.loading} onClick={() => void recall.run(name, "additive")}>
                  recall
                </button>
                <button className="btn btn-magenta py-0.5" disabled={recall.loading} onClick={() => void recall.run(name, "exact")}>
                  recall exact
                </button>
              </div>
            </div>
          ))}
        </div>
        {msg && <p className="mt-3 text-xs text-cyan-100/50">{msg}</p>}
        {recall.error && <p className="mt-2 text-xs text-magenta-glow">{recall.error}</p>}
      </Panel>

      <Panel title="Scene operations">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Snapshot the current desired-state, capture a USB patch into a scene, and compile/push to the
          footswitch. For export, set <span className="text-cyan-glow">dry_run</span> (or omit footswitch) to
          preview the compiled JSON before pushing.
        </p>
        <ToolList
          names={["save_scene", "capture_usb_patch", "export_scene_to_footswitch"]}
        />
      </Panel>
    </div>
  );
}
