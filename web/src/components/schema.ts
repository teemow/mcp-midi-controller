// Minimal JSON Schema shape covering what the daemon emits for tool inputs and
// per-control value specs. Tools are rendered schema-driven from these.
export interface JsonSchema {
  type?: string | string[];
  description?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  items?: JsonSchema;
  enum?: unknown[];
  const?: unknown;
  minimum?: number;
  maximum?: number;
  default?: unknown;
  oneOf?: JsonSchema[];
  anyOf?: JsonSchema[];
  format?: string;
  // The daemon attaches a unit hint to numeric value specs.
  unit?: string;
}

export function schemaType(s: JsonSchema | undefined): string {
  if (!s) return "";
  if (Array.isArray(s.type)) return s.type.find((t) => t !== "null") ?? s.type[0] ?? "";
  return s.type ?? "";
}

export function isNumeric(s: JsonSchema | undefined): boolean {
  const t = schemaType(s);
  return t === "number" || t === "integer";
}

// defaultForSchema produces a sensible initial value for a property so forms
// start in a valid-ish state.
export function defaultForSchema(s: JsonSchema | undefined): unknown {
  if (!s) return "";
  if (s.const !== undefined) return s.const;
  if (s.default !== undefined) return s.default;
  if (s.enum && s.enum.length > 0) return s.enum[0];
  const t = schemaType(s);
  switch (t) {
    case "integer":
    case "number":
      return s.minimum ?? 0;
    case "boolean":
      return false;
    case "array":
      return [];
    case "object":
      return {};
    default:
      return "";
  }
}
