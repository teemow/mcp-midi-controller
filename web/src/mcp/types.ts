// Mirror of the daemon's structuredContent view shapes (see internal/mcpserver
// rig_tools.go / tools.go). Only the fields the SPA consumes are typed.

export interface BindingView {
  logical: string;
  device: string;
  device_name?: string;
  endpoint?: string;
  channel?: number;
  transport?: string;
  usb: boolean;
  usb_transport?: string;
  usb_endpoint?: string;
  writable?: boolean;
}

export interface EndpointView {
  id: string;
  name: string;
  transport: string;
  paired: boolean;
  connected: boolean;
}

export interface DefinitionSummary {
  id: string;
  name: string;
  manufacturer?: string;
  transport: string;
  controls: number;
  usb: boolean;
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

export interface DefinitionView {
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
