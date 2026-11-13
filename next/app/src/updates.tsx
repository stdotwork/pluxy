// The app's Version Management page: the current and latest versions of the app and of core,
// from the release feed the native side reads (src-tauri/src/release.rs), on the Stable or the
// Preview (latest) channel, and the app release's notes. The notes never render in this webview:
// the native side opens a webview of their own over the notes area, with no app command granted
// (src-tauri/src/notes.rs). This page gets only versions, dates and status from the native side.

import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Badge, Button, Card, Choices, Facts, Notice, PageHead, useThemeState, when } from "../../ui/kit/ui";

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

/** What the native side says about the feed; no notes text and no addresses but the feed's. */
export interface ReleaseView {
  source?: string | null;
  channel: Channel;
  app: ProductView;
  core: ProductView;
  checked_at?: number | null;
  problem?: { offline: boolean; message: string } | null;
  changing_source: boolean;
}

/** How a feed change was settled (`pluxy-release`). */
interface Settled {
  changed: boolean;
  error?: string | null;
}

const CHANNELS = [
  { value: "stable" as const, label: "Stable" },
  { value: "preview" as const, label: "Preview (latest)" },
];

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** The part of el the person can see: inside the viewport and below a navigation bar pinned at
 * its top. None when nothing of el shows, so its notes webview hides rather than cover the rest. */
