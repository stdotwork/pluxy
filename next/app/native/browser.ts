// The open session's local https test address on Windows and macOS (the browser step of
// src-tauri/src/open_platform_native.rs, which hands it to updates::open_external, the app's own
// route to the person's browser): a server on 127.0.0.1 with a certificate made for this run,
// which appends each request it gets to a file the app's check reads.
//
// The certificate comes from a test authority made here, in memory: its private key is never
// written anywhere and is dropped once the server's certificate is signed. On macOS the default
// browser asks for the address only once it trusts the server, so the session adds that authority
// to the System keychain's trust (trustOnMac), on a GitHub-hosted runner only, and deletes it
// afterwards. Windows' check reads the browser's command line instead, and needs no trust.

import { spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, type KeyObject, randomBytes, sign } from "node:crypto";
import { appendFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// ---- DER, as much of it as a certificate needs ------------------------------------------------

function tlv(tag: number, body: Buffer): Buffer {
  const n = body.length;
  const len = n < 0x80 ? [n] : n < 0x100 ? [0x81, n] : [0x82, n >> 8, n & 0xff];
  if (n >= 0x10000) throw new Error("a DER value longer than this encoder writes");
  return Buffer.concat([Buffer.from([tag, ...len]), body]);
}
const seq = (...parts: Buffer[]) => tlv(0x30, Buffer.concat(parts));
const set = (...parts: Buffer[]) => tlv(0x31, Buffer.concat(parts));
/** A non-negative INTEGER from its big-endian bytes. */
const int = (b: Buffer) => tlv(0x02, (b[0] ?? 0) & 0x80 ? Buffer.concat([Buffer.from([0]), b]) : b);
const utf8 = (s: string) => tlv(0x0c, Buffer.from(s, "utf8"));
const octets = (b: Buffer) => tlv(0x04, b);
const bits = (b: Buffer, unused = 0) => tlv(0x03, Buffer.concat([Buffer.from([unused]), b]));
const yes = () => tlv(0x01, Buffer.from([0xff]));
const explicit = (n: number, b: Buffer) => tlv(0xa0 | n, b);

function oid(dotted: string): Buffer {
  const [a = 0, b = 0, ...rest] = dotted.split(".").map(Number);
  const out = [40 * a + b];
  for (const n of rest) {
    const bytes = [n & 0x7f];
    for (let v = Math.floor(n / 128); v > 0; v = Math.floor(v / 128)) bytes.unshift((v & 0x7f) | 0x80);
    out.push(...bytes);
  }
  return tlv(0x06, Buffer.from(out));
}

/** UTCTime, `YYMMDDHHMMSSZ` (years 1950 to 2049). */
function time(d: Date): Buffer {
  const p = (n: number) => String(n).padStart(2, "0");
  const s = `${p(d.getUTCFullYear() % 100)}${p(d.getUTCMonth() + 1)}${p(d.getUTCDate())}${p(d.getUTCHours())}${p(d.getUTCMinutes())}${p(d.getUTCSeconds())}Z`;
  return tlv(0x17, Buffer.from(s, "ascii"));
}

const name = (cn: string) => seq(set(seq(oid("2.5.4.3"), utf8(cn))));
const extension = (id: string, critical: boolean, value: Buffer) =>
  seq(oid(id), ...(critical ? [yes()] : []), octets(value));
/** ecdsa-with-SHA256, which takes no parameters. */
const ECDSA_SHA256 = seq(oid("1.2.840.10045.4.3.2"));

function pem(label: string, der: Buffer): string {
  const lines = der.toString("base64").match(/.{1,64}/g) ?? [];
  return `-----BEGIN ${label}-----\n${lines.join("\n")}\n-----END ${label}-----\n`;
}

/** An X.509 v3 certificate for spki (the subject's public key, DER), signed by key. */
function certificate(o: {
  issuer: string;
  subject: string;
  spki: Buffer;
  extensions: Buffer[];
  key: KeyObject;
  from: Date;
  to: Date;
}): Buffer {
  const serial = randomBytes(16);
  // Positive, and with no leading zero byte to drop.
  serial[0] = ((serial[0] ?? 0) & 0x7f) | 0x01;
  const tbs = seq(
    explicit(0, int(Buffer.from([2]))),
    int(serial),
    ECDSA_SHA256,
    name(o.issuer),
    seq(time(o.from), time(o.to)),
    name(o.subject),
    o.spki,
    explicit(3, seq(...o.extensions)),
  );
  return seq(tbs, ECDSA_SHA256, bits(sign("sha256", tbs, o.key)));
}

export interface Certificates {
  /** The test authority's certificate (PEM and DER). */
  ca: string;
  caDer: Buffer;
  /** The server's certificate (PEM) and its private key (PKCS #8 PEM). */
  cert: string;
  key: string;
}

/** A test authority (ECDSA P-256, valid from an hour ago for a day) and a server certificate it
 * signs for 127.0.0.1 and localhost, as Apple requires of a TLS server's (a subject alternative
 * name, the serverAuth extended key usage, SHA-256, at most 825 days). The authority's private
 * key is not returned: nothing can sign with it after this. */
export function certificates(): Certificates {
  const id = randomBytes(8).toString("hex");
  const from = new Date(Date.now() - 3600_000);
  const to = new Date(Date.now() + 24 * 3600_000);
  const ca = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const caSpki = ca.publicKey.export({ type: "spki", format: "der" });
  const caKeyId = createHash("sha1").update(caSpki).digest();
  const caDer = certificate({
    issuer: `pluxy native test authority ${id}`,
    subject: `pluxy native test authority ${id}`,
    spki: caSpki,
    key: ca.privateKey,
    from,
    to,
    extensions: [
      // basicConstraints: a CA.
      extension("2.5.29.19", true, seq(yes())),
      // keyUsage: keyCertSign and cRLSign.
      extension("2.5.29.15", true, bits(Buffer.from([0x06]), 1)),
      extension("2.5.29.14", false, octets(caKeyId)),
    ],
  });
  const server = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const spki = server.publicKey.export({ type: "spki", format: "der" });
  const certDer = certificate({
    issuer: `pluxy native test authority ${id}`,
    subject: "127.0.0.1",
    spki,
    key: ca.privateKey,
    from,
    to,
    extensions: [
      // basicConstraints: not a CA.
      extension("2.5.29.19", true, seq()),
      // keyUsage: digitalSignature.
      extension("2.5.29.15", true, bits(Buffer.from([0x80]), 7)),
      // extKeyUsage: serverAuth.
      extension("2.5.29.37", false, seq(oid("1.3.6.1.5.5.7.3.1"))),
      // subjectAltName: dNSName localhost, iPAddress 127.0.0.1.
      extension(
        "2.5.29.17",
        false,
        seq(tlv(0x82, Buffer.from("localhost", "ascii")), tlv(0x87, Buffer.from([127, 0, 0, 1]))),
      ),
      extension("2.5.29.14", false, octets(createHash("sha1").update(spki).digest())),
      // authorityKeyIdentifier: the authority's key identifier.
      extension("2.5.29.35", false, seq(tlv(0x80, caKeyId))),
    ],
  });
  return {
    ca: pem("CERTIFICATE", caDer),
    caDer,
    cert: pem("CERTIFICATE", certDer),
    key: server.privateKey.export({ type: "pkcs8", format: "pem" }).toString(),
  };
}

export interface Served {
  /** The address the app's check opens: this run's own, on 127.0.0.1. */
  url: string;
  certs: Certificates;
  stop: () => void;
}

/** Serves https on 127.0.0.1 at a free port with a certificate made for this run, appending one
 * JSON line per request (method, path, user agent) to dir/browser-requests.jsonl, and writes
 * dir/browser.json naming the address and that log, which the app's check reads. */
export function serveHttps(dir: string): Served {
  const certs = certificates();
  const log = join(dir, "browser-requests.jsonl");
  writeFileSync(log, "");
  const path = `/pluxy-open-external/${randomBytes(16).toString("hex")}`;
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    tls: { cert: certs.cert, key: certs.key },
    fetch(req) {
      const u = new URL(req.url);
      appendFileSync(
        log,
        `${JSON.stringify({ method: req.method, path: u.pathname, user_agent: req.headers.get("user-agent") ?? "" })}\n`,
      );
      return new Response(
        "<!doctype html><title>pluxy native test</title><p>The pluxy native test's page.</p>",
        {
          headers: { "content-type": "text/html; charset=utf-8" },
        },
      );
    },
  });
  const url = `https://127.0.0.1:${server.port}${path}`;
  writeFileSync(join(dir, "browser.json"), JSON.stringify({ url, log }));
  return { url, certs, stop: () => server.stop(true) };
}

