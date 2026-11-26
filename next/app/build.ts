// Builds the app's window into dist/, which the native build embeds (src-tauri/tauri.conf.json).
// The window's pages come from the UI kit in ../ui, and preact from ../ui's copy (preact.ts).

import { mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { onePreact } from "./preact";

const out = join(import.meta.dir, "dist");
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "src", "index.html")],
  outdir: out,
  minify: true,
  plugins: [onePreact],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  naming: { entry: "[name].[ext]", chunk: "[name]-[hash].[ext]", asset: "[name]-[hash].[ext]" },
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
for (const o of result.outputs) {
  const gz = Bun.gzipSync(new Uint8Array(await o.arrayBuffer())).length;
  console.log(`${o.path.slice(out.length + 1)}  ${o.size} bytes, ${gz} gzipped`);
}
