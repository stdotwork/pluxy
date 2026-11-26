import type { Host } from "../host";

export interface LogLines {
  lines: string[];
  "line-count": number;
  "latest-timestamp": number;
}
export interface ErrorLogFile {
  name: string;
  size: number;
  modified: number;
}
export interface LogContent {
  name: string;
  size: number;
  content: string;
}
export const logsApi = (host: Host) => ({
  logs: (after?: number) =>
    host.call<LogLines>("GET", after === undefined ? "/logs" : `/logs?after=${after}`),
  clearLogs: () => host.call<{ removed: number }>("DELETE", "/logs"),
  errorLogs: () => host.call<{ files: ErrorLogFile[] }>("GET", "/request-error-logs"),
  errorLog: (name: string) =>
    host.call<LogContent>("GET", `/request-error-logs/${encodeURIComponent(name)}?format=json`),
  requestLog: (id: string) =>
    host.call<LogContent>("GET", `/request-log-by-id/${encodeURIComponent(id)}?format=json`),
});