/** Whether this is a GitHub-hosted runner, whose machine is thrown away after the job. */
export function githubHosted(): boolean {
  return process.env.GITHUB_ACTIONS === "true" && process.env.RUNNER_ENVIRONMENT === "github-hosted";
}

const SYSTEM_KEYCHAIN = "/Library/Keychains/System.keychain";

/** `security` as root (sudo -n: never a password prompt), at most a minute. */
function security(args: string[]) {
  const r = spawnSync("sudo", ["-n", "/usr/bin/security", ...args], { encoding: "utf8", timeout: 60_000 });
  return {
    status: r.status,
    out: `${r.stdout ?? ""}${r.stderr ?? ""}${r.error ? String(r.error) : ""}`.trim(),
  };
}

/** Whether macOS trusts the server certificate in certs for TLS to 127.0.0.1 now (`security
 * verify-cert` with the SSL policy and that name). */
export function macTrusts(certs: Certificates, dir: string): { ok: boolean; out: string } {
  const file = join(dir, "server-certificate.pem");
  writeFileSync(file, certs.cert);
  const r = spawnSync("/usr/bin/security", ["verify-cert", "-c", file, "-p", "ssl", "-s", "127.0.0.1"], {
    encoding: "utf8",
    timeout: 60_000,
  });
  return { ok: r.status === 0, out: `${r.stdout ?? ""}${r.stderr ?? ""}`.trim() };
}

