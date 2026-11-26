// The app's Updates page: whether there is anything newer than what this app and pluxy run, from the
// update source the native side reads (src-tauri/src/release.rs), on the Stable or the Preview
// channel, and the app update itself. A build with no update source or no updater says so in one
// line instead of drawing sections that cannot work. The release notes never render in this
// webview: the native side opens a webview of their own over the notes area, with no app command
// granted (src-tauri/src/notes.rs). This page gets only versions, dates and status from the native
// side. "Update now" installs the app's newer release through the native updater
// (src-tauri/src/updater/), which says what it is doing and why it cannot update, if it cannot.

import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { clock, Phases, SettingRow } from "../../ui/kit/parts";
import type { PageDef } from "../../ui/kit/shell";
import { rich, strings } from "../../ui/kit/strings";
import { Badge, Button, Choices, Notice, PageHead, useLoad, useThemeState, when } from "../../ui/kit/ui";
import { Section } from "./clientPage";
import { type Connection, reply } from "./host";
import { Details, Intro, TextField } from "./pageBits";

type Product = "app" | "core";
type Channel = "stable" | "preview";

interface Latest {
  version: string;
  channel: Channel;
  published: string;
  has_notes: boolean;
  has_notes_url: boolean;
  newer?: boolean | null;
}

interface ProductView {
  installed?: string | null;
  latest?: Latest | null;
}

/** What the native side says about the update source; no notes text and no addresses but the source's. */
export interface ReleaseView {
  source?: string | null;
  channel: Channel;
  app: ProductView;
  core: ProductView;
  checked_at?: number | null;
  problem?: { offline: boolean; message: string } | null;
  changing_source: boolean;
}

/** How a source change was settled (`pluxy-release`). */
interface Settled {
  changed: boolean;
  error?: string | null;
}

/** What the native updater says (`app_update_get`, `pluxy-app-update`). */
interface Pinned {
  version: string;
  sha256: string;
}
export interface AppUpdateView {
  available: boolean;
  reason?: string | null;
  version?: string | null;
  task: {
    phase:
      | "idle"
      | "confirming"
      | "downloading"
      | "verifying"
      | "installing"
      | "restarting"
      | "failed"
      | "cancelled";
    version?: string | null;
    downloaded: number;
    total?: number | null;
    error?: string | null;
  };
  last?: { result: string; detail: string } | null;
  rollback: Pinned[];
}

const t = strings("updates", {
  title: "Updates",
  tip: "Versions of this app and pluxy, and updates",
  reading: "Reading versions…",
  readFailed: "Could not read the update settings",
  check: "Check for updates",
  checking: "Checking…",
  noSource: "This app has no update source yet, so it cannot check for updates.",
  versions: "Versions",
  thisApp: "This app",
  pluxy: "pluxy",
  preview: "Preview",
  upToDate: "Up to date",
  available: "Version {version} available",
  notChecked: "Not checked",
  checkFailed: "Check failed",
  latest: "Latest version {version}",
  noneOnChannel: "No release on this channel",
  unknown: "Unknown",
  published: "Published {date}",
  managed: "Updated together with this app.",
  local: "Runs on its own; update it where you installed it.",
  remote: "Runs on {host}; update it there.",
  notConnected: "Not connected",
  cannotUpdate: "This copy of the app cannot update itself.",
  appUpdate: "App update",
  ready: "Version {version} is ready to install.",
  updateNow: "Update now",
  restartManaged: "The app restarts, and the pluxy it runs stops for a moment.",
  restartOther: "The app restarts. pluxy keeps running.",
  waiting: "Waiting",
  confirming: "Waiting for your confirmation",
  downloading: "Downloading {done} of {total}",
  downloadingUnknown: "Downloading {done}",
  verifying: "Checking the download",
  installing: "Getting ready",
  restarting: "Restarting",
  failed: "Update failed",
  cancelled: "Download cancelled",
  cancel: "Cancel download",
  goBack: "Go back to {version}…",
  goBackAbout: "Versions this app had before. Going back asks you to confirm.",
  noDownload: "Version {version} has no download for this computer.",
  notesFor: "Release notes for {version}",
  show: "Show",
  hide: "Hide",
  openFull: "Open full notes",
  source: "Update source",
  channel: "Channel",
  stable: "Stable",
  channelAbout: "Stable gets finished releases. Preview gets early builds before they are final.",
  noneSet: "None set",
  lastChecked: "Last checked {time}",
  neverChecked: "Never checked",
  address: "Address",
  addressAbout:
    "Where this app looks for new versions. Setting, changing or removing it asks you to confirm.",
  setSource: "Set source…",
  changeSource: "Change source…",
  removeSource: "Remove source…",
  answerWindow: "Answer the confirmation window pluxy opened.",
  offline: "Could not reach the update source.",
  offlineAt: "Could not reach the update source. Showing what it said at {time}.",
  unusable: "The update source answered with something the app cannot use.",
  checkError: "Could not check for updates.",
  notChanged: "The update source was not changed: {reason}",
  notesError: "Could not show the release notes: {error}. Check for updates again later.",
});

