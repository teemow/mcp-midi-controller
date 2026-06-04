// Bundles the public docs into web/public/docs so the SPA's Docs tab can fetch
// and render them as markdown. Runs as the Vite `prebuild` step. Never copies
// docs/private (rig snapshots / real names) — that dir is gitignored and must
// stay out of the public bundle.
import { mkdirSync, copyFileSync, readdirSync, rmSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");
const docsDir = resolve(repoRoot, "docs");
const outDir = resolve(here, "..", "public", "docs");

// Explicit allow-list of files/dirs to bundle. Anything under docs/private is
// deliberately excluded.
const files = [
  { src: resolve(repoRoot, "README.md"), dest: "README.md" },
  { src: join(docsDir, "design.md"), dest: "design.md" },
  { src: join(docsDir, "usb-tools.md"), dest: "usb-tools.md" },
];

const manifest = { docs: [] };

function add(srcPath, destRel, title) {
  if (!existsSync(srcPath)) {
    console.warn(`copy-docs: skipping missing ${srcPath}`);
    return;
  }
  const destPath = join(outDir, destRel);
  mkdirSync(dirname(destPath), { recursive: true });
  copyFileSync(srcPath, destPath);
  manifest.docs.push({ path: destRel, title: title ?? destRel });
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

for (const f of files) {
  const title = f.dest.replace(/\.md$/, "");
  add(f.src, f.dest, title);
}

// docs/research/*.md
const researchDir = join(docsDir, "research");
if (existsSync(researchDir)) {
  for (const name of readdirSync(researchDir)) {
    if (name.endsWith(".md")) {
      add(join(researchDir, name), join("research", name), `research/${name.replace(/\.md$/, "")}`);
    }
  }
}

import { writeFileSync } from "node:fs";
writeFileSync(join(outDir, "manifest.json"), JSON.stringify(manifest, null, 2));
console.log(`copy-docs: bundled ${manifest.docs.length} doc(s) into public/docs`);
