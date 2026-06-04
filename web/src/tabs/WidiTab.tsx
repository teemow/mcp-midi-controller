import { Panel } from "../components/Panel";
import { ToolList } from "../components/ToolCard";

export function WidiTab() {
  return (
    <Panel title="WIDI configuration">
      <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
        Read a WIDI device's config and write settings or BLE groups. Open
        <span className="text-cyan-glow"> widi_read_config</span> first to see current state.
      </p>
      <ToolList
        names={["widi_read_config", "widi_write_setting", "widi_set_group", "widi_clear_group"]}
        openFirst
      />
    </Panel>
  );
}
