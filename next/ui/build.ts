// Builds the panel into next/internal/panel/dist, which core embeds (go generate ./internal/panel
// runs this). Asset names carry a hash of their content, so core can let browsers cache them.

import { mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";

const out = join(import.meta.dir, "..", "internal", "panel", "dist");
mkdirSync(out, { recursive: true });
for (const name of readdirSync(out)) {
  if (name !== ".gitkeep") rmSync(join(out, name), { recursive: true });
}

const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "panel", "index.html")],
  outdir: out,
  minify: true,
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  publicPath: "/panel/",
  naming: { entry: "[name].[ext]", chunk: "[name]-[hash].[ext]", asset: "[name]-[hash].[ext]" },
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
for (const o of result.outputs) {
  const size = o.size;
  const gz = Bun.gzipSync(new Uint8Array(await o.arrayBuffer())).length;
  console.log(`${o.path.slice(out.length + 1)}  ${size} bytes, ${gz} gzipped`);
}
