import { useEffect, useState } from "react";
import { useMcp } from "../mcp/McpProvider";
import { useAsync } from "../hooks/useAsync";
import { structured } from "../mcp/result";
import type { BindingView, EndpointView, DefinitionSummary } from "../mcp/types";
import { Panel } from "../components/Panel";

export function DevicesTab() {
  const { callTool, status } = useMcp();

  const devices = useAsync(async () => {
    const r = await callTool("list_devices");
    return structured<{ devices: BindingView[] }>(r)?.devices ?? [];
  });
  const endpoints = useAsync(async () => {
    const r = await callTool("discover_endpoints");
    return structured<{ endpoints: EndpointView[] }>(r)?.endpoints ?? [];
  });
  const definitions = useAsync(async () => {
    const r = await callTool("list_definitions");
    return structured<{ definitions: DefinitionSummary[] }>(r)?.definitions ?? [];
  });

  useEffect(() => {
    if (status === "ready") {
      void devices.run();
      void definitions.run();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  const action = useAsync(async (name: string, args: Record<string, unknown>) => {
    const r = await callTool(name, args);
    await devices.run();
    return r;
  });

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <Panel
        title="Bound devices"
        actions={
          <button className="btn" disabled={devices.loading} onClick={() => void devices.run()}>
            refresh
          </button>
        }
      >
        {devices.error && <p className="text-xs text-magenta-glow">{devices.error}</p>}
        {(devices.data?.length ?? 0) === 0 && !devices.loading && (
          <p className="text-xs text-cyan-100/40">
            No devices bound yet. Discover an endpoint, then bind it.
          </p>
        )}
        <div className="flex flex-col gap-2">
          {devices.data?.map((d) => (
            <div key={d.logical} className="rounded border border-cyan-500/20 bg-ink-900/50 p-3">
              <div className="flex items-center justify-between">
                <span className="font-mono text-sm text-cyan-glow">{d.logical}</span>
                <button
                  className="btn btn-magenta py-0.5"
                  disabled={action.loading}
                  onClick={() => void action.run("unbind_device", { logical: d.logical })}
                >
                  unbind
                </button>
              </div>
              <div className="mt-2 flex flex-wrap gap-1.5 text-[0.65rem]">
                <span className="tag">{d.device_name ?? d.device}</span>
                {d.transport && <span className="tag">{d.transport}</span>}
                {d.endpoint && <span className="tag">{d.endpoint}</span>}
                {typeof d.channel === "number" && <span className="tag">ch {d.channel}</span>}
                {d.usb && (
                  <span className="tag border-magenta-glow/40 text-magenta-glow">
                    usb {d.usb_transport}
                    {d.writable ? " · writable" : ""}
                  </span>
                )}
              </div>
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

      <Panel title="Bind a device" className="lg:col-span-2">
        <BindForm
          endpoints={endpoints.data ?? []}
          definitions={definitions.data ?? []}
          busy={action.loading}
          onBind={(args) => void action.run("bind_device", args)}
        />
        {action.error && <p className="mt-2 text-xs text-magenta-glow">{action.error}</p>}
      </Panel>
    </div>
  );
}

function BindForm({
  endpoints,
  definitions,
  busy,
  onBind,
}: {
  endpoints: EndpointView[];
  definitions: DefinitionSummary[];
  busy: boolean;
  onBind: (args: Record<string, unknown>) => void;
}) {
  const [logical, setLogical] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [device, setDevice] = useState("");
  const [transport, setTransport] = useState("");
  const [channel, setChannel] = useState("");
  const [writable, setWritable] = useState(false);

  const submit = () => {
    const args: Record<string, unknown> = { logical, endpoint, device };
    if (transport) args.transport = transport;
    if (channel !== "") args.channel = parseInt(channel, 10);
    if (writable) args.writable = true;
    onBind(args);
  };

  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
      <div>
        <label className="label">logical name</label>
        <input className="field" value={logical} onChange={(e) => setLogical(e.target.value)} placeholder="e.g. reverb" />
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
        <label className="label">device definition</label>
        <input className="field" list="definition-options" value={device} onChange={(e) => setDevice(e.target.value)} placeholder="definition id" />
        <datalist id="definition-options">
          {definitions.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </datalist>
      </div>
      <div>
        <label className="label">transport (optional)</label>
        <select className="field" value={transport} onChange={(e) => setTransport(e.target.value)}>
          <option value="">default (control surface)</option>
          <option value="blemidi">blemidi</option>
          <option value="osc">osc</option>
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
        <button className="btn" disabled={busy || !logical || !endpoint || !device} onClick={submit}>
          bind
        </button>
      </div>
    </div>
  );
}
