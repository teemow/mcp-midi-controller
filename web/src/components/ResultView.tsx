import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { isError, resultImages, resultText, structured } from "../mcp/result";
import { JsonView } from "./JsonView";

// ResultView renders a tool result: any images, the human-readable text, then
// any structuredContent as JSON. Errors get a magenta error frame.
export function ResultView({ result }: { result: CallToolResult }) {
  const text = resultText(result);
  const images = resultImages(result);
  const struct = structured(result);
  const err = isError(result);
  return (
    <div className="flex flex-col gap-2">
      {images.map((src, i) => (
        <img
          key={i}
          src={src}
          alt="tool result"
          className="max-w-full rounded border border-cyan-500/20"
        />
      ))}
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
      {!text && struct === undefined && images.length === 0 && (
        <p className="text-xs text-cyan-100/40">(empty result)</p>
      )}
    </div>
  );
}
