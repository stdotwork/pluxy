// The Agent usage page: draws what view.js works out from the hub's report, and drives syncs.
// It sets textContent and attributes only, never HTML.
import { connect } from "./pluxy-page.js";
import * as v from "./view.js";

const { t } = v;
const HUB = "pluxy/agent-usage";
const SVG = "http://www.w3.org/2000/svg";
const RANGES = ["today", "24h", "7d", "30d", "all"];

const state = {
  range: "7d",
  granularity: null,
  selected: [],
  groupBy: "source",
  view: "trend",
  table: "models",
  allModels: false,
  report: null,
  sync: null,
  storage: null,
  sources: null,
  note: "",
  error: "",
};
let pluxy;
let offsets;
let syncTimer;

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, val] of Object.entries(attrs)) {
    if (k === "text") e.textContent = val;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), val);
    else if (val === true) e.setAttribute(k, "");
    else if (val !== false && val != null) e.setAttribute(k, String(val));
  }
  for (const c of children.flat()) if (c) e.append(c);
  return e;
}

function svg(tag, attrs = {}, ...children) {
  const e = document.createElementNS(SVG, tag);
  for (const [k, val] of Object.entries(attrs)) {
    if (k === "text") e.textContent = val;
    else e.setAttribute(k, String(val));
  }
  for (const c of children.flat()) if (c) e.append(c);
  return e;
}

function call(op, input = {}) {
  return pluxy.call("native.call", { package: HUB, op, input });
}

async function refresh() {
  try {
    const input = { range: state.range, sources: state.selected, offsets, now: Math.floor(Date.now() / 1000) };
    if (state.granularity) input.granularity = state.granularity;
    state.report = await call("report", input);
    state.sync = state.report.sync;
    state.storage = await call("storage");
    state.sources = await call("sources");
    state.error = "";
  } catch (e) {
    state.error = `${t.readFailed} (${e.message})`;
  }
  render();
}

/** One sync call; while a sync runs, another a second later, with the report refreshed now and
 * then and once it ends. */
async function sync(now = false) {
  clearTimeout(syncTimer);
  const was = state.sync?.running;
  try {
    state.sync = await call("sync", now ? { now: true } : {});
  } catch (e) {
    state.error = `${t.readFailed} (${e.message})`;
    render();
    return;
  }
  if (state.sync.running) {
    state.ticks = (state.ticks ?? 0) + 1;
    if (state.ticks % 5 === 0) await refresh();
    else render();
    syncTimer = setTimeout(() => sync(), 1000);
  } else if (was || now) {
    await refresh();
  } else render();
}

function segmented(label, items, current, onPick) {
  return el(
    "div",
    { class: "segmented", role: "group", "aria-label": label },
    items.map((i) =>
      el("button", {
        text: i.label,
        "aria-pressed": i.id === current ? "true" : "false",
        disabled: i.disabled,
        title: i.title || null,
        onclick: () => onPick(i.id),
      }),
    ),
  );
}

function header() {
  const running = state.sync?.running;
  return el(
    "header",
    { class: "row spread" },
    el("div", {}, el("h1", { text: t.title }), el("p", { class: "muted", text: state.sync ? v.statusLine(state.sync) : t.loading })),
    el("button", { text: running ? t.syncing : t.syncNow, disabled: running, onclick: () => sync(true) }),
  );
}

