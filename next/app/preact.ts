// The kit and the app must run one copy of preact (two copies break hooks). The kit's copy is in
// ../ui/node_modules; this plugin resolves every preact import, the app's and the kit's, there,
// the way the kit itself resolves it. build.ts uses it.

import { join } from "node:path";
import type { BunPlugin } from "bun";

const ui = join(import.meta.dir, "..", "ui");

export const onePreact: BunPlugin = {
  name: "one preact",
  setup(build) {
    build.onResolve({ filter: /^preact(\/.*)?$/ }, (args) => ({ path: Bun.resolveSync(args.path, ui) }));
  },
};
