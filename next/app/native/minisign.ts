// Updater keys and signatures in the format Tauri's updater reads (the stock updater plugin checks
// them with minisign-verify): a public key is base64 of minisign's public key file, a signature
// base64 of minisign's signature file. Signatures here are minisign's prehashed kind (`ED`:
// Ed25519 over the file's BLAKE2b-512), and their trusted comment carries `version:<version>`,
// which the updater requires to equal the version the feed announces. For the test package and
// the native test only: every key is made at run time and kept in memory.

import { createHash, generateKeyPairSync, type KeyObject, randomBytes, sign } from "node:crypto";

export interface UpdaterKey {
  /** The public key as Tauri's `plugins.updater.pubkey` takes it. */
  pubkey: string;
  /** minisign's 8-byte key ID, as it appears in the key and its signatures. */
  keyId: Buffer;
  privateKey: KeyObject;
}

/** A new key; keyId, when given, is used instead of a random ID (a forged signature's). */
export function updaterKey(keyId: Buffer = randomBytes(8)): UpdaterKey {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const raw = Buffer.from(publicKey.export({ format: "jwk" }).x as string, "base64url");
  const id = Buffer.from(keyId).reverse().toString("hex").toUpperCase();
  const blob = Buffer.concat([Buffer.from("Ed"), keyId, raw]).toString("base64");
  const pubkey = Buffer.from(`untrusted comment: minisign public key: ${id}\n${blob}\n`).toString("base64");
  return { pubkey, keyId, privateKey };
}

/** The key ID inside a public key in Tauri's format. */
export function keyIdOf(pubkey: string): Buffer {
  const text = Buffer.from(pubkey, "base64").toString("utf8");
  const line = text.split("\n")[1] ?? "";
  return Buffer.from(line, "base64").subarray(2, 10);
}

/** A signature of bytes for version, in Tauri's format. */
export function signUpdate(key: UpdaterKey, bytes: Uint8Array, file: string, version: string): string {
  const hash = createHash("blake2b512").update(bytes).digest();
  const signature = sign(null, hash, key.privateKey);
  const trusted = `timestamp:${Math.floor(Date.now() / 1000)}\tfile:${file}\tversion:${version}`;
  const global = sign(null, Buffer.concat([signature, Buffer.from(trusted)]), key.privateKey);
  const first = Buffer.concat([Buffer.from("ED"), key.keyId, signature]).toString("base64");
  const text = `untrusted comment: signature from a pluxy test key\n${first}\ntrusted comment: ${trusted}\n${global.toString("base64")}\n`;
  return Buffer.from(text).toString("base64");
}
