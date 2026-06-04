interface JsonViewProps {
  value: unknown;
  className?: string;
}

// JsonView renders any value as pretty-printed, monospace JSON inside a CRT
// panel body. Used for structured tool output and raw result inspection.
export function JsonView({ value, className }: JsonViewProps) {
  let text: string;
  try {
    text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  } catch {
    text = String(value);
  }
  return (
    <pre
      className={`overflow-auto whitespace-pre-wrap break-words rounded bg-ink-900/80 p-3 text-xs leading-relaxed text-cyan-100/80 ${className ?? ""}`}
    >
      {text}
    </pre>
  );
}