function visibleRect(el: Element): Rect | null {
  const r = el.getBoundingClientRect();
  let top = 0;
  const nav = document.querySelector("[data-part=nav]");
  if (nav) {
    const n = nav.getBoundingClientRect();
    const position = getComputedStyle(nav).position;
    const pinned = (position === "sticky" || position === "fixed") && n.top <= 0.5;
    if (pinned && n.left < r.right && n.right > r.left) top = Math.max(top, n.bottom);
  }
  const view = document.documentElement;
  const x = Math.max(r.left, 0);
  const y = Math.max(r.top, top);
  const width = Math.min(r.right, view.clientWidth) - x;
  const height = Math.min(r.bottom, view.clientHeight) - y;
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
      {error && (
        <Notice tone="danger">Unable to load release notes. Please check again later. ({error})</Notice>
      )}
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

/** Release notes, as the old app showed them: expanded at first, with a link to the full notes. */
function ReleaseNotes(props: {
  product: Product;
  view: ProductView;
  listed: boolean;
  channel: Channel;
  checked: number;
  theme: number;
  onError: (e: string) => void;
}) {
  const [expanded, setExpanded] = useState(true);
  const l = props.view.latest;
  const full = () =>
    invoke("release_notes_full", { product: props.product }).catch((e) => props.onError(String(e)));
  return (
    <Card
      title="Release notes"
      actions={
        <span data-part="release-notes-toggle" data-product={props.product}>
          <Button onClick={() => setExpanded((x) => !x)}>{expanded ? "Collapse" : "Expand"}</Button>
        </span>
      }
    >
      {expanded && (
        <div class="stack">
          {l && (
            <p>
              <strong class="mono">v{l.version}</strong> Latest Version
              {when(l.published) ? ` · Published ${when(l.published)}` : ""}
            </p>
          )}
          {l?.has_notes ? (
            <NotesView
              product={props.product}
              version={l.version}
              channel={props.channel}
              checked={props.checked}
              theme={props.theme}
            />
          ) : (
            <p class="empty" role="status">
              {l
                ? "This release has no notes."
                : props.listed
                  ? "No release on this channel."
                  : "Check for software updates to see the latest release notes."}
            </p>
          )}
          {l?.has_notes_url && (
            <div class="actions">
              <span data-part="release-notes-full" data-product={props.product}>
                <Button onClick={full}>View full release notes</Button>
              </span>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

function ProductCard(props: {
  product: Product;
  title: string;
  view: ProductView;
  checked: boolean;
  failed: boolean;
}) {
  const l = props.view.latest;
  const status = !props.checked ? (
    <Badge tone={props.failed ? "danger" : "muted"}>{props.failed ? "Check Failed" : "Not Checked"}</Badge>
  ) : l?.newer === true ? (
    <Badge tone="warn">Version {l.version} is available</Badge>
  ) : l?.newer === false ? (
    <Badge tone="ok">Up to Date</Badge>
  ) : undefined;
  return (
    <div
      data-part="release-product"
      data-product={props.product}
      data-installed={props.view.installed ?? ""}
      data-latest={l?.version ?? ""}
    >
      <Card title={props.title} actions={status}>
        <Facts
          rows={[
            ["Current Version", <span class="mono">{props.view.installed ?? "unknown"}</span>],
            [
              "Latest Version",
              l ? (
                <span>
                  <span class="mono">{l.version}</span>{" "}
                  {l.channel === "preview" && <Badge tone="muted">Preview</Badge>}
                </span>
              ) : props.checked ? (
                "none on this channel"
              ) : (
                "Not Checked"
              ),
            ],
            ["Published", l ? when(l.published) : undefined],
          ]}
        />
      </Card>
    </div>
  );
}

export function UpdatesPage() {
  const themes = useThemeState();
  const [view, setView] = useState<ReleaseView>();
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string>();
  const [address, setAddress] = useState("");
  const [theme, setTheme] = useState(0);

  const run = async (call: () => Promise<ReleaseView | undefined>) => {
    setBusy(true);
    setError(undefined);
    try {
      const v = await call();
      if (v) setView(v);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const reload = () => run(() => invoke<ReleaseView>("release_get"));
  const check = async () => {
    setChecking(true);
    await run(() => invoke<ReleaseView>("release_check"));
    setChecking(false);
  };

  useEffect(() => {
    reload();
    // A feed change is settled in its native confirmation; one confirmed but not applied is an error.
    const off = listen<Settled>("pluxy-release", async (e) => {
      await reload();
      if (e.payload?.error) setError(`The release feed was not changed: ${e.payload.error}`);
    });
    const unsubscribe = themes.subscribe(() => setTheme((n) => n + 1));
    return () => {
      unsubscribe();
      off.then(
        (f) => f(),
        () => {},
      );
    };
  }, []);

  const changeSource = (source: string | null) =>
    run(async () => {
      await invoke("release_source_set", { source });
      return invoke<ReleaseView>("release_get");
    });

  const v = view;
  const checked = !!v?.checked_at;
  const failed = !!v?.problem;
  return (
    <>
      <PageHead
        title="Version Management"
        actions={
          <span data-part="release-check">
            <Button variant="primary" disabled={busy || !v?.source} onClick={check}>
              {checking ? "Checking" : "Check for Updates"}
            </Button>
          </span>
        }
      />
      {error && (
        <div data-part="release-error">
          <Notice tone="danger">{error}</Notice>
        </div>
      )}
      {v?.problem && (
        <div data-part="release-problem" data-offline={String(v.problem.offline)}>
          <Notice tone={v.problem.offline ? "warn" : "danger"}>
            {v.problem.message}
            {v.checked_at ? ` Showing the releases from the last check, ${when(v.checked_at)}.` : ""}
          </Notice>
        </div>
      )}
      {v && (
        <div class="stack">
          <ProductCard
            product="app"
            title="Desktop Application"
            view={v.app}
            checked={checked}
            failed={failed}
          />
          <ProductCard product="core" title="Core" view={v.core} checked={checked} failed={failed} />
          <ReleaseNotes
            product="app"
            view={v.app}
            listed={checked}
            channel={v.channel}
            checked={v.checked_at ?? 0}
            theme={theme}
            onError={setError}
          />
          {/* Changing the channel while a feed change waits would void that change. */}
          <fieldset
            data-part="release-channel"
            disabled={v.changing_source}
            style={{ border: 0, padding: 0, margin: 0, minWidth: 0 }}
          >
            <Choices
              label="Channel"
              value={v.channel}
              choices={CHANNELS}
              onChange={(channel) => run(() => invoke<ReleaseView>("release_channel_set", { channel }))}
            />
          </fieldset>
          <div
            data-part="release-source"
            data-source={v.source ?? ""}
            data-changing={String(v.changing_source)}
          >
            <Card title="Release feed">
              <Facts
                rows={[
                  ["Feed", v.source ? <span class="mono">{v.source}</span> : "none set"],
                  [
                    "Last checked",
                    <span data-part="release-checked" data-at={v.checked_at ? String(v.checked_at) : ""}>
                      {v.checked_at ? when(v.checked_at) : "never"}
                    </span>,
                  ],
                ]}
              />
              <label class="field" data-part="field">
                <span class="label">Feed address</span>
                <input
                  type="text"
                  data-part="release-source-input"
                  value={address}
                  placeholder="https://"
                  autocomplete="off"
                  spellcheck={false}
                  onInput={(e) => setAddress(e.currentTarget.value)}
                />
                <small class="hint">
                  The app has no built-in feed. Changing it, or removing it, asks you to confirm in a native
                  dialog.
                </small>
              </label>
              <div class="actions">
                <span data-part="release-source-change">
                  <Button
                    disabled={busy || v.changing_source || address.trim() === ""}
                    onClick={() => changeSource(address.trim())}
                  >
                    Change feed…
                  </Button>
                </span>
                {v.source && (
                  <Button disabled={busy || v.changing_source} onClick={() => changeSource(null)}>
                    Remove feed…
                  </Button>
                )}
              </div>
              {v.changing_source && (
                <Notice tone="muted">Waiting for your confirmation in the native dialog.</Notice>
              )}
            </Card>
          </div>
        </div>
      )}
    </>
  );
}
