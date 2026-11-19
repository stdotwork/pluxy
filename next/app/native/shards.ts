// The native test's sessions (run.ts), each in one of the shards CI runs as parallel jobs. Each
// shard runs only its own sessions, on what one build job built for them all
// (PLUXY_APP_NATIVE_PREBUILT); without a shard, run.ts runs every session, as before. CI
// (.github/workflows/next-app.yml, the native job's matrix) lists these shards in this order, and
// run.ts refuses to start when the matrix it was given differs. A new session gets a name here, in
// the shard it fits best, and run.ts asks want(name) before it runs it. The times are each
// session's on GitHub's Ubuntu runners (the median of four runs on main and in the merge queue on
// 2026-11-17, from each session's last line to the next's). The grouping made each shard about as
// long as the release gate was then, when each of its calls rebuilt the release app; the gate is
// one session and cannot be split.
export const SHARDS: Record<string, readonly string[]> = {
  // About 7.7 minutes: usage 4.4, program 2.3, core steps 0.5, transactions 0.5.
  usage: ["usage", "program", "transactions", "core steps"],
  // About 8 minutes: spawn 2.1, sessions 1.1, launcher 1.1, claude-code 1.0, routing 0.9, vault
  // 0.7, save 0.5, open 0.4, admission 0.2.
  tools: ["admission", "routing", "vault", "spawn", "sessions", "launcher", "claude-code", "open", "save"],
  // About 7.5 minutes: codex 1.4, pi 1.4, grok 1.3, hermes 0.9, claude-desktop 0.9, opencode 0.9,
  // omp 0.9.
  agents: ["codex", "claude-desktop", "opencode", "omp", "grok", "hermes", "pi"],
  // About 7.7 minutes: managed 1.9, local 1.1, remote 1.1, packaged 1.0, access 0.9, update 0.8,
  // window 0.4, startup 0.3, early exit and instances 0.2, window-no-tray 0.1, privacy 0.1.
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
  // One release build: the gate's later calls find it up to date.
  release: ["release gate"],
};

/** The sessions of the Windows and macOS native test (platform.ts), which next-app-platforms.yml
 * runs on disposable runners of those systems (runner.ts). They are not among the shards above: the
 * Linux matrix of next-app.yml does not list them and never runs them. */
export const PLATFORM_SHARDS: Record<string, readonly string[]> = {
  platform: ["single instance", "login item", "window and tray"],
  // The test package and the release gate on these systems: a job of its own, since it builds the
  // test package twice and the release app.
  package: ["bundle", "bundle release gate"],
  // The application update (update-cases.ts): a job of its own, since it builds the test package,
  // runs a test core, and its route restarts the app four times. Its session is not "update",
  // which names the Linux one in the app shard: a session is named once across all shards.
  update: ["app update"],
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
