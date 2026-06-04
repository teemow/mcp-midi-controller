import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { isError, resultText, structured } from "../mcp/result";
import { JsonView } from "./JsonView";

// ResultView renders a tool result: the human-readable text, then any
// structuredContent as JSON. Errors get a magenta error frame.
export function ResultView({ result }: { result: CallToolResult }) {
  const text = resultText(result);
  const struct = structured(result);
  const err = isError(result);
  return (
    <div className="flex flex-col gap-2">
      {text && (
        <pre
          className={`overflow-auto whitespace-pre-wrap break-words rounded p-3 text-xs leading-relaxed ${
            err
              ? "border border-magenta-glow/40 bg-magenta-glow/5 text-magenta-glow"
              : "bg-ink-900/80 text-cyan-100/85"
          }`}
        >
          {text}
        </pre>
      )}
      {struct !== undefined && (
        <div>
          <p className="label">structuredContent</p>
          <JsonView value={struct} />
        </div>
      )}
      {!text && struct === undefined && (
        <p className="text-xs text-cyan-100/40">(empty result)</p>
      )}
    </div>
  );
}
