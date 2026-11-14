// The Codex sessions page. Everything a session holds is shown as text: this file sets
// textContent and attributes only, never HTML.
import { connect } from "./pluxy-page.js";

const PAGE_SIZE = 50;
const t = {
  title: "Codex Session Management",
  description:
    "Codex's local sessions on this computer, read-only. Showing a session's messages asks you each time.",
  refresh: "Refresh",
  total: "Total sessions",
  active: "Active",
  archived: "Archived",
  database: "Database source",
  databaseMissing: "No session database found",
  loadFailed: "Failed to load Codex sessions: {error}",
  localSessions: "Local sessions",
  pageDescription: "Page {page} · up to {size} items per page",
  loading: "Scanning local sessions…",
  empty: "No Codex sessions were found.",
  untitled: "Untitled session",
  noProject: "No project path",
  noProvider: "Unknown provider",
  noTime: "Unknown update time",
  previousPage: "Previous page",
  nextPage: "Next page",
  warnings: "Warnings",
  codexHome: "Codex home",
  codexHomeCustom: "set here",
  codexHomeDefault: "the default",
  codexHomeSave: "Use this folder",
  codexHomeReset: "Use the default",
  settingsFailed: "Failed to save the Codex home: {error}",
  backToList: "Back to Sessions",
  metadata: "Session Metadata",
  sessionTitle: "Session Title",
  workspace: "Workspace (CWD)",
  model: "Model",
  provider: "Model Provider",
  archivedStatus: "Archived Status",
  isArchived: "Archived",
  isNotArchived: "Active (Not Archived)",
  rolloutPath: "Rollout File",
  dbPath: "SQLite Database",
  totalLines: "Total Lines",
  messagesCount: "Messages",
  userTurns: "User Turns",
  assistantTurns: "Assistant Turns",
  toolExecutions: "Tool Calls",
  reasoningTurns: "Reasoning Blocks",
  fileSize: "File Size",
  filterAll: "All Roles",
  userRole: "User",
  assistantRole: "Assistant",
  toolRole: "Tool",
  reasoningRole: "Reasoning",
  lineNumber: "#Line {line}",
  truncated: "Cut here: the message is longer than 8 KiB.",
  loadMore: "Show more messages",
  detailFailed: "Failed to load session context: {error}",
  emptyMessages: "No conversational messages found in this session.",
};

const fill = (s, vars) => s.replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? ""));

/** An element with attributes (strings) and children (nodes or text). */
function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "onclick") e.addEventListener("click", v);
    else if (v !== undefined && v !== false) e.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