const megabytes = (n: number): string => `${(n / 1048576).toFixed(1)} MB`;

/** The reason the updater gives when this build has no updater key: nothing to offer, and not an error. */
const notConfigured = (reason?: string | null): boolean => !!reason && /not configured/i.test(reason);

/** Why the app cannot offer an update, in plain words: the feed's own refusal for this computer, else the native text. */
function reasonText(reason: string): string {
  const m = /^Version (\S+) has no update for this system/.exec(reason);
  return m ? t("noDownload", { version: m[1] as string }) : reason;
}

/** What the app update section shows apart from its version line: a task, a result or a copy to go back to. */
function updateHasMore(u: AppUpdateView): boolean {
  return (
    u.available ||
    u.task.phase !== "idle" ||
    !!u.last ||
    u.rollback.length > 0 ||
    (!!u.reason && !notConfigured(u.reason))
  );
}

const PHASE: Record<Exclude<AppUpdateView["task"]["phase"], "downloading">, () => string> = {
  idle: () => t("waiting"),
  confirming: () => t("confirming"),
  verifying: () => t("verifying"),
  installing: () => t("installing"),
  restarting: () => t("restarting"),
  failed: () => t("failed"),
  cancelled: () => t("cancelled"),
};

function phaseText(task: AppUpdateView["task"]): string {
  if (task.phase !== "downloading") return PHASE[task.phase]();
  return task.total
    ? t("downloading", { done: megabytes(task.downloaded), total: megabytes(task.total) })
    : t("downloadingUnknown", { done: megabytes(task.downloaded) });
}

/** Update now, its progress and Cancel download, the last outcome, and Go back to. */
function AppUpdate(props: {
  update: AppUpdateView;
  check: number;
  managed: boolean;
  onError: (e: string) => void;
}) {
  const u = props.update;
  const task = u.task;
  const call = (command: string, args?: Record<string, unknown>) =>
    invoke(command, args).catch((e) => props.onError(String(e)));
  // An update or a rollback runs, or waits for its native confirmation.
  const busy = task.phase !== "idle" && task.phase !== "failed" && task.phase !== "cancelled";
  const more = updateHasMore(u);
  return (
    // The carrier is always there for what reads the updater's view; its section only when it has something to say.
    <div
      data-part="app-update"
      data-available={String(u.available)}
      data-reason={u.reason ?? ""}
      data-phase={task.phase}
      data-downloaded={String(task.downloaded)}
      data-total={task.total == null ? "" : String(task.total)}
      data-error={task.error ?? ""}
      data-check={String(props.check)}
    >
      {more && (
        <Section title={t("appUpdate")}>
          <div class="update-body">
            {u.reason && !notConfigured(u.reason) && <Notice tone="warn">{reasonText(u.reason)}</Notice>}
            {u.available && u.version && !busy && (
              <>
                <p>{t("ready", { version: u.version })}</p>
                <p class="muted">{t(props.managed ? "restartManaged" : "restartOther")}</p>
                <div class="actions">
                  <span data-part="app-update-now">
                    <Button variant="primary" onClick={() => call("app_update_start")}>
                      {t("updateNow")}
                    </Button>
                  </span>
                </div>
              </>
            )}
            {task.phase !== "idle" && (
              <p role="status">
                <strong>{phaseText(task)}</strong>
                {task.version ? ` · ${task.version}` : ""}
                {task.error ? ` · ${task.error}` : ""}
              </p>
            )}
            {task.phase === "downloading" && task.total ? (
              <progress class="update-progress" value={task.downloaded} max={task.total} />
            ) : null}
            {task.phase === "downloading" && (
              <div class="actions">
                <span data-part="app-update-cancel">
                  <Button onClick={() => call("app_update_cancel")}>{t("cancel")}</Button>
                </span>
              </div>
            )}
            {u.last && (
              <div data-part="app-update-last" data-result={u.last.result} data-detail={u.last.detail}>
                <Notice tone={u.last.result === "failed" ? "danger" : "ok"}>{u.last.detail}</Notice>
              </div>
            )}
            {u.rollback.length > 0 && (
              <>
                <p class="muted">{t("goBackAbout")}</p>
                <div class="actions">
                  {u.rollback.map((p) => (
                    <span
                      key={p.version}
                      data-part="app-rollback"
                      data-version={p.version}
                      data-sha256={p.sha256}
                    >
                      <Button
                        disabled={busy}
                        onClick={() => call("app_update_rollback", { version: p.version, sha256: p.sha256 })}
                      >
                        {t("goBack", { version: p.version })}
                      </Button>
                    </span>
                  ))}
                </div>
              </>
            )}
          </div>
        </Section>
      )}
    </div>
  );
}

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** The part of el the person can see: inside the viewport and inside the frame's pane, the part
 * of the window that scrolls, so never over the header strip or the navigation. None when
 * nothing of el shows, so its notes webview hides rather than cover the rest. */
