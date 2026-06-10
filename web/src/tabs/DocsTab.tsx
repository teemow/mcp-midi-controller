import { useEffect, useState } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Panel } from "../components/Panel";

interface DocEntry {
  path: string;
  title: string;
}

// Docs are bundled at build time into public/docs (see web/scripts/copy-docs.mjs)
// and served under /app/docs. Private docs live in a separate, optional
// manifest.private.json that only exists in local builds (gitignored in dist).
const base = import.meta.env.BASE_URL;

export function DocsTab() {
  const [docs, setDocs] = useState<DocEntry[]>([]);
  const [active, setActive] = useState<string | null>(null);
  const [content, setContent] = useState<string>("");
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const publicDocs = fetch(`${base}docs/manifest.json`).then((r) =>
      r.ok ? (r.json() as Promise<{ docs: DocEntry[] }>) : Promise.reject(new Error(`manifest ${r.status}`)),
    );
    // The private manifest is absent in public builds; a 404 is expected.
    const privateDocs = fetch(`${base}docs/manifest.private.json`)
      .then((r) => (r.ok ? (r.json() as Promise<{ docs: DocEntry[] }>) : { docs: [] }))
      .catch(() => ({ docs: [] }));

    Promise.all([publicDocs, privateDocs])
      .then(([pub, priv]) => {
        const all = [...pub.docs, ...priv.docs];
        setDocs(all);
        if (all.length > 0) load(all[0].path);
      })
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const load = (path: string) => {
    setActive(path);
    setErr(null);
    fetch(`${base}docs/${path}`)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`${path} ${r.status}`))))
      .then(setContent)
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)));
  };

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-4">
      <Panel title="Docs" className="lg:col-span-1">
        {docs.length === 0 && !err && <p className="text-xs text-cyan-100/40">loading…</p>}
        <div className="flex flex-col gap-1">
          {docs.map((d) => (
            <button
              key={d.path}
              onClick={() => load(d.path)}
              className={`rounded px-2 py-1.5 text-left text-xs transition ${
                active === d.path ? "bg-cyan-glow/10 text-cyan-glow" : "text-cyan-100/60 hover:text-cyan-100"
              }`}
            >
              {d.title}
            </button>
          ))}
        </div>
      </Panel>

      <Panel title={active ?? "Document"} className="lg:col-span-3">
        {err && <p className="text-xs text-magenta-glow">{err}</p>}
        <article className="docs-prose max-w-none text-sm leading-relaxed text-cyan-100/80">
          <Markdown remarkPlugins={[remarkGfm]}>{content}</Markdown>
        </article>
      </Panel>
    </div>
  );
}
