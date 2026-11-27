// The Logs page's texts, shared by its two tabs.

import { strings } from "../strings";

export const t = strings("logs", {
  title: "Logs",
  tip: "pluxy's log and failed requests",
  tabs: "Logs",
  tabLog: "pluxy's log",
  tabFailed: "Failed requests",
  hidden: "Names are hidden on screen only. A download has the log as pluxy wrote it.",

  // pluxy's log
  follow: "Follow new lines",
  wrap: "Wrap lines",
  find: "Find in log",
  show: "Show",
  allLines: "All lines",
  warnings: "Warnings",
  errors: "Errors",
  download: "Download",
  clear: "Clear log",
  clearQuestion: "Deletes pluxy's log. Failed requests stay.",
  confirmClear: "Yes, clear",
  log: "pluxy's log",
  loading: "Loading pluxy's log…",
  empty: "Nothing logged yet.",
  noMatch: "No lines match.",
  offTitle: "pluxy is not writing its log to a file",
  offText: "Turn it on to read the log here. It starts with the next thing pluxy logs.",
  turnOn: "Turn on file logging",
  turningOn: "Turning on…",
  settings: "Logging settings",
  readFailed: "Could not read pluxy's log: {error}",
  readNewFailed: "Could not read new lines: {error}. Trying again every 2 seconds.",
  capped: "Showing the latest 10,000 lines. Older lines are left out of Find and Download.",
  clearFailed: "Could not clear the log: {error}",
  turnOnFailed: "Could not turn on file logging: {error}",

  // Failed requests
  intro: "pluxy keeps the full log of its latest failed requests.",
  everyRequest:
    "Log every request is on, so failed requests are not listed here. Open a request's log by its trace ID.",
  traceId: "Trace ID",
  open: "Open",
  copyTrace: "Copy trace ID",
  copied: "Copied",
  colRequest: "Request",
  colTime: "Time",
  colTrace: "Trace ID",
  colSize: "Size",
  loadingFailed: "Loading failed requests…",
  none: "No failed requests.",
  listFailed: "Could not read the failed requests",
  opening: "Opening this log…",
  logOf: "Log of {trace}",
  missing:
    "No log is kept for this request. pluxy keeps logs of its latest failed requests, and of every request only while Log every request is on.",
  tooBig:
    "This log is over 2 MiB, too big to show or download here. It is in pluxy's logs folder on the computer pluxy runs on.",
  badTrace: "That is not a trace ID. It looks like {example}.",
  openFailed: "Could not open this log: {error}",
});