function toolbar(r) {
  const pickRange = (id) => {
    state.range = id;
    state.granularity = null;
    refresh();
  };
  const pickGranularity = (id) => {
    state.granularity = id;
    refresh();
  };
  const chips = v.chips(r, state.selected).map((c) =>
    el(
      "button",
      {
        class: "chip",
        "aria-pressed": c.active ? "true" : "false",
        title: c.title ?? null,
        "data-status": c.archived ? "archived" : null,
        onclick: () => {
          if (c.id === null) state.selected = [];
          else if (state.selected.includes(c.id)) state.selected = state.selected.filter((s) => s !== c.id);
          else state.selected = [...state.selected, c.id];
          refresh();
        },
      },
      el("span", { text: c.label }),
      c.total ? el("span", { class: "muted", text: ` ${c.total}` }) : null,
    ),
  );
  return el(
    "section",
    { class: "toolbar" },
    segmented(
      t.timeRange,
      RANGES.map((id) => ({ id, label: t.range[id] })),
      state.range,
      pickRange,
    ),
    segmented(t.granularity, v.granularities(state.range), r.granularity, pickGranularity),
    el("div", { class: "row", role: "group", "aria-label": t.sourceFilter }, chips),
  );
}

function cardsView(r) {
  const c = v.cards(r);
  const mix = el(
    "div",
    { class: "mixbar" },
    c.tokens.mix.map((m) => el("span", { style: `width:${(m.share * 100).toFixed(2)}%;background:${m.color}`, title: `${m.label} ${m.text}` })),
  );
  const legend = el(
    "div",
    { class: "row legend" },
    c.tokens.mix.map((m) => el("span", {}, el("i", { style: `background:${m.color}` }), `${m.label} ${m.text}`)),
  );
  return el(
    "section",
    { class: "cards" },
    el(
      "div",
      { class: "card", "data-kpi": "tokens" },
      el("div", { class: "muted", text: c.tokens.label }),
      el("div", { class: "kpi", text: c.tokens.value, title: c.tokens.title }),
      el("div", { class: `muted ${c.tokens.tone}`, text: c.tokens.subtitle }),
      mix,
      legend,
    ),
    el(
      "div",
      { class: "card", "data-kpi": "cache" },
      el("div", { class: "muted", text: c.cache.label }),
      el("div", { class: "kpi", text: c.cache.value }),
      el("div", { class: "meter" }, el("span", { style: `width:${(c.cache.rate * 100).toFixed(2)}%` })),
      el("div", { class: "muted", text: c.cache.subtitle }),
    ),
  );
}

function barChart(model) {
  if (model.empty) return el("p", { class: "muted empty", text: t.chartEmpty });
  const W = 800;
  const H = 240;
  const left = 48;
  const bottom = 24;
  const top = 16;
  const plotH = H - bottom - top;
  const max = model.ticks[model.ticks.length - 1] || 1;
  const n = model.bars.length;
  const slot = (W - left) / n;
  const barW = Math.max(1, Math.min(40, slot * 0.7));
  const y = (val) => top + plotH - (val / max) * plotH;
  const g = svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "chart", role: "img" });
  for (const tick of model.ticks) {
    g.append(svg("line", { x1: left, x2: W, y1: y(tick), y2: y(tick), class: "grid" }));
    g.append(svg("text", { x: left - 6, y: y(tick) + 4, class: "axis", "text-anchor": "end", text: v.compact(tick) }));
  }
  const every = Math.max(1, Math.ceil(n / 10));
  model.bars.forEach((b, i) => {
    const x = left + i * slot + (slot - barW) / 2;
    const group = svg("g", {}, svg("title", { text: `${b.label.full}: ${v.full(b.total)}` }));
    b.segments.forEach((s, k) => {
      if (s.value <= 0) return;
      group.append(svg("rect", { x, y: y(s.y1), width: barW, height: Math.max(0.5, y(s.y0) - y(s.y1)), fill: model.keys[k].color }));
    });
    g.append(group);
    if (model.labels && b.total > 0) {
      g.append(svg("text", { x: x + barW / 2, y: y(b.total) - 4, class: "axis", "text-anchor": "middle", text: v.compact(b.total) }));
    }
    if (i % every === 0) {
      g.append(svg("text", { x: x + barW / 2, y: H - 6, class: "axis", "text-anchor": "middle", text: b.label.axis }));
    }
  });
  const legend = el(
    "div",
    { class: "row legend" },
    model.keys.map((k) => el("span", {}, el("i", { style: `background:${k.color}` }), k.label)),
  );
  return el("div", {}, g, legend);
}

