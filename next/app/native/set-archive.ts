// The Windows test package's update: the program set of a folder bundle (the app's updater,
// src-tauri/src/updater/set.rs) as a .tar.gz, made here so it says the same on every system.

import { lstatSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

/** A .tar.gz of the program set in folder (exe, pluxy.exe and bundled/, at the top): ustar
 * entries for its folders and files only, in sorted order, owned by no one, mode 755 or 644 and no
 * time, so it says the same on every system. */
export function setArchive(folder: string, exe: string): Buffer {
  const blocks: Buffer[] = [];
  const header = (path: string, kind: "0" | "5", size: number) => {
    const h = Buffer.alloc(512);
    if (Buffer.byteLength(path) > 100) throw new Error(`${path} is too long for a ustar name`);
    h.write(path, 0, "utf8");
    h.write(`${kind === "5" ? "0000755" : "0000644"}\0`, 100, "latin1");
    h.write("0000000\0", 108, "latin1");
    h.write("0000000\0", 116, "latin1");
    h.write(`${size.toString(8).padStart(11, "0")}\0`, 124, "latin1");
    h.write("00000000000\0", 136, "latin1");
    h.write("        ", 148, "latin1");
    h.write(kind, 156, "latin1");
    h.write("ustar\u000000", 257, "latin1");
    let sum = 0;
    for (const b of h) sum += b;
    h.write(`${sum.toString(8).padStart(6, "0")}\0 `, 148, "latin1");
    return h;
  };
  const add = (rel: string) => {
    const path = join(folder, ...rel.split("/"));
    if (lstatSync(path).isDirectory()) {
      blocks.push(header(`${rel}/`, "5", 0));
      for (const name of readdirSync(path).sort()) add(`${rel}/${name}`);
      return;
    }
    if (!lstatSync(path).isFile()) throw new Error(`${path} is neither a folder nor a regular file`);
    const data = readFileSync(path);
    blocks.push(header(rel, "0", data.length), data, Buffer.alloc((512 - (data.length % 512)) % 512));
  };
  for (const part of ["bundled", "pluxy.exe", exe]) add(part);
  blocks.push(Buffer.alloc(1024));
  return gzipSync(Buffer.concat(blocks));
}
