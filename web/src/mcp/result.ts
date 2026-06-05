import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";

// resultText concatenates every text content block of a tool result. Most
// daemon tools return a human-readable text rendering alongside any structured
// content.
export function resultText(res: CallToolResult): string {
  return (res.content ?? [])
    .filter((c): c is { type: "text"; text: string } => c.type === "text")
    .map((c) => c.text)
    .join("\n");
}

// structured returns the machine-readable structuredContent of a result, typed
// as the caller expects. The read tools (list_devices, describe_device,
// discover_endpoints, list_scenes, read_state, ...) emit it.
export function structured<T = unknown>(res: CallToolResult): T | undefined {
  return res.structuredContent as T | undefined;
}

export function isError(res: CallToolResult): boolean {
  return res.isError === true;
}