function charts(r) {
  const controls = el(
    "div",
    { class: "row" },
    segmented(
      t.viewLabel,
      [
        { id: "trend", label: t.view.trend },
        { id: "structure", label: t.view.structure },
      ],
      state.view,
      (id) => {
        state.view = id;
        render();
      },
    ),
    state.view === "trend"
      ? segmented(
          t.groupBy,
          [
            { id: "source", label: t.groupBySource },
            { id: "model", label: t.groupByModel },
          ],
          state.groupBy,
          (id) => {
            state.groupBy = id;
            render();
          },
        )
      : null,
  );
  const model = state.view === "trend" ? v.trend(r, state.groupBy) : v.tokenMix(r);
  return el("section", { class: "panel" }, controls, barChart(model));
}

function heat(cells, cols, rows, cell, titleOf, xOf, yOf) {
  const g = svg("svg", { viewBox: `0 0 ${cols * cell + 30} ${rows * cell + 4}`, class: "heat" });
  for (const c of cells) {
    g.append(
      svg(
        "rect",
        { x: 30 + xOf(c) * cell, y: yOf(c) * cell, width: cell - 2, height: cell - 2, rx: 2, class: `l${c.level}` },
        svg("title", { text: titleOf(c) }),
      ),
    );
  }
  ["Mon", "Wed", "Fri", "Sun"].forEach((d) => {
    const row = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].indexOf(d);
    g.append(svg("text", { x: 0, y: row * cell + cell - 3, class: "axis", text: d }));
  });
  return g;
}

function activity(r) {
  const cal = v.calendar(r);
  const hrs = v.hours(r);
  const scale = el(
    "div",
    { class: "row legend muted" },
    t.heatLess,
    [0, 1, 2, 3, 4].map((l) => el("i", { class: `heat-key l${l}` })),
    t.heatMore,
  );
  return el(
    "section",
    { class: "panel" },
    el("h2", { text: t.activityTitle }),
    el("p", { class: "muted", text: t.activityHint }),
    el("h3", { text: t.view.calendar }),
    el("p", { class: "muted", text: t.calendarHint }),
    heat(cal.cells, cal.weeks, 7, 12, (c) => `${c.date}: ${v.full(c.value)}`, (c) => c.col, (c) => c.row),
    el("h3", { text: t.view.hours }),
    el("p", { class: "muted", text: t.hoursWindow }),
    heat(hrs, 24, 7, 16, (c) => `${c.weekday} ${c.hour}:00: ${v.full(c.value)}`, (c) => c.hour, (c) => c.row),
    scale,
  );
}

function table(model, extra) {
  return el(
    "table",
    {},
    el("thead", {}, el("tr", {}, model.header.map((h) => el("th", { text: h })))),
    el(
      "tbody",
      {},
      model.rows.map((row) => {
        const cells = Array.isArray(row) ? row : row.cells;
        return el(
          "tr",
          { class: row.dimmed ? "dimmed" : null, title: row.title ?? null },
          cells.map((c, i) =>
            el("td", {}, el("span", { text: c }), i === 0 && row.note ? el("small", { class: "muted", text: ` ${row.note}` }) : null),
          ),
        );
      }),
    ),
    extra,
  );
}

function tables(r) {
  const models = v.modelsTable(r, state.allModels);
  const sources = v.sourcesTable(r, state.selected);
  const tabs = segmented(
    t.details,
    [
      { id: "models", label: `${t.models} ${r.models.length}` },
      { id: "sources", label: `${t.sources} ${r.sources.length}` },
      { id: "details", label: t.details },
    ],
    state.table,
    (id) => {
      state.table = id;
      render();
    },
  );
  let body;
  if (state.table === "models") {
    body = el(
      "div",
      {},
      table(models),
      models.more
        ? el("button", {
            text: models.more,
            onclick: () => {
              state.allModels = !state.allModels;
              render();
            },
          })
        : null,
    );
  } else if (state.table === "sources") body = table(sources);
  else body = table(v.detailsTable(r));
  return el("section", { class: "panel" }, tabs, body);
}

