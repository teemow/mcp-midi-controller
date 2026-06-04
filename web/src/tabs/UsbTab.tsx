import { useMcp } from "../mcp/McpProvider";
import { Panel } from "../components/Panel";
import { ToolList, ToolListByPrefix } from "../components/ToolCard";

const GLOBAL_USB = ["usb_identify", "usb_read", "usb_dump", "usb_probe", "usb_monitor", "usb_write"];

export function UsbTab() {
  const { tools } = useMcp();
  const writeAvailable = tools.some((t) => t.name === "usb_write");

  return (
    <div className="flex flex-col gap-3">
      <Panel title="USB editor / readback">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Generic USB inspection and (when enabled) writes. Write tools only appear when the daemon was
          started with <span className="text-cyan-glow">usb_allow_writes</span> and a writable USB binding —
          currently{" "}
          <span className={writeAvailable ? "text-cyan-glow" : "text-magenta-glow"}>
            {writeAvailable ? "writes ENABLED" : "read-only"}
          </span>
          .
        </p>
        <ToolList names={GLOBAL_USB} />
      </Panel>

      <Panel title="Per-device USB tools">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Dynamic editor/readback tools generated for each USB-bound device (named
          <span className="text-cyan-glow"> usb_&lt;logical&gt;_*</span>).
        </p>
        <ToolListByPrefix prefixes={["usb_"]} exclude={GLOBAL_USB} />
      </Panel>
    </div>
  );
}
