// What the Agent usage page shows, worked out from the hub's report with no DOM: the text of every
// card, cell and status line, the bars and their segments, and the calendar's and hours' cells.
// app.js draws these; the page's tests (next/app/tests/agent-usage-page.test.ts) check them against
// a fixed report.

export const t = {
  title: "Agent usage",
  timeRange: "Time range",
  range: { today: "Today", "24h": "Last 24 hours", "7d": "Last 7 days", "30d": "Last 30 days", all: "All" },
  syncNow: "Sync now",
  syncing: "Syncing…",
  lastSynced: "Last synced",
  syncProgress: "Syncing {source} ({scanned}/{total})",
  neverSynced: "Not synced yet",
  loading: "Reading local Agent usage",
  readFailed: "Failed to read local Agent usage. Try again later.",
  totalTokens: "Total tokens",
  inputTokens: "Input tokens",
  outputTokens: "Output tokens",
  cacheReadTokens: "Cache read",
  cacheCreationTokens: "Cache write",
  sources: "Agent sources",
  sourceFilter: "Filter Agent",
  empty: "No local Agent usage yet",
  building: "Building the history database; data will appear as it is read",
  archived: "Uninstalled · history kept",
  bucketColumn: { hour: "Hour", day: "Date", week: "Week of", month: "Month" },
  viewLabel: "Chart view",
  view: { trend: "Trend", calendar: "Calendar", hours: "Hours", structure: "Token mix" },
  groupBy: "Chart grouping",
  groupBySource: "By source",
  groupByModel: "By model",
  token: { input: "Input", output: "Output", cache_read: "Cache read", cache_write: "Cache write" },
  chartEmpty: "No data to plot in this range",
  other: "Other",
  unknownModel: "Unknown model",
  granularity: "Granularity",
  granularities: { hour: "Hour", day: "Day", week: "Week", month: "Month" },
  hourUnavailable: "Hourly buckets need a shorter range than All",
  activeDays: "{count} active days",
  changeUp: "+{percent} vs previous period",
  changeDown: "−{percent} vs previous period",
  changeFlat: "Same as previous period",
  changeNoBaseline: "No usage in previous period",
  cacheHitRate: "Cache hit rate",
  cacheHitDetail: "{read} read · {write} written",
  models: "Models",
  showAllModels: "Show all {count} models",
  showFewerModels: "Show fewer",
  details: "Details",
  allSources: "All",
  share: "Share",
  days: "Active days",
  activityTitle: "Activity",
  activityHint: "Ignores the time range above and follows the Agent filter",
  calendarHint: "All history",
  hoursWindow: "Last 30 days",
  heatLess: "Less",
  heatMore: "More",
  storageTitle: "Agent usage storage",
  storageDescription: "Stored locally and kept after an Agent or its logs are removed, until you clear it.",
  storageCurrent: "Using {size} · {rows} hourly rows · {files} files",
  storageLastSync: "Last synced: {time}",
  clear: "Clear Agent history",
  cleared: "Agent usage history cleared.",
  clearFailed: "Agent usage history was not cleared: {error}",
  interval: "Sync interval while this page is open",
  intervals: { 0: "Off", 5: "Every 5 minutes", 15: "Every 15 minutes", 30: "Every 30 minutes", 60: "Every hour" },
  folders: "Folders",
  foldersDefault: "Its default folders",
  foldersSave: "Use these folders",
  foldersReset: "Use the defaults",
  foldersHint: "One full path a line. Changing them asks you to confirm.",
  foldersFailed: "The folders were not changed: {error}",
};

export const OTHER = "__other__";
/** Colours of the token parts, as the old page had them. */
export const TOKEN_COLORS = {
  cache_read: "#10b981",
  cache_write: "#a7f3d0",
  input: "#3b82f6",
  output: "#f59e0b",
};
/** The token mix's parts, bottom to top. */
export const MIX = ["cache_read", "cache_write", "input", "output"];
const SERIES = ["#3b82f6", "#f59e0b", "#10b981", "#8b5cf6", "#ef4444", "#06b6d4", "#ec4899"];
const OTHER_COLOR = "#9ca3af";
/** Models shown before "Show all". */
export const MODELS_SHOWN = 8;
/** Bars that get a value label above them. */
const SPARSE_BARS = 16;