function settings() {
  const s = state.storage;
  const interval = state.sources?.interval_minutes ?? 5;
  const select = el(
    "select",
    {
      "aria-label": t.interval,
      onchange: async (e) => {
        await call("settings", { interval_minutes: Number(e.target.value) }).catch(() => {});
        await refresh();
      },
    },
    Object.entries(t.intervals).map(([m, label]) => el("option", { value: m, selected: Number(m) === interval, text: label })),
  );
  const folders = (state.sources?.sources ?? [])
    .filter((src) => src.status === "active")
    .map((src) => {
      const area = el("textarea", { rows: 2, placeholder: t.foldersDefault, "aria-label": `${t.folders}: ${src.label}` });
      area.value = (src.roots ?? []).join("\n");
      const save = async (roots) => {
        try {
          await call("roots", { source: src.id, roots });
          state.note = "";
        } catch (e) {
          if (e.code !== "forbidden") state.note = v.fill(t.foldersFailed, { error: e.message });
        }
        await refresh();
      };
      return el(
        "div",
        { class: "folders" },
        el("strong", { text: `${src.label} ` }),
        el("span", { class: "muted", text: src.id }),
        area,
        el(
          "div",
          { class: "row" },
          el("button", {
            text: t.foldersSave,
            onclick: () =>
              save(
                area.value
                  .split("\n")
                  .map((x) => x.trim())
                  .filter(Boolean),
              ),
          }),
          el("button", { text: t.foldersReset, onclick: () => save([]) }),
        ),
      );
    });
  return el(
    "section",
    { class: "panel" },
    el("h2", { text: t.storageTitle }),
    el("p", { class: "muted", text: t.storageDescription }),
    s ? el("p", { text: v.fill(t.storageCurrent, { size: v.size(s.bytes), rows: v.full(s.rows), files: v.full(s.files) }) }) : null,
    el("p", { class: "muted", text: s?.last_sync ? v.fill(t.storageLastSync, { time: v.when(s.last_sync) }) : t.neverSynced }),
    el("label", { class: "row" }, t.interval, select),
    el(
      "div",
      { class: "row" },
      el("button", {
        text: t.clear,
        disabled: state.sync?.running,
        onclick: async () => {
          try {
            await call("clear");
            state.note = t.cleared;
          } catch (e) {
            if (e.code !== "forbidden") state.note = v.fill(t.clearFailed, { error: e.message });
          }
          await refresh();
        },
      }),
      state.note ? el("span", { class: "muted", text: state.note }) : null,
    ),
    el("h3", { text: t.folders }),
    el("p", { class: "muted", text: t.foldersHint }),
    folders,
  );
}

function render() {
  const app = document.getElementById("app");
  const r = state.report;
  const parts = [header()];
  if (state.error) parts.push(el("p", { class: "error", text: state.error }));
  if (r) {
    parts.push(toolbar(r));
    const empty = v.emptyText({ ...r, sync: state.sync });
    if (empty) parts.push(el("p", { class: "muted empty", text: empty }));
    parts.push(cardsView(r), charts(r), activity(r), tables(r));
  }
  parts.push(settings());
  app.replaceChildren(...parts);
}

async function main() {
  try {
    pluxy = await connect();
  } catch (e) {
    document.getElementById("app").textContent = `${t.readFailed} (${e.message})`;
    return;
  }
  offsets = v.utcOffsets(Date.UTC(2000, 0, 1), Date.now() + 2 * 86400000);
  await refresh();
  // A sync when one is due (never synced, or the interval passed), and again each minute.
  await sync();
  setInterval(() => {
    if (!state.sync?.running) sync();
  }, 60000);
}

main();
