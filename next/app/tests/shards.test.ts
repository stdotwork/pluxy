// The native test's shard table (native/shards.ts): a session is named once within the Linux
// shards and once within the Windows and macOS ones, since a session asks for its run by name in
// the run that has it (run.ts, platform.ts). A name in two shards of one table makes the table
// refuse to load, which stops every native test job before it starts. A name in both tables is
// the same session run on both: only the signed tool sessions (platform-tools.ts) are.
import { expect, test } from "bun:test";

test("every native test session is named in one shard only", async () => {
  const { PLATFORM_SHARDS, selectShard } = await import("../native/shards");
  for (const shard of Object.keys(PLATFORM_SHARDS))
    expect(selectShard(shard, undefined, undefined, true).size).toBeGreaterThan(0);
});
