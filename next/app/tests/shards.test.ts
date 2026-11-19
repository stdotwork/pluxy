// The native test's shard table (native/shards.ts): every session is named once across the Linux
// shards and the Windows and macOS ones, since a session asks for its run by name. A name in two
// shards makes the table refuse to load, which stops every native test job before it starts.
import { expect, test } from "bun:test";

test("every native test session is named in one shard only", async () => {
  const { PLATFORM_SHARDS, SESSIONS, SHARDS, selectShard } = await import("../native/shards");
  const named = [...Object.values(SHARDS), ...Object.values(PLATFORM_SHARDS)].flat();
  expect(SESSIONS.size).toBe(named.length);
  for (const shard of Object.keys(PLATFORM_SHARDS)) expect(selectShard(shard).size).toBeGreaterThan(0);
});
