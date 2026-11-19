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
    "open",
    "save",
    "claude-desktop",
    "opencode",
    "omp",
    "grok",
    "hermes",
    "pi",
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

/** The sessions of the Windows and macOS native test (platform.ts), which next-app-platforms.yml
 * runs on the GitHub-hosted runners of those systems. They are not among the shards above: the
 * Linux matrix of next-app.yml does not list them and never runs them. */
export const PLATFORM_SHARDS: Record<string, readonly string[]> = {
  platform: ["single instance", "login item", "window and tray"],
  // The test package and the release gate on these systems: a job of its own, since it builds the
  // test package twice and the release app.
  package: ["bundle", "bundle release gate"],
  // The application update (update-cases.ts): a job of its own, since it builds the test package,
  // runs a test core, and its route restarts the app four times. Its session is not "update",
  // which names the Linux one in the app shard: a session is named once across all shards. The
  // install session (macOS: the disk image, its bundle copied out, updated and rolled back) shares
  // it, since it needs the same test package and test core.
  update: ["app update", "install"],
};

/** Every session, each named once across the shards. */
export const SESSIONS: ReadonlySet<string> = (() => {
  const all = new Set<string>();
  for (const [shard, names] of [...Object.entries(SHARDS), ...Object.entries(PLATFORM_SHARDS)])
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
  if (!shard) return new Set(Object.values(SHARDS).flat());
  const platform = PLATFORM_SHARDS[shard];
  if (platform) return new Set(platform);
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