/** macOS, on a GitHub-hosted runner only (it changes what the whole machine trusts): adds the test
 * authority in certs to the System keychain as a trusted root for this session, and returns what
 * removes it: deleting the certificate from that keychain (by its SHA-1), after which nothing
 * chains to it. The admin trust setting naming it stays: removing that asks for a password on
 * this macOS (`remove-trusted-cert -d` waits for a dialog no one can answer), and with the
 * certificate deleted and its key never kept, it matches nothing anyone can present. */
export function trustOnMac(certs: Certificates, dir: string): () => void {
  if (!githubHosted())
    throw new Error(
      `trusting a test certificate authority changes what the whole machine trusts, so it runs only on a GitHub-hosted runner (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted); here GITHUB_ACTIONS=${process.env.GITHUB_ACTIONS ?? ""}, RUNNER_ENVIRONMENT=${process.env.RUNNER_ENVIRONMENT ?? ""}`,
    );
  const file = join(dir, "test-authority.pem");
  writeFileSync(file, certs.ca);
  const sha1 = createHash("sha1").update(certs.caDer).digest("hex").toUpperCase();
  const added = security(["add-trusted-cert", "-d", "-r", "trustRoot", "-k", SYSTEM_KEYCHAIN, file]);
  const remove = () => {
    const r = security(["delete-certificate", "-Z", sha1, SYSTEM_KEYCHAIN]);
    if (r.status !== 0)
      throw new Error(
        `deleting the test authority ${sha1} from ${SYSTEM_KEYCHAIN} exited with ${r.status}: ${r.out}`,
      );
  };
  if (added.status !== 0) {
    // It may have been added to the keychain before the trust setting failed.
    let removed = "deleted it again";
    try {
      remove();
    } catch (e) {
      removed = (e as Error).message;
    }
    throw new Error(`trusting the test authority exited with ${added.status}: ${added.out} (${removed})`);
  }
  return remove;
}
