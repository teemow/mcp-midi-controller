import { useMemo, useState } from "react";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { SchemaForm } from "./SchemaForm";
import { ResultView } from "./ResultView";
import { defaultForSchema, type JsonSchema } from "./schema";

// initialArgs seeds form values from const-pinned properties so the call
// carries them even though they are hidden in the form.
function initialArgs(schema: JsonSchema | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const props = schema?.properties ?? {};
  for (const [k, v] of Object.entries(props)) {
    if (v.const !== undefined) out[k] = v.const;
    else if (v.default !== undefined) out[k] = defaultForSchema(v);
  }
  return out;
}

// ToolRunner renders a schema-driven form for one tool, runs it, and shows the
// result. It also lets the operator drop to raw-JSON arguments for tools whose
// shape the form cannot fully express (e.g. nested oneOf arrays).
export function ToolRunner({ tool }: { tool: Tool }) {
  const { callTool } = useMcp();
  const schema = tool.inputSchema as JsonSchema | undefined;
  const [args, setArgs] = useState<Record<string, unknown>>(() => initialArgs(schema));
  const [raw, setRaw] = useState(false);
  const [rawText, setRawText] = useState("{}");
  const [rawErr, setRawErr] = useState<string | null>(null);

  const call = useAsync((payload: Record<string, unknown>) => callTool(tool.name, payload));

  const argPreview = useMemo(() => JSON.stringify(args, null, 2), [args]);

  return (
    <div className="flex flex-col gap-3">
      {tool.description && (
        <p className="text-xs leading-relaxed text-cyan-100/50">{tool.description}</p>
      )}

      <div className="flex items-center gap-3 text-[0.65rem] uppercase tracking-[0.2em] text-cyan-100/40">
        <button
          className={`transition hover:text-cyan-glow ${!raw ? "text-cyan-glow" : ""}`}
          onClick={() => setRaw(false)}
        >
          form
        </button>
        <button
          className={`transition hover:text-cyan-glow ${raw ? "text-cyan-glow" : ""}`}
          onClick={() => {
            setRawText(argPreview);
            setRaw(true);
          }}
        >
          raw json
        </button>
      </div>

      {raw ? (
        <div>
          <textarea
            className="field h-40 font-mono"
            value={rawText}
            spellCheck={false}
            onChange={(e) => {
              setRawText(e.target.value);
              try {
                setArgs(JSON.parse(e.target.value || "{}"));
                setRawErr(null);
              } catch (ex) {
                setRawErr(ex instanceof Error ? ex.message : "invalid JSON");
              }
            }}
          />
          {rawErr && <p className="mt-1 text-[0.65rem] text-magenta-glow">{rawErr}</p>}
        </div>
      ) : (
        <SchemaForm schema={schema} value={args} onChange={setArgs} />
      )}

      <div>
        <button className="btn" disabled={call.loading} onClick={() => void call.run(args)}>
          {call.loading ? "running…" : "invoke"}
        </button>
      </div>

      {call.error && <p className="text-xs text-magenta-glow">error: {call.error}</p>}
      {call.data && <ResultView result={call.data} />}
    </div>
  );
}
