import { useState } from "react";
import { isNumeric, schemaType, type JsonSchema } from "./schema";

interface FieldProps {
  name: string;
  schema: JsonSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  required?: boolean;
}

// Field renders a single schema-driven input widget: enum -> select, bounded
// number -> slider + box, number/integer -> number box, boolean -> checkbox,
// string -> text, and arrays/objects -> a JSON editor. Used by SchemaForm and
// the device control UI.
export function Field({ name, schema, value, onChange, required }: FieldProps) {
  const t = schemaType(schema);
  const label = (
    <label className="label">
      {name}
      {required ? <span className="text-magenta-glow"> *</span> : null}
      {schema.unit ? <span className="text-cyan-100/30"> ({schema.unit})</span> : null}
    </label>
  );

  // enum -> select
  if (schema.enum && schema.enum.length > 0) {
    return (
      <div>
        {label}
        <select
          className="field"
          value={String(value ?? "")}
          onChange={(e) => {
            const raw = e.target.value;
            const match = schema.enum!.find((o) => String(o) === raw);
            onChange(match ?? raw);
          }}
        >
          {schema.enum.map((o) => (
            <option key={String(o)} value={String(o)}>
              {String(o)}
            </option>
          ))}
        </select>
        {schema.description && <Hint text={schema.description} />}
      </div>
    );
  }

  // bounded numeric -> slider + number box
  if (isNumeric(schema) && schema.minimum !== undefined && schema.maximum !== undefined) {
    const num = typeof value === "number" ? value : Number(value) || schema.minimum;
    const step = t === "integer" ? 1 : "any";
    return (
      <div>
        {label}
        <div className="flex items-center gap-3">
          <input
            type="range"
            className="h-1 flex-1 cursor-pointer appearance-none rounded bg-ink-600 accent-cyan-glow"
            min={schema.minimum}
            max={schema.maximum}
            step={step}
            value={num}
            onChange={(e) => onChange(t === "integer" ? parseInt(e.target.value, 10) : parseFloat(e.target.value))}
          />
          <input
            type="number"
            className="field w-24"
            min={schema.minimum}
            max={schema.maximum}
            step={step}
            value={num}
            onChange={(e) => onChange(t === "integer" ? parseInt(e.target.value, 10) : parseFloat(e.target.value))}
          />
        </div>
        {schema.description && <Hint text={schema.description} />}
      </div>
    );
  }

  // unbounded numeric -> number box
  if (isNumeric(schema)) {
    return (
      <div>
        {label}
        <input
          type="number"
          className="field"
          step={t === "integer" ? 1 : "any"}
          min={schema.minimum}
          max={schema.maximum}
          value={value === undefined || value === null ? "" : String(value)}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "") return onChange(undefined);
            onChange(t === "integer" ? parseInt(v, 10) : parseFloat(v));
          }}
        />
        {schema.description && <Hint text={schema.description} />}
      </div>
    );
  }

  // boolean -> checkbox
  if (t === "boolean") {
    return (
      <div>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            className="h-4 w-4 accent-cyan-glow"
            checked={Boolean(value)}
            onChange={(e) => onChange(e.target.checked)}
          />
          <span className="text-xs uppercase tracking-[0.2em] text-cyan-100/50">{name}</span>
        </label>
        {schema.description && <Hint text={schema.description} />}
      </div>
    );
  }

  // array / object -> JSON editor
  if (t === "array" || t === "object") {
    return (
      <div>
        {label}
        <JsonField value={value} onChange={onChange} />
        {schema.description && <Hint text={schema.description} />}
      </div>
    );
  }

  // default -> text
  return (
    <div>
      {label}
      <input
        type="text"
        className="field"
        value={value === undefined || value === null ? "" : String(value)}
        onChange={(e) => onChange(e.target.value)}
      />
      {schema.description && <Hint text={schema.description} />}
    </div>
  );
}

function Hint({ text }: { text: string }) {
  return <p className="mt-1 text-[0.65rem] leading-snug text-cyan-100/35">{text}</p>;
}

// JsonField is a textarea bound to a JSON value; it keeps an editable string
// buffer and only propagates parsed values, flagging parse errors inline.
export function JsonField({ value, onChange }: { value: unknown; onChange: (v: unknown) => void }) {
  const [text, setText] = useState(() => {
    try {
      return value === undefined ? "" : JSON.stringify(value, null, 2);
    } catch {
      return "";
    }
  });
  const [err, setErr] = useState<string | null>(null);

  return (
    <div>
      <textarea
        className="field h-28 font-mono"
        value={text}
        spellCheck={false}
        onChange={(e) => {
          const next = e.target.value;
          setText(next);
          if (next.trim() === "") {
            setErr(null);
            onChange(undefined);
            return;
          }
          try {
            onChange(JSON.parse(next));
            setErr(null);
          } catch (ex) {
            setErr(ex instanceof Error ? ex.message : "invalid JSON");
          }
        }}
      />
      {err && <p className="mt-1 text-[0.65rem] text-magenta-glow">{err}</p>}
    </div>
  );
}
