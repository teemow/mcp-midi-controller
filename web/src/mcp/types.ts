// Mirror of the daemon's structuredContent view shapes (see internal/mcpserver
// rig_tools.go / tools.go). The surface speaks only the three concepts: a
// device (an instance in the rig), a device type (a kind of gear), and a scene.
// Only the fields the SPA consumes are typed.

export interface ConnectionView {
  transport: string;
  endpoint?: string;
  channel?: number;
  writable?: boolean;
  usb?: boolean;
}

export interface DeviceView {
  name: string;
  type: string;
  type_name?: string;
  transport?: string;
  endpoint?: string;
  channel?: number;
  usb: boolean;
  usb_transport?: string;
  usb_endpoint?: string;
  writable?: boolean;
  connections?: ConnectionView[];
}

export interface EndpointView {
  id: string;
  name: string;
  transport: string;
  paired: boolean;
  connected: boolean;
}

export interface DeviceTypeSummary {
  id: string;
  name: string;
  manufacturer?: string;
  transport: string;
  controls: number;
  usb: boolean;
  // known reports whether a device in the rig already uses this type.
  known: boolean;
}

export interface ValueSpecView {
  type?: string;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  values?: Record<string, number>;
}

export interface ControlView {
  name: string;
  description?: string;
  type: string;
  cc?: number;
  nrpn?: number;
  program?: number;
  sysex?: string;
  address?: string;
  parametric?: boolean;
  value: ValueSpecView;
}

export interface DeviceTypeDetail {
  id: string;
  name: string;
  manufacturer?: string;
  description?: string;
  transport: string;
  settle_ms?: number;
  usb: boolean;
  controls: ControlView[];
}

export type ReadStateView = Record<
  string,
  { desired?: Record<string, unknown>; observed?: Record<string, unknown> }
>;