export function fill(text, values) {
  return text.replace(/\{(\w+)\}/g, (_, k) => String(values[k] ?? ""));
}

const compactFormat = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });
const fullFormat = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

/** 1.2K, 3.4M. */
export function compact(n) {
  return compactFormat.format(n);
}

/** 1,234. */
export function full(n) {
  return fullFormat.format(n);
}

/** A share (0 to 1) as a percent with digits decimals. */
export function percent(x, digits) {
  return `${(x * 100).toFixed(digits)}%`;
}

/** Bytes as KB, MB or GB. */
export function size(bytes) {
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

/** A unix time as a short local date and time. */
export function when(unix) {
  return new Date(unix * 1000).toLocaleString("en-US", { dateStyle: "short", timeStyle: "short" });
}

/** The change from the previous period: its text and tone ("up", "down" or ""). */
export function change(current, previous) {
  if (previous == null) return null;
  if (previous <= 0) return { text: t.changeNoBaseline, tone: "" };
  const c = (current - previous) / previous;
  if (Math.abs(c) < 0.005) return { text: t.changeFlat, tone: "" };
  const p = percent(Math.abs(c), 0);
  return c > 0 ? { text: fill(t.changeUp, { percent: p }), tone: "up" } : { text: fill(t.changeDown, { percent: p }), tone: "down" };
}

/** The two cards: Total tokens and Cache hit rate. */
export function cards(report) {
  const tot = report.totals;
  const ch = change(tot.total, report.previous?.total);
  const parts = MIX.map((k) => tot[k]);
  const sum = parts.reduce((a, b) => a + b, 0);
  const mix = MIX.map((k) => ({
    key: k,
    label: t.token[k],
    color: TOKEN_COLORS[k],
    share: sum > 0 ? tot[k] / sum : 0,
    text: percent(sum > 0 ? tot[k] / sum : 0, 0),
  }));
  const denominator = tot.input + tot.cache_read + tot.cache_write;
  const rate = denominator > 0 ? tot.cache_read / denominator : 0;
  return {
    tokens: {
      label: t.totalTokens,
      value: compact(tot.total),
      title: full(tot.total),
      subtitle: ch ? ch.text : fill(t.activeDays, { count: report.active_days }),
      tone: ch ? ch.tone : "",
      mix,
    },
    cache: {
      label: t.cacheHitRate,
      value: percent(rate, 1),
      rate,
      subtitle: fill(t.cacheHitDetail, { read: compact(tot.cache_read), write: compact(tot.cache_write) }),
    },
  };
}

/** A bucket key as an axis label and as a full label. */
export function bucketLabel(key, granularity) {
  if (granularity === "hour") {
    const [d, h] = key.split("T");
    return { axis: `${d.slice(5)} ${h}:00`, full: `${d} ${h}:00` };
  }
  if (granularity === "month") return { axis: key, full: key };
  return { axis: key.slice(5), full: key };
}

/** A key's label: a source's, a model's, or Other. */
function keyLabel(key, groupBy, sources) {
  if (key === OTHER) return t.other;
  if (groupBy === "model") return key === "" ? t.unknownModel : key;
  return sources.find((s) => s.id === key)?.label ?? key;
}

/** The series of the trend: by source or by model, each key with its label and colour. */
export function trend(report, groupBy) {
  const stack = groupBy === "model" ? report.by_model : report.by_source;
  const keys = stack.keys.map((key, i) => ({
    key,
    label: keyLabel(key, groupBy, report.sources),
    title: groupBy === "source" && key !== OTHER ? `${keyLabel(key, groupBy, report.sources)} · ${key}` : null,
    color: key === OTHER ? OTHER_COLOR : SERIES[i % SERIES.length],
  }));
  const bars = report.buckets.map((bucket, b) => {
    let y = 0;
    const segments = keys.map((k, i) => {
      const value = stack.values[b]?.[i] ?? 0;
      const s = { key: k.key, value, y0: y, y1: y + value };
      y += value;
      return s;
    });
    return { bucket, label: bucketLabel(bucket, report.granularity), total: y, segments };
  });
  return bars_(keys, bars);
}

/** The token mix: each bucket's four parts. */
export function tokenMix(report) {
  const keys = MIX.map((k) => ({ key: k, label: t.token[k], color: TOKEN_COLORS[k] }));
  const bars = report.buckets.map((bucket, b) => {
    let y = 0;
    const segments = MIX.map((k) => {
      const value = report.series[b]?.[k] ?? 0;
      const s = { key: k, value, y0: y, y1: y + value };
      y += value;
      return s;
    });
    return { bucket, label: bucketLabel(bucket, report.granularity), total: y, segments };
  });
  return bars_(keys, bars);
}

function bars_(keys, bars) {
  const max = Math.max(0, ...bars.map((b) => b.total));
  return { keys, bars, max, ticks: ticks(max), labels: bars.length <= SPARSE_BARS, empty: max === 0 };
}

/** Axis ticks from 0: three to five steps of 1, 2 or 5 times a power of ten. */
export function ticks(max) {
  if (max <= 0) return [0];
  const raw = max / 4;
  const p = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 5, 10].map((m) => m * p).find((s) => max / s <= 5) ?? 10 * p;
  const out = [];
  for (let v = 0; v < max + step; v += step) out.push(v);
  return out;
}