function visibleRect(el: Element): Rect | null {
  const r = el.getBoundingClientRect();
  const view = document.documentElement;
  const pane = el.closest("[data-part=main]")?.getBoundingClientRect();
  const x = Math.max(r.left, pane?.left ?? 0, 0);
  const y = Math.max(r.top, pane?.top ?? 0, 0);
  const width = Math.min(r.right, pane?.right ?? Infinity, view.clientWidth) - x;
  const height = Math.min(r.bottom, pane?.bottom ?? Infinity, view.clientHeight) - y;
  return width >= 1 && height >= 1 ? { x, y, width, height } : null;
}

/** The theme's values for the notes' style sheet; the native side checks each again. */
function notesStyle(el: Element) {
  const s = getComputedStyle(el);
  const v = (name: string) => s.getPropertyValue(`--px-${name}`).trim();
  return {
    text: v("color-text"),
    muted: v("color-text-muted"),
    surface: v("color-surface"),
    border: v("color-border"),
    accent: v("color-accent"),
    font: v("font-body"),
    mono: v("font-mono"),
    size: v("font-size"),
  };
}

/** The notes of product's latest release, in a webview of their own over this area. */
function NotesView(props: {
  product: Product;
  version: string;
  channel: Channel;
  checked: number;
  theme: number;
}) {
  const area = useRef<HTMLElement>(null);
  const placeNow = useRef<(() => void) | undefined>(undefined);
  const [label, setLabel] = useState<string>();
  const [error, setError] = useState<string>();

  // The page moves the area without resizing or scrolling it when something above it comes or
  // goes (a notice), so it is placed again after every render, and when any block around it
  // changes size.
  useLayoutEffect(() => placeNow.current?.());

  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    let live = true;
    let opened: string | undefined;
    setLabel(undefined);
    setError(undefined);
    const place = () => {
      if (opened && area.current)
        invoke("release_notes_place", { label: opened, rect: visibleRect(area.current) }).catch(() => {});
    };
    placeNow.current = place;
    const resize = new ResizeObserver(place);
    invoke<string>("release_notes_open", {
      product: props.product,
      rect: visibleRect(el),
      style: notesStyle(el),
    }).then(
      (l) => {
        if (!live) {
          invoke("release_notes_close", { label: l }).catch(() => {});
          return;
        }
        opened = l;
        setLabel(l);
        resize.observe(el);
        for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) resize.observe(p);
        window.addEventListener("resize", place);
        window.addEventListener("scroll", place, true);
      },
      (e) => live && setError(String(e)),
    );
    return () => {
      live = false;
      placeNow.current = undefined;
      resize.disconnect();
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      if (opened) invoke("release_notes_close", { label: opened }).catch(() => {});
    };
  }, [props.product, props.version, props.channel, props.checked, props.theme]);

  return (
    <>
      {error && <Notice tone="danger">{t("notesError", { error })}</Notice>}
      <section
        ref={area}
        class="release-notes"
        data-part="release-notes"
        data-product={props.product}
        data-label={label ?? ""}
        aria-label="Release notes"
        style={{ height: "320px" }}
      />
    </>
  );
}

