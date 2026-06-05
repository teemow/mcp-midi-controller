import { useEffect, useMemo, useState } from "react";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { structured, resultText } from "../mcp/result";
import type { DeviceView, ReadStateView } from "../mcp/types";
import { Panel } from "../components/Panel";
import { Field } from "../components/Field";
import { defaultForSchema, type JsonSchema } from "../components/schema";
import { ToolList } from "../components/ToolCard";

interface ControlSpec {
  name: string;
  description?: string;
  valueSchema: JsonSchema;
  parametric: boolean;
  // For parametric controls the inner value schema lives under value.value.
  innerValueSchema?: JsonSchema;
}

// parseControls extracts one ControlSpec per entry in the control_<name>
// tool's settings.items.oneOf, which the daemon builds from the device type
// (tools.go controlToolSchema).
function parseControls(tool: Tool | undefined): ControlSpec[] {
  const schema = tool?.inputSchema as JsonSchema | undefined;
  const items = schema?.properties?.settings?.items;
  const oneOf = items?.oneOf;
  if (!oneOf) return [];
  const specs: ControlSpec[] = [];
  for (const item of oneOf) {
    const name = item.properties?.control?.const;
    const valueSchema = item.properties?.value;
    if (typeof name !== "string" || !valueSchema) continue;
    const parametric =
      valueSchema.type === "object" && Boolean(valueSchema.properties?.value);
    specs.push({
      name,
      description: item.description,
      valueSchema,
      parametric,
      innerValueSchema: parametric ? valueSchema.properties?.value : undefined,
    });
  }
  return specs;
}

export function ControlTab() {
  const { callTool, status, tools } = useMcp();
  const [logical, setLogical] = useState<string>("");

  const devices = useAsync(async () => {
    const r = await callTool("list_devices");
    return structured<{ devices: DeviceView[] }>(r)?.devices ?? [];
  });
  const state = useAsync(async (dev: string) => {
    const r = await callTool("read_state", { device: dev });
    return structured<ReadStateView>(r);
  });

  useEffect(() => {
    if (status === "ready") void devices.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  // Default to the first control-bearing device once devices load.
  useEffect(() => {
    if (!logical && devices.data) {
      const first = devices.data.find((d) => tools.some((t) => t.name === `control_${d.name}`));
      if (first) {
        setLogical(first.name);
        void state.run(first.name);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [devices.data]);

  const controlTool = tools.find((t) => t.name === `control_${logical}`);
  const specs = useMemo(() => parseControls(controlTool), [controlTool]);

  const controllable = (devices.data ?? []).filter((d) =>
    tools.some((t) => t.name === `control_${d.name}`),
  );

  const observed = logical ? state.data?.[logical]?.observed ?? {} : {};
  const desired = logical ? state.data?.[logical]?.desired ?? {} : {};

  return (
    <div className="flex flex-col gap-3">
      <Panel title="Device">
        <div className="flex flex-wrap items-center gap-2">
          {controllable.length === 0 && (
            <p className="text-xs text-cyan-100/40">
              No control-bearing devices bound. Bind one with a control surface in the Devices tab.
            </p>
          )}
          {controllable.map((d) => (
            <button
              key={d.name}
              onClick={() => {
                setLogical(d.name);
                void state.run(d.name);
              }}
              className={`rounded border px-3 py-1.5 text-xs uppercase tracking-wider transition ${
                logical === d.name
                  ? "border-cyan-glow/70 bg-cyan-glow/10 text-cyan-glow shadow-glow-cyan"
                  : "border-cyan-500/20 text-cyan-100/60 hover:border-cyan-glow/40"
              }`}
            >
              {d.name}
            </button>
          ))}
          {logical && (
            <button className="btn ml-auto" disabled={state.loading} onClick={() => void state.run(logical)}>
              read state
            </button>
          )}
        </div>
      </Panel>

      {logical && (
        <Panel title={`control_${logical}`}>
          {specs.length === 0 && <p className="text-xs text-cyan-100/40">No controls in schema.</p>}
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {specs.map((spec) => (
              <ControlRow
                key={spec.name}
                logical={logical}
                spec={spec}
                desired={desired[spec.name]}
                observed={observed[spec.name]}
                onApplied={() => void state.run(logical)}
              />
            ))}
          </div>
        </Panel>
      )}

      <Panel title="Feedback & MIDI-learn">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Confirm a control echoed back (verify_control), map the rig's feedback matrix
          (probe_feedback), or capture inbound CCs (learn_start then learn_capture).
        </p>
        <ToolList names={["verify_control", "probe_feedback", "learn_start", "learn_capture"]} />
      </Panel>
    </div>
  );
}

function ControlRow({
  logical,
  spec,
  desired,
  observed,
  onApplied,
}: {
  logical: string;
  spec: ControlSpec;
  desired: unknown;
  observed: unknown;
  onApplied: () => void;
}) {
  const { callTool } = useMcp();
  const valueSchema = spec.parametric ? spec.innerValueSchema! : spec.valueSchema;
  const [value, setValue] = useState<unknown>(() => defaultForSchema(valueSchema));
  const [number, setNumber] = useState<number>(0);

  const apply = useAsync(async () => {
    const v = spec.parametric ? { number, value } : value;
    const r = await callTool(`control_${logical}`, { settings: [{ control: spec.name, value: v }] });
    onApplied();
    return r;
  });

  return (
    <div className="rounded border border-cyan-500/20 bg-ink-900/40 p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="font-mono text-sm text-cyan-glow">{spec.name}</span>
        <div className="flex gap-1.5 text-[0.6rem]">
          {desired !== undefined && <span className="tag">desired {fmt(desired)}</span>}
          {observed !== undefined && (
            <span className="tag border-magenta-glow/40 text-magenta-glow">obs {fmt(observed)}</span>
          )}
        </div>
      </div>
      {spec.parametric && (
        <div className="mb-2">
          <Field
            name="number"
            schema={{ type: "integer", minimum: 0, description: "address number (cc#/nrpn#/note#)" }}
            value={number}
            onChange={(v) => setNumber(typeof v === "number" ? v : parseInt(String(v), 10) || 0)}
          />
        </div>
      )}
      <Field name="value" schema={valueSchema} value={value} onChange={setValue} />
      <div className="mt-2 flex items-center gap-2">
        <button className="btn" disabled={apply.loading} onClick={() => void apply.run()}>
          {apply.loading ? "…" : "set"}
        </button>
        {apply.error && <span className="text-[0.65rem] text-magenta-glow">{apply.error}</span>}
        {apply.data && (
          <span className={`text-[0.65rem] ${apply.data.isError ? "text-magenta-glow" : "text-cyan-100/40"}`}>
            {resultText(apply.data)}
          </span>
        )}
      </div>
    </div>
  );
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}
