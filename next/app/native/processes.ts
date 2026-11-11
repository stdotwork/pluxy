import { readdirSync, readFileSync } from "node:fs";

/** The processes among pid's descendants, with their names. */
export function descendants(pid: number): Map<number, string> {
  const parent = new Map<number, number>();
  const name = new Map<number, string>();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const stat = readFileSync(`/proc/${d}/stat`, "utf8");
      const close = stat.lastIndexOf(")");
      name.set(Number(d), stat.slice(stat.indexOf("(") + 1, close));
      parent.set(Number(d), Number(stat.slice(close + 2).split(" ")[1]));
    } catch {
      // gone already
    }
  }
  const found = new Map<number, string>();
  for (const [p, comm] of name) {
    for (let q = parent.get(p); q !== undefined && q > 1; q = parent.get(q)) {
      if (q === pid) {
        found.set(p, comm);
        break;
      }
    }
  }
  return found;
}

/** The web processes among pid's descendants. */
export function webProcesses(pid: number): number {
  return [...descendants(pid).values()].filter((comm) => comm.startsWith("WebKitWebProces")).length;
}

