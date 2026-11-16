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
 * runs on the GitHub-hosted runners of those systems, one job per shard and system. The Linux
 * matrix of next-app.yml does not list these shards. A session may have the same name here as in
 * SHARDS when it is the same session run on those systems (the tools shard: run.ts runs it on
 * Linux, platform.ts on Windows and macOS). */
export const PLATFORM_SHARDS: Record<string, readonly string[]> = {
  platform: ["single instance", "login item", "window and tray"],
  // The test package and the release gate on these systems: a job of its own, since it builds the
  // test package twice and the release app.
  package: ["bundle", "bundle release gate"],
  // The signed tool sessions in the real app (native/platform-tools.ts), on the build with
  // artifact-test-trust: a job of its own, since it builds that app, the fixture tool and the
  // execution program, and starts a test core.
  tools: ["admission", "program", "routing", "launcher"],
};

/** The sessions of shards, each named once across them. */
function sessionsOf(shards: Record<string, readonly string[]>, table: string): Set<string> {
  const all = new Set<string>();
  for (const [shard, names] of Object.entries(shards))
    for (const name of names) {
      if (all.has(name))
        throw new Error(`native/shards.ts: session ${name} is in more than one shard of ${table} (${shard})`);
      all.add(name);
    }
  return all;
}

/** Every session: each named once within SHARDS and once within PLATFORM_SHARDS. */
export const SESSIONS: ReadonlySet<string> = new Set([
  ...sessionsOf(SHARDS, "SHARDS"),
  ...sessionsOf(PLATFORM_SHARDS, "PLATFORM_SHARDS"),
]);

/** The sessions this run runs: every one without a shard, otherwise that shard's. index and total
 * are the job's place in CI's matrix (strategy.job-index, strategy.job-total), which must match
 * SHARDS, so that every shard here is a job there. With platform (platform.ts), the shard is one of
 * PLATFORM_SHARDS, whose matrix crosses the systems, so it has no place to check; without a shard
 * that is every platform session. */
export function selectShard(
  shard?: string,
  index?: string,
  total?: string,
  platform = false,
): ReadonlySet<string> {
  if (platform) {
    if (!shard) return new Set(Object.values(PLATFORM_SHARDS).flat());
    const sessions = PLATFORM_SHARDS[shard];
    if (!sessions)
      throw new Error(
        `no platform test shard ${shard}; native/shards.ts has ${Object.keys(PLATFORM_SHARDS).join(", ")}`,
      );
    return new Set(sessions);
  }
  if (!shard) return new Set(Object.values(SHARDS).flat());
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