const app = document.getElementById("app");
const show = (...nodes) => app.replaceChildren(...nodes);
const errorText = (e) => (e && typeof e === "object" && "message" in e ? e.message : String(e));
const when = (ms) => (typeof ms === "number" ? new Date(ms).toLocaleString() : t.noTime);
const size = (n) =>
  n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`;

let pluxy;
let offset = 0;

/** Shows the list from offset n. */
function go(n) {
  offset = n;
  return showList();
}

async function call(op, input) {
  return pluxy.call("native.call", { package: pluxy.extension, op, input });
}

async function showList() {
  show(el("p", { class: "muted" }, t.loading));
  let page;
  try {
    page = await call("list", { offset, limit: PAGE_SIZE });
  } catch (e) {
    show(header(), el("p", { class: "error" }, fill(t.loadFailed, { error: errorText(e) })));
    return;
  }
  const stat = (label, value, cls) => el("div", { class: "stat" }, label, el("b", { class: cls }, value));
  const sessions = page.sessions.map((s) =>
    el(
      "li",
      { class: s.archived ? "archived" : "active" },
      el(
        "button",
        { type: "button", onclick: () => showDetail(s.id, s.title || t.untitled) },
        el("strong", {}, s.title || t.untitled),
        " ",
        el("span", { class: "badge" }, s.archived ? t.archived : t.active),
        el("br"),
        el("span", { class: "muted private" }, s.cwd || t.noProject),
        el("br"),
        el("span", { class: "muted" }, `${s.provider || t.noProvider} · ${when(s.updated_ms)}`),
      ),
    ),
  );
  show(
    header(),
    el(
      "div",
      { class: "stats" },
      stat(t.total, page.total),
      stat(t.active, page.active),
      stat(t.archived, page.archived),
      stat(t.database, page.databases[0] ?? t.databaseMissing, "private"),
    ),
    page.warnings.length
      ? el(
          "div",
          { class: "panel warn" },
          el("strong", {}, t.warnings),
          el(
            "ul",
            {},
            page.warnings.map((w) => el("li", {}, w)),
          ),
        )
      : null,
    home(page.codex_home),
    el("h2", {}, t.localSessions),
    el(
      "p",
      { class: "muted" },
      fill(t.pageDescription, { page: Math.floor(offset / PAGE_SIZE) + 1, size: PAGE_SIZE }),
    ),
    sessions.length ? el("ul", { class: "sessions" }, sessions) : el("p", { class: "muted" }, t.empty),
    el(
      "div",
      { class: "row" },
      el(
        "button",
        {
          type: "button",
          disabled: offset === 0,
          onclick: () => go(Math.max(0, offset - PAGE_SIZE)),
        },
        t.previousPage,
      ),
      el(
        "button",
        { type: "button", disabled: !page.has_more, onclick: () => go(offset + PAGE_SIZE) },
        t.nextPage,
      ),
    ),
  );
}

function header() {
  return el(
    "div",
    {},
    el(
      "div",
      { class: "row" },
      el("h1", {}, t.title),
      el("button", { type: "button", onclick: () => showList() }, t.refresh),
    ),
    el("p", { class: "muted" }, t.description),
  );
}

/** The Codex home in use, and the form that sets it (each change asks you natively). */
function home(current) {
  const input = el("input", { type: "text", size: 40, "aria-label": t.codexHome });
  input.value = current;
  const message = el("p", { class: "error" });
  const save = async (value) => {
    message.textContent = "";
    try {
      await call("settings", { codex_home: value });
      offset = 0;
      await showList();
    } catch (e) {
      message.textContent = fill(t.settingsFailed, { error: errorText(e) });
    }
  };
  return el(
    "div",
    { class: "panel" },
    el("div", { class: "row" }, el("strong", {}, t.codexHome), input),
    el(
      "div",
      { class: "row" },
      el("button", { type: "button", onclick: () => save(input.value.trim()) }, t.codexHomeSave),
      el("button", { type: "button", onclick: () => save("") }, t.codexHomeReset),
    ),
    message,
  );
}

async function showDetail(id, title) {
  const back = el("button", { type: "button", onclick: () => showList() }, t.backToList);
  show(el("div", { class: "row" }, back), el("h1", {}, title), el("p", { class: "muted" }, t.loading));
  let d;
  try {
    d = await call("detail", { id });
  } catch (e) {
    show(
      el("div", { class: "row" }, back),
      el("h1", {}, title),
      el("p", { class: "error" }, fill(t.detailFailed, { error: errorText(e) })),
    );
    return;
  }
  const row = (label, value, cls) => [el("dt", {}, label), el("dd", { class: cls }, value ?? "")];
  const filter = el(
    "select",
    { "aria-label": t.filterAll },
    [
      ["", t.filterAll],
      ["user", t.userRole],
      ["assistant", t.assistantRole],
      ["tool", t.toolRole],
      ["reasoning", t.reasoningRole],
    ].map(([v, label]) => el("option", { value: v }, label)),
  );
  const list = el("div");
  const more = el("div");
  let messages = d.messages;
  const render = () => {
    const role = filter.value;
    const shown = messages.filter((m) => !role || m.role === role);
    list.replaceChildren(
      ...(shown.length
        ? shown.map((m) =>
            el(
              "div",
              { class: `message ${m.role}` },
              el(
                "span",
                { class: "muted" },
                `${m.role} · ${fill(t.lineNumber, { line: m.line })}${m.timestamp ? ` · ${m.timestamp}` : ""}`,
              ),
              el("pre", {}, m.content),
              m.truncated ? el("p", { class: "muted" }, t.truncated) : null,
            ),
          )
        : [el("p", { class: "muted" }, t.emptyMessages)]),
    );
  };
  const loadMore = (next) => {
    more.replaceChildren(
      next === null
        ? ""
        : el(
            "button",
            {
              type: "button",
              onclick: async () => {
                try {
                  const page = await call("detail", { id: d.id, offset: next });
                  messages = messages.concat(page.messages);
                  render();
                  loadMore(page.next_offset);
                } catch (e) {
                  more.append(el("p", { class: "error" }, fill(t.detailFailed, { error: errorText(e) })));
                }
              },
            },
            t.loadMore,
          ),
    );
  };
  filter.addEventListener("change", render);
  render();
  loadMore(d.next_offset);
  show(
    el("div", { class: "row" }, back),
    el("h1", {}, d.title || t.untitled),
    el(
      "div",
      { class: "panel" },
      el("h2", {}, t.metadata),
      el(
        "dl",
        {},
        row(t.sessionTitle, d.title || t.untitled),
        row(t.workspace, d.cwd || t.noProject, "private"),
        row(t.model, d.model ?? ""),
        row(t.provider, d.provider || t.noProvider),
        row(t.archivedStatus, d.archived ? t.isArchived : t.isNotArchived),
        row(t.rolloutPath, d.rollout ?? "", "private"),
        row(t.dbPath, d.database ?? "", "private"),
      ),
    ),
    el(
      "div",
      { class: "stats" },
      [
        [t.totalLines, d.stats.lines],
        [t.messagesCount, d.stats.messages],
        [t.userTurns, d.stats.user],
        [t.assistantTurns, d.stats.assistant],
        [t.toolExecutions, d.stats.tool],
        [t.reasoningTurns, d.stats.reasoning],
        [t.fileSize, size(d.stats.size)],
      ].map(([label, value]) => el("div", { class: "stat" }, label, el("b", {}, value))),
    ),
    el("div", { class: "row" }, filter),
    list,
    more,
  );
}

connect()
  .then((p) => {
    pluxy = p;
    return showList();
  })
  .catch((e) => show(el("p", { class: "error" }, fill(t.loadFailed, { error: errorText(e) }))));
