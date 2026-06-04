import { Panel } from "../components/Panel";
import { ToolList } from "../components/ToolCard";

export function IpadTab() {
  return (
    <div className="flex flex-col gap-3">
      <div className="rounded border border-cyan-500/20 bg-ink-800/50 px-4 py-2 text-xs text-cyan-100/50">
        AUM sessions and AUv3 probes are staged by the separate iPad LAN receiver on
        <span className="text-cyan-glow"> :7800</span>. New arrivals also surface live in the Activity
        feed. This tab is mostly inspect/import.
      </div>

      <Panel title="AUM sessions">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          List, inspect, diff against the CC convention, import as proposed bindings, author or edit
          sessions, and export a single collection as a .aum_midimap.
        </p>
        <ToolList
          names={[
            "list_aum_sessions",
            "get_aum_session",
            "diff_aum_session",
            "import_aum_session",
            "author_aum_session",
            "edit_aum_session",
            "export_aum_midimap",
          ]}
          openFirst
        />
      </Panel>

      <Panel title="AUv3 probes">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          List staged plugin parameter-tree dumps, inspect one, and scaffold a device definition from it.
        </p>
        <ToolList names={["list_auv3_probes", "get_auv3_probe", "import_auv3_probe"]} openFirst />
      </Panel>
    </div>
  );
}
