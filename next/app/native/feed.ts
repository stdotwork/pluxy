// The native test's fake release feed and stand-in browser (run.ts). The feed is a real https
// server on a random loopback port, with a certificate authority made for this run only: the app
// trusts it only in native-test builds, through PLUXY_APP_TEST_FEED_CA. The stand-in browser is
// the run's default https handler (a desktop file and mimeapps.list in the run's own XDG
// folders); it writes each address it is asked to open into a log and opens nothing.

import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** Makes a certificate authority and a server certificate for 127.0.0.1 in dir. */
export function certificates(dir: string): { ca: string; cert: string; key: string } {
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const ssl = (args: string[]) => {
    const r = spawnSync("openssl", args, { cwd: dir, stdio: ["ignore", "ignore", "pipe"], encoding: "utf8" });
    // r.error: openssl did not run at all (not found, or no such working folder).
    if (r.error) throw new Error(`openssl ${args[0]} could not run in ${dir}: ${r.error.message}`);
    if (r.status !== 0) throw new Error(`openssl ${args[0]}: exit ${r.status}: ${r.stderr}`);
  };
  const ec = ["-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes"];
  ssl([
    "req",
    "-x509",
    ...ec,
    "-keyout",
    "ca.key",
    "-out",
    "ca.pem",
    "-days",
    "1",
    "-subj",
    "/CN=pluxy native test feed CA",
    "-addext",
    "basicConstraints=critical,CA:TRUE",
    "-addext",
    "keyUsage=critical,keyCertSign",
  ]);
  ssl(["req", ...ec, "-keyout", "server.key", "-out", "server.csr", "-subj", "/CN=127.0.0.1"]);
  writeFileSync(
    join(dir, "server.ext"),
    "subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n",
  );
  ssl([
    "x509",
    "-req",
    "-in",
    "server.csr",
    "-CA",
    "ca.pem",
    "-CAkey",
    "ca.key",
    "-CAcreateserial",
    "-out",
    "server.pem",
    "-days",
    "1",
    "-extfile",
    "server.ext",
  ]);
  return { ca: join(dir, "ca.pem"), cert: join(dir, "server.pem"), key: join(dir, "server.key") };
}

export interface Feed {
  /** The feed's address; another path on the same server is never confirmed. */
  url: string;
  other: string;
  base: string;
  ca: string;
  /** Requests by path. */
  requests: Map<string, number>;
  stop(): void;
}

/** Serves native/release-feed.json at /feed.json, with {BASE} as this server's origin. */
export function startFeed(dir: string): Feed {
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const { ca, cert, key } = certificates(dir);
  const fixture = readFileSync(join(import.meta.dir, "release-feed.json"), "utf8");
  const requests = new Map<string, number>();
  let base = "";
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    tls: { cert: readFileSync(cert, "utf8"), key: readFileSync(key, "utf8") },
    fetch(req): Response {
      const path = new URL(req.url).pathname;
      requests.set(path, (requests.get(path) ?? 0) + 1);
      if (req.method === "GET" && path === "/feed.json") {
        return new Response(fixture.replaceAll("{BASE}", base), {
          headers: { "content-type": "application/json" },
        });
      }
      return new Response("not here", { status: 404 });
    },
  });
  base = `https://127.0.0.1:${server.port}`;
  return {
    url: `${base}/feed.json`,
    other: `${base}/other.json`,
    base,
    ca,
    requests,
    stop: () => server.stop(true),
  };
}

/** Makes the stand-in browser the run's https handler; returns the log it writes. */
export function standInBrowser(root: string, dirs: { config: string; data: string }): string {
  const log = join(root, "browser.log");
  const script = join(root, "browser.sh");
  writeFileSync(script, `#!/bin/sh\nprintf '%s\\n' "$1" >> ${JSON.stringify(log)}\n`);
  chmodSync(script, 0o700);
  const apps = join(dirs.data, "applications");
  mkdirSync(apps, { recursive: true });
  writeFileSync(
    join(apps, "pluxy-test-browser.desktop"),
    `[Desktop Entry]\nType=Application\nName=pluxy test browser\nExec=${script} %u\nMimeType=x-scheme-handler/https;\nNoDisplay=true\n`,
  );
  writeFileSync(
    join(dirs.config, "mimeapps.list"),
    "[Default Applications]\nx-scheme-handler/https=pluxy-test-browser.desktop\n",
  );
  return log;
}
