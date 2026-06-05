import { useEffect, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { structured } from "../mcp/result";
import type {
  ConnectionView,
  DeviceView,
  EndpointView,
  DeviceTypeSummary,
  DeviceTypeDetail,
} from "../mcp/types";
import { Panel } from "../components/Panel";
import { ToolList } from "../components/ToolCard";

// The single Devices tab: your rig (bound devices + their connections), the
// device-type catalog you can add from (with detail), endpoint discovery, the
// bind form, and the device-type authoring tools. AUM-derived devices appear in
// the rig like any other device — they are just devices on the auv3midi
// transport.
export function DevicesTab() {
  const { callTool, status } = useMcp();
  const [selectedType, setSelectedType] = useState<string | null>(null);

  // One call gives both the rig (devices) and the catalog (available types).
  const rig = useAsync(async () => {
    const r = await callTool("list_devices", { available: true });
    const sc = structured<{ devices: DeviceView[]; types: DeviceTypeSummary[] }>(r);
    return { devices: sc?.devices ?? [], types: sc?.types ?? [] };
  });
  const endpoints = useAsync(async () => {
    const r = await callTool("discover_endpoints");
    return structured<{ endpoints: EndpointView[] }>(r)?.endpoints ?? [];
  });
  const detail = useAsync(async (id: string) => {
    const r = await callTool("describe_device", { device: id });
    return structured<DeviceTypeDetail>(r);
  });

  useEffect(() => {
    if (status === "ready") void rig.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  const action = useAsync(async (name: string, args: Record<string, unknown>) => {
    const r = await callTool(name, args);
    await rig.run();
    return r;
  });

  const openType = (id: string) => {
    setSelectedType(id);
    void detail.run(id);
  };

  const devices = rig.data?.devices ?? [];
  const types = rig.data?.types ?? [];

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <Panel
        title="Your rig"
        actions={
          <button className="btn" disabled={rig.loading} onClick={() => void rig.run()}>
            refresh
          </button>
        }
      >
        {rig.error && <p className="text-xs text-magenta-glow">{rig.error}</p>}
        {devices.length === 0 && !rig.loading && (
          <p className="text-xs text-cyan-100/40">
            No devices in your rig yet. Discover an endpoint, then bind it to a device type.
          </p>
        )}
        <div className="flex flex-col gap-2">
          {devices.map((d) => (
            <div key={d.name} className="rounded border border-cyan-500/20 bg-ink-900/50 p-3">
              <div className="flex items-center justify-between">
                <span className="font-mono text-sm text-cyan-glow">{d.name}</span>
                <button
                  className="btn btn-magenta py-0.5"
                  disabled={action.loading}
                  onClick={() => void action.run("unbind_device", { name: d.name })}
                >
                  unbind
                </button>
              </div>
              <div className="mt-2 flex flex-wrap gap-1.5 text-[0.65rem]">
                <span className="tag">{d.type_name ?? d.type}</span>
                {d.transport && <span className="tag">{d.transport}</span>}
              </div>
              <Connections device={d} />
            </div>
          ))}
        </div>
      </Panel>

      <Panel
        title="Discover & pair endpoints"
        actions={
          <button className="btn" disabled={endpoints.loading} onClick={() => void endpoints.run()}>
            {endpoints.loading ? "scanning…" : "scan"}
          </button>
        }
      >
        {endpoints.error && <p className="text-xs text-magenta-glow">{endpoints.error}</p>}
        {!endpoints.data && !endpoints.loading && (
          <p className="text-xs text-cyan-100/40">Press scan to discover reachable endpoints.</p>
        )}
        <div className="flex flex-col gap-2">
          {endpoints.data?.map((e) => (
            <div key={`${e.transport}:${e.id}`} className="rounded border border-cyan-500/20 bg-ink-900/50 p-3">
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-mono text-sm text-cyan-100">{e.name || e.id}</span>
                {!e.paired && (
                  <button
                    className="btn py-0.5"
                    disabled={action.loading}
                    onClick={() =>
                      void action.run("pair_endpoint", { endpoint: e.id, transport: e.transport })
                    }
                  >
                    pair
                  </button>
                )}
              </div>
              <div className="mt-2 flex flex-wrap gap-1.5 text-[0.65rem]">
                <span className="tag">{e.transport}</span>
                <span className="tag truncate">{e.id}</span>
                <span className={`tag ${e.paired ? "border-cyan-glow/50 text-cyan-glow" : ""}`}>
                  {e.paired ? "paired" : "unpaired"}
                </span>
                <span className={`tag ${e.connected ? "border-cyan-glow/50 text-cyan-glow" : ""}`}>
                  {e.connected ? "connected" : "disconnected"}
                </span>
              </div>
            </div>
          ))}
        </div>
      </Panel>

      <Panel title="Available device types" className="lg:col-span-1">
        <p className="mb-2 text-xs text-cyan-100/40">
          The gear you can add. {types.length} type(s) loaded; * marks a type already in your rig.
        </p>
        <div className="flex flex-col gap-1.5">
          {types.map((t) => (
            <button
              key={t.id}
              onClick={() => openType(t.id)}
              className={`rounded border px-3 py-2 text-left transition ${
                selectedType === t.id
                  ? "border-cyan-glow/60 bg-cyan-glow/10"
                  : "border-cyan-500/15 bg-ink-900/40 hover:border-cyan-glow/40"
              }`}
            >
              <div className="flex items-center justify-between font-mono text-sm text-cyan-100">
                <span>{t.name}</span>
                {t.known && <span className="text-[0.6rem] text-cyan-glow">in rig</span>}
              </div>
              <div className="mt-1 flex flex-wrap gap-1 text-[0.6rem]">
                <span className="tag">{t.id}</span>
                <span className="tag">{t.transport}</span>
                <span className="tag">{t.controls} ctl</span>
                {t.usb && <span className="tag border-magenta-glow/40 text-magenta-glow">usb</span>}
              </div>
            </button>
          ))}
        </div>
      </Panel>

      <Panel title="Device type detail" className="lg:col-span-1">
        {!selectedType && <p className="text-xs text-cyan-100/40">Select a device type.</p>}
        {detail.loading && <p className="text-xs text-cyan-100/40">loading…</p>}
        {detail.data && <DeviceTypeDetailView def={detail.data} />}
      </Panel>

      <Panel title="Add a device" className="lg:col-span-2">
        <BindForm
          endpoints={endpoints.data ?? []}
          types={types}
          busy={action.loading}
          onBind={(args) => void action.run("bind_device", args)}
        />
        {action.error && <p className="mt-2 text-xs text-magenta-glow">{action.error}</p>}
      </Panel>

      <Panel title="Author a device type" className="lg:col-span-2">
        <p className="mb-3 text-xs leading-relaxed text-cyan-100/50">
          Start a draft, add controls one by one, then save to hot-load it into the catalog.
        </p>
        <ToolList names={["create_device_type", "add_control", "save_device_type"]} />
      </Panel>
    </div>
  );
}

// Connections renders a device's per-transport connection(s): endpoint, channel
// and any USB editor / write opt-in. The flat single-transport device shows one
// row; a multi-transport device (e.g. SL-2 on BLE + USB) shows several.
function Connections({ device }: { device: DeviceView }) {
  const conns: ConnectionView[] =
    device.connections && device.connections.length > 0
      ? device.connections
      : device.endpoint || device.channel
        ? [{ transport: device.transport ?? "", endpoint: device.endpoint, channel: device.channel }]
        : [];
  if (conns.length === 0) return null;
  return (
    <div className="mt-2 flex flex-col gap-1">
      {conns.map((c, i) => (
        <div key={`${c.transport}:${i}`} className="flex flex-wrap items-center gap-1.5 text-[0.65rem]">
          <span className={`tag ${c.usb ? "border-magenta-glow/40 text-magenta-glow" : ""}`}>
            {c.transport || "—"}
          </span>
          {c.endpoint && <span className="tag truncate">{c.endpoint}</span>}
          {typeof c.channel === "number" && c.channel > 0 && <span className="tag">ch {c.channel}</span>}
          {c.usb && <span className="tag">editor</span>}
          {c.writable && <span className="tag border-magenta-glow/40 text-magenta-glow">writable</span>}
        </div>
      ))}
    </div>
  );
}

function BindForm({
  endpoints,
  types,
  busy,
  onBind,
}: {
  endpoints: EndpointView[];
  types: DeviceTypeSummary[];
  busy: boolean;
  onBind: (args: Record<string, unknown>) => void;
}) {
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [device, setDevice] = useState("");
  const [transport, setTransport] = useState("");
  const [channel, setChannel] = useState("");
  const [writable, setWritable] = useState(false);

  const submit = () => {
    const args: Record<string, unknown> = { name, endpoint, device };
    if (transport) args.transport = transport;
    if (channel !== "") args.channel = parseInt(channel, 10);
    if (writable) args.writable = true;
    onBind(args);
  };

  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
      <div>
        <label className="label">device name</label>
        <input className="field" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. reverb" />
      </div>
      <div>
        <label className="label">endpoint</label>
        <input
          className="field"
          list="endpoint-options"
          value={endpoint}
          onChange={(e) => setEndpoint(e.target.value)}
          placeholder="endpoint id"
        />
        <datalist id="endpoint-options">
          {endpoints.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name} ({e.transport})
            </option>
          ))}
        </datalist>
      </div>
      <div>
        <label className="label">device type</label>
        <input className="field" list="type-options" value={device} onChange={(e) => setDevice(e.target.value)} placeholder="device type id" />
        <datalist id="type-options">
          {types.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </datalist>
      </div>
      <div>
        <label className="label">transport (optional)</label>
        <select className="field" value={transport} onChange={(e) => setTransport(e.target.value)}>
          <option value="">default (control)</option>
          <option value="blemidi">blemidi</option>
          <option value="osc">osc</option>
          <option value="auv3midi">auv3midi</option>
          <option value="usbmidi">usbmidi</option>
          <option value="usbhid">usbhid</option>
        </select>
      </div>
      <div>
        <label className="label">channel (optional)</label>
        <input className="field" type="number" value={channel} onChange={(e) => setChannel(e.target.value)} placeholder="1-16" />
      </div>
      <div className="flex items-end gap-3">
        <label className="flex items-center gap-2 text-xs text-cyan-100/60">
          <input type="checkbox" className="h-4 w-4 accent-magenta-glow" checked={writable} onChange={(e) => setWritable(e.target.checked)} />
          usb writable
        </label>
        <button className="btn" disabled={busy || !name || !endpoint || !device} onClick={submit}>
          add
        </button>
      </div>
    </div>
  );
}

