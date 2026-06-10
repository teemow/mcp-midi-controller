// Bundles the public docs into web/public/docs so the SPA's Docs tab can fetch
// and render them as markdown. Runs as the Vite `prebuild` step.
//
// docs/private (rig snapshots / real names) is bundled too, but only into a
// separate manifest.private.json + private/ subdir. Both are gitignored in
// internal/webui/dist (the repo is public), so private docs only exist in
// local builds and never land in git. The Docs tab treats the private
// manifest as optional.
import { mkdirSync, copyFileSync, readdirSync, rmSync, existsSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");
const docsDir = resolve(repoRoot, "docs");
const outDir = resolve(here, "..", "public", "docs");

// Explicit allow-list of files/dirs for the public manifest.
const files = [
  { src: resolve(repoRoot, "README.md"), dest: "README.md" },
  { src: join(docsDir, "design.md"), dest: "design.md" },
  { src: join(docsDir, "usb-tools.md"), dest: "usb-tools.md" },
];

const manifest = { docs: [] };
const privateManifest = { docs: [] };

function add(srcPath, destRel, title, target = manifest) {
  if (!existsSync(srcPath)) {
    console.warn(`copy-docs: skipping missing ${srcPath}`);
    return;
  }
  const destPath = join(outDir, destRel);
  mkdirSync(dirname(destPath), { recursive: true });
  copyFileSync(srcPath, destPath);
  target.docs.push({ path: destRel, title: title ?? destRel });
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

// docs/private/*.md (top level only — subdirs hold binary captures). Goes
// into the separate private manifest; see header comment for the git story.
const privateDir = join(docsDir, "private");
if (existsSync(privateDir)) {
  for (const name of readdirSync(privateDir)) {
    if (name.endsWith(".md")) {
      add(
        join(privateDir, name),
        join("private", name),
        `private/${name.replace(/\.md$/, "")}`,
        privateManifest,
      );
    }
  }
}

writeFileSync(join(outDir, "manifest.json"), JSON.stringify(manifest, null, 2));
if (privateManifest.docs.length > 0) {
  writeFileSync(join(outDir, "manifest.private.json"), JSON.stringify(privateManifest, null, 2));
}
console.log(
  `copy-docs: bundled ${manifest.docs.length} public + ${privateManifest.docs.length} private doc(s) into public/docs`,
);
