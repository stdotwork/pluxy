// What the Logs page reads out of core's log lines and failed-request file names.

/** The most lines the page keeps in the browser, so a very large log does not freeze the window. */
export const LOG_CAP = 10000;

/** A trace ID as core writes it (`server/requestlog.go`): the time to the second, then 16 hex digits. */
export const TRACE_SHAPE = /^[0-9]{14}-[0-9a-f]{16}$/;

/** An example of one, for the message that refuses anything else. */
export const TRACE_EXAMPLE = "20261125220609-f1787f10d870f9e7";

/** The level of a line of pluxy's log ("[2026-11-23 12:00:00.000] [warn ] ..."), or "". */
export function logLevel(line: string): string {
  return line.match(/^\[[^\]]+\] \[(info|warn|error)\s*\]/)?.[1] ?? "";
}

/** The local day as core's log writes it: 2026-11-23. */
export const logDay = (d: Date) =>
  [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");

/**
 * What a failed request's file name says: the request it was and its trace ID. Core writes the
 * request's path with its slashes as dashes (`error-v1-chat-completions-2026-11-25T220609-<trace>.log`),
 * so the path cannot be rebuilt and is shown as written. A name that does not fit is shown whole.
 */
export function failedRequest(name: string): { request: string; trace?: string } {
  const m = name.match(/^error-(.*)-\d{4}-\d{2}-\d{2}T\d{6}-([0-9]{14}-[0-9a-f]{16})\.log$/);
  return m ? { request: m[1] || "/", trace: m[2] } : { request: name };
}

/** A file's size in the unit a person reads: 812 B, 3.4 KB, 1.2 MB. */
export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const unit = bytes < 1024 * 1024 ? { size: 1024, name: "KB" } : { size: 1024 * 1024, name: "MB" };
  const n = bytes / unit.size;
  return `${n >= 10 ? Math.round(n) : Math.round(n * 10) / 10} ${unit.name}`;
}