/** The release notes of the latest app release: a heading naming the version, Show or Hide, and the full notes. */
function ReleaseNotes(props: {
  view: ProductView;
  channel: Channel;
  checked: number;
  theme: number;
  onError: (e: string) => void;
}) {
  const [shown, setShown] = useState(true);
  const l = props.view.latest;
  if (!l || !(l.has_notes || l.has_notes_url)) return null;
  const full = () => invoke("release_notes_full", { product: "app" }).catch((e) => props.onError(String(e)));
  return (
    <Section
      title={t("notesFor", { version: l.version })}
      aside={
        <>
          {l.has_notes && (
            <span data-part="release-notes-toggle" data-product="app">
              <Button onClick={() => setShown((x) => !x)}>{shown ? t("hide") : t("show")}</Button>
            </span>
          )}
          {l.has_notes_url && (
            <span data-part="release-notes-full" data-product="app">
              <Button onClick={full}>{t("openFull")}</Button>
            </span>
          )}
        </>
      }
    >
      {l.has_notes && shown && (
        <NotesView
          product="app"
          version={l.version}
          channel={props.channel}
          checked={props.checked}
          theme={props.theme}
        />
      )}
    </Section>
  );
}

/** One product's row: what is installed, and, once the source was asked, how it compares with the latest release. */
function VersionRow(props: {
  product: Product;
  name: string;
  about?: string;
  view: ProductView;
  /** What to show as the installed version instead of the number: pluxy not being connected. */
  installed?: string;
  sourceSet: boolean;
  checked: boolean;
  failed: boolean;
}) {
  const l = props.view.latest;
  const status = !props.sourceSet ? undefined : !props.checked ? (
    <Badge tone={props.failed ? "danger" : "muted"}>
      {props.failed ? t("checkFailed") : t("notChecked")}
    </Badge>
  ) : l?.newer === true ? (
    <Badge tone="warn">{t("available", { version: l.version })}</Badge>
  ) : l?.newer === false ? (
    <Badge tone="ok">{t("upToDate")}</Badge>
  ) : undefined;
  const published = l && when(l.published);
  return (
    <div
      data-part="release-product"
      data-product={props.product}
      data-installed={props.view.installed ?? ""}
      data-latest={l?.version ?? ""}
    >
      <SettingRow name={props.name} description={props.about}>
        <div class="version-facts">
          <span class="version-line">
            {props.installed ? (
              <span class="muted">{props.installed}</span>
            ) : (
              <span class="mono">{props.view.installed ?? t("unknown")}</span>
            )}
            {status}
          </span>
          {props.sourceSet && props.checked && (
            <span class="version-latest muted">
              {l ? (
                <>
                  {l.newer == null && <span>{t("latest", { version: l.version })}</span>}
                  {l.channel === "preview" && <Badge tone="muted">{t("preview")}</Badge>}
                  {published && <span>{rich(t("published", { date: "{date}" }), { date: published })}</span>}
                </>
              ) : (
                t("noneOnChannel")
              )}
            </span>
          )}
        </div>
      </SettingRow>
    </div>
  );
}

const WINDOW_EVENT_DONE = ["idle", "failed", "cancelled"];

interface Both {
  view: ReleaseView;
  update: AppUpdateView;
}

/** Both native views, read together: the page draws once both answer. */
async function readBoth(): Promise<Reply<Both>> {
  const [view, update] = await Promise.all([
    reply<ReleaseView>("release_get"),
    reply<AppUpdateView>("app_update_get"),
  ]);
  if (view.body === undefined || update.body === undefined)
    return {
      status: view.body === undefined ? view.status : update.status,
      error: view.error ?? update.error,
    };
  return { status: 200, body: { view: view.body, update: update.body } };
}