function DeviceTypeDetailView({ def }: { def: DeviceTypeDetail }) {
  return (
    <div className="flex flex-col gap-3">
      <div>
        <h3 className="text-lg text-cyan-glow">{def.name}</h3>
        <div className="mt-1 flex flex-wrap gap-1.5 text-[0.65rem]">
          <span className="tag">{def.id}</span>
          {def.manufacturer && <span className="tag">{def.manufacturer}</span>}
          <span className="tag">{def.transport}</span>
          {def.usb && <span className="tag border-magenta-glow/40 text-magenta-glow">usb</span>}
        </div>
        {def.description && <p className="mt-2 text-xs text-cyan-100/50">{def.description}</p>}
      </div>
      <div className="overflow-auto">
        <table className="w-full border-collapse text-left text-xs">
          <thead>
            <tr className="text-[0.6rem] uppercase tracking-[0.2em] text-cyan-100/40">
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">control</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">type</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">address</th>
              <th className="border-b border-cyan-500/20 py-1.5 pr-3">value spec</th>
            </tr>
          </thead>
          <tbody>
            {def.controls.map((c) => (
              <tr key={c.name} className="align-top">
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 font-mono text-cyan-100">
                  {c.name}
                  {c.description && <div className="text-[0.6rem] text-cyan-100/40">{c.description}</div>}
                </td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/70">{c.type}</td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/60">{addressOf(c)}</td>
                <td className="border-b border-cyan-500/10 py-1.5 pr-3 text-cyan-100/60">{specOf(c)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function addressOf(c: DeviceTypeDetail["controls"][number]): string {
  if (c.cc !== undefined) return `cc ${c.cc}`;
  if (c.nrpn !== undefined) return `nrpn ${c.nrpn}`;
  if (c.program !== undefined) return `pgm ${c.program}`;
  if (c.sysex) return "sysex";
  if (c.address) return c.address;
  return "—";
}

function specOf(c: DeviceTypeDetail["controls"][number]): string {
  const v = c.value;
  if (v.values && Object.keys(v.values).length > 0) {
    return `enum {${Object.keys(v.values).join(", ")}}`;
  }
  const parts: string[] = [];
  if (v.min !== undefined || v.max !== undefined) {
    parts.push(`${v.min ?? "?"}..${v.max ?? "?"}`);
  }
  if (v.unit) parts.push(v.unit);
  return parts.join(" ") || v.type || "—";
}
