import { afterEach, beforeEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { createServer, type Server } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { type Pin, pinnedBytes, type Retry } from "../native/appimage";

// The pinned download (native/appimage.ts) against a local server that answers from a script.

const body = new TextEncoder().encode("the pinned bytes");
const digest = (data: Uint8Array) => createHash("sha256").update(data).digest("hex");
// Waits of a millisecond, so a retry costs the test nothing.
const fast: Retry = { backoffMs: [1, 1, 1], budgetMs: 10_000, attemptMs: 5_000 };

let cache: string;
let before: string | undefined;
const servers: { stop(force?: boolean): void }[] = [];

beforeEach(() => {
  cache = mkdtempSync(join(tmpdir(), "pluxy-pin-download-"));
  before = process.env.XDG_CACHE_HOME;
  process.env.XDG_CACHE_HOME = cache;
});

afterEach(() => {
  if (before === undefined) delete process.env.XDG_CACHE_HOME;
  else process.env.XDG_CACHE_HOME = before;
  for (const s of servers.splice(0)) s.stop(true);
  rmSync(cache, { recursive: true, force: true });
});

/** A server that answers its requests in turn from replies (the last one repeats), and the
 * pin for its file. */
function serve(replies: (number | Uint8Array)[], sha256 = digest(body)) {
  const requests: number[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch() {
      const reply = replies[Math.min(requests.length, replies.length - 1)] as number | Uint8Array;
      requests.push(1);
      return typeof reply === "number" ? new Response("refused", { status: reply }) : new Response(reply);
    },
  });
  servers.push(server);
  const pin: Pin = { cache: "file", url: `http://127.0.0.1:${server.port}/file`, sha256 };
  return { pin, requests };
}

test("a refused download is retried until the bytes come, and they are kept", async () => {
  const { pin, requests } = serve([403, 429, 503, body]);
  expect(await pinnedBytes(pin, fast)).toEqual(body);
  expect(requests.length).toBe(4);
  // Kept in the cache folder: the next use asks the server nothing.
  expect(await pinnedBytes(pin, fast)).toEqual(body);
  expect(requests.length).toBe(4);
});

test("a download that is always refused gives up and names every attempt", async () => {
  const { pin, requests } = serve([403, 403, 502, 403]);
  const error = await pinnedBytes(pin, fast).then(
    () => undefined,
    (e: Error) => e,
  );
  expect(error?.message).toBe(
    `downloading ${pin.url} failed (attempt 1: HTTP 403; attempt 2: HTTP 403; attempt 3: HTTP 502; attempt 4: HTTP 403)`,
  );
  expect(requests.length).toBe(4);
  expect(readdirSync(join(cache, "pluxy-app-native"))).toEqual([]);
});

test("a status a retry cannot change stops at the first attempt", async () => {
  const { pin, requests } = serve([404, body]);
  await expect(pinnedBytes(pin, fast)).rejects.toThrow(`downloading ${pin.url} failed (attempt 1: HTTP 404)`);
  expect(requests.length).toBe(1);
});

test("bytes with the wrong digest are refused at once, never retried and never kept", async () => {
  const { pin, requests } = serve([body, body], digest(new TextEncoder().encode("other bytes")));
  await expect(pinnedBytes(pin, fast)).rejects.toThrow(
    `${pin.url} has SHA-256 ${digest(body)}, not the pinned ${pin.sha256}`,
  );
  expect(requests.length).toBe(1);
  expect(readdirSync(join(cache, "pluxy-app-native"))).toEqual([]);
});

test("a refusal then the wrong bytes ends at the digest check, not another retry", async () => {
  const { pin, requests } = serve([403, body, body], digest(new TextEncoder().encode("other bytes")));
  await expect(pinnedBytes(pin, fast)).rejects.toThrow(`${pin.url} has SHA-256 ${digest(body)}`);
  expect(requests.length).toBe(2);
});

test("a cached file with the wrong digest is not used", async () => {
  const { pin, requests } = serve([body]);
  await pinnedBytes(pin, fast);
  const file = join(cache, "pluxy-app-native", pin.cache);
  expect(existsSync(file)).toBe(true);
  await Bun.write(file, "tampered");
  expect(await pinnedBytes(pin, fast)).toEqual(body);
  expect(requests.length).toBe(2);
});

test("a dropped connection is retried", async () => {
  // A bare TCP server: it hangs up on the first connection and answers the second.
  let connections = 0;
  const net: Server = createServer((socket) => {
    connections++;
    socket.on("error", () => {});
    if (connections === 1) return void socket.destroy();
    socket.end(
      Buffer.concat([
        Buffer.from(`HTTP/1.1 200 OK\r\nContent-Length: ${body.length}\r\nConnection: close\r\n\r\n`),
        body,
      ]),
    );
  });
  await new Promise<void>((done) => net.listen(0, "127.0.0.1", done));
  servers.push({ stop: () => net.close() });
  const address = net.address();
  if (!address || typeof address === "string") throw new Error("no port");
  const pin: Pin = { cache: "file", url: `http://127.0.0.1:${address.port}/file`, sha256: digest(body) };
  expect(await pinnedBytes(pin, fast)).toEqual(body);
  expect(connections).toBe(2);
});

test("the budget ends the retries early", async () => {
  const { pin, requests } = serve([403]);
  const started = Date.now();
  await expect(
    pinnedBytes(pin, { backoffMs: [200, 200, 200, 200], budgetMs: 350, attemptMs: 5_000 }),
  ).rejects.toThrow("failed (attempt 1: HTTP 403; attempt 2: HTTP 403)");
  expect(requests.length).toBe(2);
  expect(Date.now() - started).toBeLessThan(2_000);
});