/** How the pluxy row says where its updates come from. */
function pluxyAbout(c: Connection): string {
  if (!c.target || !c.signed_in) return t("notConnected");
  if (c.mode === "managed") return t("managed");
  if (c.mode === "remote") return t("remote", { host: new URL(c.target).host });
  return t("local");
}

export function UpdatesPage(props: { connection: Connection }) {
  const themes = useThemeState();
  const loaded = useLoad(readBoth);
  const [view, setView] = useState<ReleaseView>();
  const [update, setUpdate] = useState<AppUpdateView>();
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  // Why a check did not run, why the source or the channel was not changed, and why the updater did not start.
  const [checkError, setCheckError] = useState<string>();
  const [sourceError, setSourceError] = useState<string>();
  const [updateError, setUpdateError] = useState<string>();
  const [address, setAddress] = useState("");
  const [theme, setTheme] = useState(0);
  // Counts checks whose update state has arrived, so a reader knows it is this check's.
  const [checks, setChecks] = useState(0);

  // What the page read, until something it did gives newer views.
  useEffect(() => {
    if (!loaded.data) return;
    setView(loaded.data.view);
    setUpdate(loaded.data.update);
  }, [loaded.data]);
  const v = view ?? loaded.data?.view;
  const u = update ?? loaded.data?.update;

  const refreshUpdate = () =>
    invoke<AppUpdateView>("app_update_get").then(setUpdate, (e) => setUpdateError(String(e)));

  useEffect(() => {
    // A source change is settled in its native confirmation; one confirmed but not applied is an error.
    const off = listen<Settled>("pluxy-release", async (e) => {
      await invoke<ReleaseView>("release_get").then(setView, (err) => setSourceError(String(err)));
      if (e.payload?.error) setSourceError(t("notChanged", { reason: e.payload.error }));
      else if (e.payload?.changed) {
        setSourceError(undefined);
        setAddress("");
      }
    });
    // The event carries only the task; when it ends, the rest (last outcome, kept copies) is read again.
    const offUpdate = listen<{ task: AppUpdateView["task"] }>("pluxy-app-update", (e) => {
      const task = e.payload.task;
      setUpdate((cur) => (cur ? { ...cur, task } : cur));
      if (WINDOW_EVENT_DONE.includes(task.phase)) refreshUpdate();
    });
    const unsubscribe = themes.subscribe(() => setTheme((n) => n + 1));
    return () => {
      unsubscribe();
      for (const o of [off, offUpdate])
        o.then(
          (f) => f(),
          () => {},
        );
    };
  }, []);

  const check = async () => {
    setChecking(true);
    setCheckError(undefined);
    try {
      setView(await invoke<ReleaseView>("release_check"));
    } catch (e) {
      setCheckError(String(e));
    }
    await refreshUpdate();
    setChecks((n) => n + 1);
    setChecking(false);
  };

  const change = async (call: () => Promise<ReleaseView | undefined>, explain: (e: string) => string) => {
    setBusy(true);
    setSourceError(undefined);
    try {
      const next = await call();
      if (next) setView(next);
    } catch (e) {
      setSourceError(explain(String(e)));
    } finally {
      setBusy(false);
    }
  };
  const changeSource = (source: string | null) =>
    change(
      async () => {
        await invoke("release_source_set", { source });
        return invoke<ReleaseView>("release_get");
      },
      (reason) => t("notChanged", { reason }),
    );

  const checked = !!v?.checked_at;
  const c = props.connection;
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <span data-part="release-check">
            <Button variant="primary" disabled={busy || checking || !v?.source} onClick={check}>
              {checking ? t("checking") : t("check")}
            </Button>
          </span>
        }
      />
      <Phases
        load={loaded}
        loading={t("reading")}
        failed={{ title: t("readFailed") }}
        isEmpty={() => false}
        empty={null}
      >
        {() =>
          v && u ? (
            <div class="update-page">
              {!v.source && <Intro>{t("noSource")}</Intro>}
              {checkError && (
                <Notice tone="danger">
                  {t("checkError")}
                  <Details text={checkError} />
                </Notice>
              )}
              {v.problem && (
                <div data-part="release-problem" data-offline={String(v.problem.offline)}>
                  {v.problem.offline ? (
                    <Notice tone="warn">
                      {v.checked_at ? t("offlineAt", { time: clock(v.checked_at * 1000) }) : t("offline")}
                    </Notice>
                  ) : (
                    <Notice tone="danger">
                      {t("unusable")}
                      <Details text={v.problem.message} />
                    </Notice>
                  )}
                </div>
              )}
              <Section title={t("versions")}>
                <VersionRow
                  product="app"
                  name={t("thisApp")}
                  about={notConfigured(u.reason) ? t("cannotUpdate") : undefined}
                  view={v.app}
                  sourceSet={!!v.source}
                  checked={checked}
                  failed={!!v.problem}
                />
                <VersionRow
                  product="core"
                  name={t("pluxy")}
                  about={pluxyAbout(c)}
                  installed={!c.target || !c.signed_in ? t("notConnected") : undefined}
                  view={v.core}
                  sourceSet={!!v.source}
                  checked={checked}
                  failed={!!v.problem}
                />
              </Section>
              <AppUpdate update={u} check={checks} managed={c.mode === "managed"} onError={setUpdateError} />
              {updateError && <Notice tone="danger">{updateError}</Notice>}
              <ReleaseNotes
                view={v.app}
                channel={v.channel}
                checked={v.checked_at ?? 0}
                theme={theme}
                onError={setUpdateError}
              />
              <Section title={t("source")}>
                <SettingRow name={t("channel")} description={t("channelAbout")}>
                  {/* Changing the channel while a source change waits would void that change. */}
                  <div data-part="release-channel">
                    <Choices
                      label={t("channel")}
                      value={v.channel}
                      disabled={v.changing_source}
                      choices={[
                        { value: "stable" as const, label: t("stable") },
                        { value: "preview" as const, label: t("preview") },
                      ]}
                      onChange={(channel) =>
                        change(
                          () => invoke<ReleaseView>("release_channel_set", { channel }),
                          (e) => e,
                        )
                      }
                    />
                  </div>
                </SettingRow>
                <div
                  data-part="release-source"
                  data-source={v.source ?? ""}
                  data-changing={String(v.changing_source)}
                >
                  <SettingRow name={t("source")}>
                    <div class="version-facts">
                      <span class={v.source ? "mono" : "muted"}>{v.source ?? t("noneSet")}</span>
                      <span
                        class="muted"
                        data-part="release-checked"
                        data-at={v.checked_at ? String(v.checked_at) : ""}
                      >
                        {v.checked_at
                          ? rich(t("lastChecked", { time: "{time}" }), { time: when(v.checked_at) })
                          : t("neverChecked")}
                      </span>
                    </div>
                  </SettingRow>
                  <SettingRow
                    name={t("address")}
                    description={t("addressAbout")}
                    disabled={v.changing_source ? t("answerWindow") : undefined}
                  >
                    <div class="source-form">
                      <TextField
                        label={t("address")}
                        data="release-source-input"
                        value={address}
                        placeholder="https://"
                        onInput={setAddress}
                      />
                      <div class="actions">
                        <span data-part="release-source-change">
                          <Button
                            disabled={busy || v.changing_source || address.trim() === ""}
                            onClick={() => changeSource(address.trim())}
                          >
                            {v.source ? t("changeSource") : t("setSource")}
                          </Button>
                        </span>
                        {v.source && (
                          <Button disabled={busy || v.changing_source} onClick={() => changeSource(null)}>
                            {t("removeSource")}
                          </Button>
                        )}
                      </div>
                    </div>
                  </SettingRow>
                </div>
                {sourceError && (
                  <div data-part="release-error">
                    <Notice tone="danger">{sourceError}</Notice>
                  </div>
                )}
              </Section>
            </div>
          ) : null
        }
      </Phases>
    </>
  );
}

export const updatesPage = (connection: Connection): PageDef => ({
  id: "versions",
  title: t("title"),
  dest: "settings",
  kind: "topic",
  section: "device",
  tip: t("tip"),
  render: () => <UpdatesPage connection={connection} />,
});
