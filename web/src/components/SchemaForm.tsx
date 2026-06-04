import { Field } from "./Field";
import { type JsonSchema } from "./schema";

interface SchemaFormProps {
  schema: JsonSchema | undefined;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

// SchemaForm renders the top-level properties of an object schema as a stack of
// Field widgets. Properties pinned by `const` are hidden (auto-filled by the
// caller). Non-object schemas fall back to a single JSON field.
export function SchemaForm({ schema, value, onChange }: SchemaFormProps) {
  const props = schema?.properties ?? {};
  const names = Object.keys(props);
  const required = new Set(schema?.required ?? []);

  if (names.length === 0) {
    return <p className="text-xs text-cyan-100/40">No parameters.</p>;
  }

  return (
    <div className="flex flex-col gap-3">
      {names.map((name) => {
        const node = props[name];
        if (node.const !== undefined) return null;
        return (
          <Field
            key={name}
            name={name}
            schema={node}
            value={value[name]}
            required={required.has(name)}
            onChange={(v) => onChange({ ...value, [name]: v })}
          />
        );
      })}
    </div>
  );
}
