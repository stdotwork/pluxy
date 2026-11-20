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
  // window 0.4, startup 0.3, early exit and instances 0.2, window-no-tray 0.1, privacy 0.1; and
  // setup, added later, about 1.5 on a Namespace devbox (not yet measured on GitHub's runners).
  app: [
    "managed",
    "startup",
    "window",
    "window-no-tray",
    "privacy",
    "setup",
    "access",
    "early exit",
    "instances",
    "update",
    "bundled core",
    "local",
    "remote",
    "packaged",
  ],
  // One release build: the gate's later calls find it up to date.
  release: ["release gate"],
};

/** The sessions of the Windows and macOS native test (platform.ts), which next-app-platforms.yml
 * runs on disposable runners of those systems (runner.ts), one job per shard and system. The Linux
 * matrix of next-app.yml does not list these shards. A session may have the same name here as in
 * SHARDS when it is the same session run on those systems (the tools shard: run.ts runs it on
 * Linux, platform.ts on Windows and macOS). */
export const PLATFORM_SHARDS: Record<string, readonly string[]> = {
  platform: ["single instance", "login item", "window and tray"],
  // The test package, its bundled core and the release gate on these systems: a job of its own,
  // since it builds the test package twice and the release app.
  package: ["bundle", "bundle release gate", "bundle core"],
  // The signed tool sessions in the real app (native/platform-tools.ts), on the build with
  // artifact-test-trust: a job of its own, since it builds that app, the fixture tool and the
  // execution program, and starts a test core.
  tools: ["admission", "program", "routing", "launcher", "vault"],
  // The open session of native/platform-tools.ts, on the same build as the tools shard: a job of
  // its own, as the tools shard's sessions take most of its time limit.
  open: ["open"],
  // The application update (update-cases.ts): a job of its own, since it builds the test package,
  // runs a test core, and its route restarts the app four times. Its session is not "update",
  // which names the Linux one in the app shard: a session is named once across all shards. The
  // install session (Windows: the per-user setup program, installed, updated in place by the app
  // and uninstalled; macOS: the disk image, its bundle copied out, updated and rolled back)
  // shares it, since it needs the same test package and test core.
  update: ["app update", "install"],
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