/** Heat levels 0 to 4: 0 for nothing, then the quartiles of the positive values. */
export function levels(values) {
  const pos = values.filter((v) => v > 0).sort((a, b) => a - b);
  const q = (p) => pos[Math.max(0, Math.ceil(p * pos.length) - 1)];
  const [q1, q2, q3] = pos.length ? [q(0.25), q(0.5), q(0.75)] : [0, 0, 0];
  return (v) => (v <= 0 ? 0 : v <= q1 ? 1 : v <= q2 ? 2 : v <= q3 ? 3 : 4);
}

function addDays(date, n) {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

/** The calendar's cells: a column a week (Monday first), a row a weekday. */
export function calendar(report) {
  const { start, totals } = report.calendar;
  const level = levels(totals);
  const cells = totals.map((value, i) => ({
    date: addDays(start, i),
    value,
    level: level(value),
    col: Math.floor(i / 7),
    row: i % 7,
  }));
  const months = [];
  for (const c of cells) {
    if (c.row === 0 && (months.length === 0 || months[months.length - 1].month !== c.date.slice(0, 7))) {
      months.push({ col: c.col, month: c.date.slice(0, 7) });
    }
  }
  return { cells, weeks: Math.ceil(totals.length / 7), months };
}

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

/** The hours' cells: a row a weekday (Monday first), a column an hour. */
export function hours(report) {
  const level = levels(report.hours.flat());
  return report.hours.flatMap((row, d) =>
    row.map((value, h) => ({ weekday: WEEKDAYS[d], row: d, hour: h, value, level: level(value) })),
  );
}

/** The Models table: name, share and total; the first eight unless all. */
export function modelsTable(report, all) {
  const sum = report.models.reduce((a, m) => a + m.total, 0);
  const rows = report.models.map((m) => ({
    cells: [m.model === OTHER ? t.other : m.model === "" ? t.unknownModel : m.model, percent(sum > 0 ? m.total / sum : 0, 1), compact(m.total)],
    title: full(m.total),
  }));
  return {
    header: [t.models, t.share, t.totalTokens],
    rows: all ? rows : rows.slice(0, MODELS_SHOWN),
    more: rows.length > MODELS_SHOWN ? (all ? t.showFewerModels : fill(t.showAllModels, { count: rows.length })) : null,
  };
}

/** The Sources table: label (with its package ID and any archived note), share, total and active days. */
export function sourcesTable(report, selected) {
  const sum = report.sources.reduce((a, s) => a + s.total, 0);
  return {
    header: [t.sources, t.share, t.totalTokens, t.days],
    rows: report.sources.map((s) => ({
      id: s.id,
      cells: [s.label, percent(sum > 0 ? s.total / sum : 0, 1), compact(s.total), String(s.active_days)],
      note: s.status === "archived" ? `${s.id} · ${t.archived}` : s.id,
      dimmed: selected.length > 0 && !selected.includes(s.id),
    })),
  };
}

/** The Details table: each bucket with tokens, newest first. */
export function detailsTable(report) {
  const rows = [];
  report.buckets.forEach((bucket, i) => {
    const s = report.series[i];
    if (!s || s.total <= 0) return;
    rows.unshift([
      bucketLabel(bucket, report.granularity).full,
      full(s.input),
      full(s.output),
      full(s.cache_read),
      full(s.cache_write),
      full(s.total),
    ]);
  });
  return {
    header: [
      t.bucketColumn[report.granularity],
      t.inputTokens,
      t.outputTokens,
      t.cacheReadTokens,
      t.cacheCreationTokens,
      t.totalTokens,
    ],
    rows,
  };
}

/** The source chips: All, then each source with tokens in the range or selected, most first. */
export function chips(report, selected) {
  return [
    { id: null, label: t.allSources, active: selected.length === 0 },
    ...report.sources
      .filter((s) => s.total > 0 || selected.includes(s.id))
      .map((s) => ({
        id: s.id,
        label: s.label,
        total: compact(s.total),
        active: selected.includes(s.id),
        title: s.status === "archived" ? `${s.label} · ${s.id} · ${t.archived}` : `${s.label} · ${s.id}`,
        archived: s.status === "archived",
      })),
  ];
}

/** The sync status line. */
export function statusLine(sync) {
  if (sync.running) {
    const s = (sync.sources ?? []).find((x) => x.phase === "reading" && x.changed > 0);
    if (s) return fill(t.syncProgress, { source: s.label, scanned: s.read + s.failed, total: s.changed });
    return t.syncing;
  }
  if (sync.last_sync) return `${t.lastSynced} ${when(sync.last_sync)}`;
  return t.neverSynced;
}

/** The empty state's text, when the report has nothing. */
export function emptyText(report) {
  if (report.totals.total > 0) return null;
  return report.sync?.running ? t.building : t.empty;
}

/** The granularities and whether each can be chosen in range. */
export function granularities(range) {
  return ["hour", "day", "week", "month"].map((g) => ({
    id: g,
    label: t.granularities[g],
    disabled: g === "hour" && range === "all",
    title: g === "hour" && range === "all" ? t.hourUnavailable : "",
  }));
}

/** This zone's UTC offsets from fromMs to toMs: [[unix seconds, minutes east of UTC]], each
 * entry where the offset changes (found to the minute), as the hub's report takes them. */
export function utcOffsets(fromMs, toMs) {
  const east = (ms) => -new Date(ms).getTimezoneOffset();
  const out = [[Math.floor(fromMs / 1000), east(fromMs)]];
  const DAY = 86400000;
  for (let a = fromMs; a < toMs; a += DAY) {
    const b = Math.min(a + DAY, toMs);
    if (east(b) === east(a)) continue;
    let lo = a;
    let hi = b;
    while (hi - lo > 60000) {
      const mid = lo + Math.floor((hi - lo) / 2);
      if (east(mid) === east(a)) lo = mid;
      else hi = mid;
    }
    const at = Math.floor(hi / 60000) * 60;
    if (at > out[out.length - 1][0]) out.push([at, east(hi)]);
  }
  return out;
}
