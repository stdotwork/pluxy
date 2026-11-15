// The native test's sessions (run.ts), each in one of the shards CI runs as parallel jobs. Each
// shard builds everything run.ts builds, then runs only its own sessions; without a shard, run.ts
// runs every session, as before. CI (.github/workflows/next-app.yml, the native job's matrix) lists
// these shards in this order, and run.ts refuses to start when the matrix it was given differs. A
// new session gets a name here, in the shard it fits best, and run.ts asks want(name) before it
// runs it. The times are from a main run, with the setup builds before them taking about 7
// minutes in every shard.
export const SHARDS: Record<string, readonly string[]> = {
  // About 7 minutes.
  usage: ["usage", "program", "transactions"],
  // About 6 minutes.
  tools: [
    "core steps",
    "admission",
    "routing",
    "vault",
    "spawn",
    "sessions",
    "launcher",
    "claude-code",
    "codex",
    "claude-desktop",
    "open",
  ],
  // About 7 minutes.
  app: [
    "managed",
    "startup",
    "window",
    "window-no-tray",
    "privacy",
    "access",
    "early exit",
    "instances",
    "update",
    "local",
    "remote",
    "packaged",
  ],
  // About 9 minutes: five release builds.
  release: ["release gate"],
};

/** Every session, each named once across the shards. */
export const SESSIONS: ReadonlySet<string> = (() => {
  const all = new Set<string>();
  for (const [shard, names] of Object.entries(SHARDS))
    for (const name of names) {
      if (all.has(name))
        throw new Error(`native/shards.ts: session ${name} is in more than one shard (${shard})`);
      all.add(name);
    }
  return all;
})();

/** The sessions this run runs: every one without a shard, otherwise that shard's. index and total
 * are the job's place in CI's matrix (strategy.job-index, strategy.job-total), which must match
 * SHARDS, so that every shard here is a job there. */
export function selectShard(shard?: string, index?: string, total?: string): ReadonlySet<string> {
  if (!shard) return SESSIONS;
  const names = Object.keys(SHARDS);
  const sessions = SHARDS[shard];
  if (!sessions) throw new Error(`no native test shard ${shard}; native/shards.ts has ${names.join(", ")}`);
  if (index !== undefined || total !== undefined) {
    if (total !== String(names.length) || index !== String(names.indexOf(shard)))
      throw new Error(
        `shard ${shard} is job ${index} of ${total} in CI's matrix, but native/shards.ts lists ${names.length} shards with ${shard} at ${names.indexOf(shard)}: list the shards ${names.join(", ")} in that order in next-app.yml`,
      );
  }
  return new Set(sessions);
}
