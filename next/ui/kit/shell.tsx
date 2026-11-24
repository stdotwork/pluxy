// The kit's shell: signing in, navigation and the page area, laid out by the theme's layout
// options. A host build passes the pages it offers; the shell knows nothing else about them.

import type { VNode } from "preact";
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "./api";
import { ExtensionPage, type ExtensionPageProps } from "./extensions/page";
import type { Host, Reply } from "./host";
import { PrivacyContext, type PrivacyState, privacyMasks } from "./privacy";
import { strings } from "./strings";
import type { ThemeState } from "./theme/state";
import { Button, type Kit, KitContext, Notice } from "./ui";

const t = strings("shell", {
  signInHint: "Sign in with the management key from core's config.",
  key: "Management key",
  remember: "Remember on this device",
  signIn: "Sign in",
  refused: "Core refused the management key. Sign in again.",
  navigation: "Main",
  signOut: "Sign out",
  signOutShort: "Out",
  safeAddress: "If custom CSS hides the panel, add ?safe to the URL to disable it.",
  privacy: "Privacy mode",
  privacyShort: "Priv",
  privacyEnable: "Turn on privacy mode to hide account emails and identifiers",
  privacyDisable: "Turn off privacy mode to show full account details",
  "group.operate": "Operate",
  "group.gateway": "Gateway",
  "group.observe": "Observe",
  "group.control": "Control",
  "group.extensions": "Extension pages",
});

/**
 * The navigation's groups, in the order it shows them; a group that holds no page is not shown.
 * Extension pages are always in "extensions".
 */
export const PAGE_GROUPS = ["operate", "gateway", "observe", "control", "extensions"] as const;
export type PageGroup = (typeof PAGE_GROUPS)[number];

/**
 * A page in the navigation. Extension pages are pages too: their render mounts the extension's
 * isolated document, so the shell and the theme hooks treat them like any other.
 */
export interface PageDef {
  /**
   * Stable ID: the address is #/<id>, and the root element carries data-page="<id>". An
   * extension page's ID is ext/<publisher>/<name>/<page>.
   */
  id: string;
  title: string;
  /** A label of up to about six characters, for the rail navigation. */
  short: string;
  /** The navigation group the page is listed in. */
  group: PageGroup;
  /** The navigation entry's tooltip, a few words on what the page is for. Default: the title. */
  tip?: string;
  render: () => VNode;
}

/**
 * Asked before the shell follows a change of the address's hash to another page: it returns true
 * to stay, and calls go later to follow the change after all. A page with unsaved changes adds
 * one with guardLeave.
 */
export type LeaveGuard = (go: () => void) => boolean;

const leaveGuards = new Set<LeaveGuard>();

/** Adds a leave guard; the function it returns removes it. */
export function guardLeave(guard: LeaveGuard): () => void {
  leaveGuards.add(guard);
  return () => leaveGuards.delete(guard);
}

/** The Setup page's ID: the one page the shell opens by itself, and hides when core lacks its calls. */
export const SETUP_PAGE = "setup";

/**
 * What the shell does with core's answer to GET /setup once signed in. offered: the Setup page
 * is in the navigation; a core without the call (404) gets none. open: Setup opens in place of
 * the first page, on a first run when the address (hash) names no page.
 */
export function setupGate(
  reply: Reply<{ first_run: boolean }>,
  hash: string,
): { offered: boolean; open: boolean } {
  if (reply.status === 404) return { offered: false, open: false };
  return { offered: true, open: reply.body?.first_run === true && hash.replace(/^#\/?/, "") === "" };
}

function currentId(pages: PageDef[]): string {
  const path = location.hash.replace(/^#\/?/, "");
  const match = pages.filter((p) => path === p.id || path.startsWith(`${p.id}/`));
  const longest = match.sort((a, b) => b.id.length - a.id.length)[0];
  return longest?.id ?? pages[0]?.id ?? "";
}

/** Draws one extension page. The panel's default shows it in a sandboxed frame. */
export type ExtensionPageView = (props: ExtensionPageProps) => VNode;

/** The pages of the extensions that run, from core. */
function useExtensionPages(on: boolean, kit: Kit, version: number, View: ExtensionPageView): PageDef[] {
  const [defs, setDefs] = useState<PageDef[]>([]);
  useEffect(() => {
    if (!on) {
      setDefs([]);
      return;
    }
    let live = true;
    kit.api.ui().then((r) => {
      if (!live) return;
      if (r.status === 401) return kit.unauthorized();
      const ui = r.body;
      if (!ui) return;
      setDefs(
        ui.extensions.flatMap((ext) =>
          ext.pages.map((page) => ({
            id: `ext/${ext.id}/${page.id}`,
            title: page.title,
            short: page.short,
            group: "extensions" as const,
            render: () => (
              <View ext={ext} page={page} pagesOrigin={ui.pages_origin} pagesError={ui.pages_error} />
            ),
          })),
        ),
      );
    });
    return () => {
      live = false;
    };
  }, [on, kit, version, View]);
  return defs;
}

function SignIn(props: { host: Host; onDone: () => void; reason?: string; safeAddress?: boolean }) {
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState(props.reason);
  const [busy, setBusy] = useState(false);
  return (
    <main class="signin" data-part="main">
      <form
        class="card"
        data-part="card"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const why = await props.host.signIn(key, remember);
          setBusy(false);
          if (why) setError(why);
          else props.onDone();
        }}
      >
        <h1 data-part="page-title">pluxy</h1>
        <p class="muted">{t("signInHint")}</p>
        <label class="field" data-part="field">
          <span class="label">{t("key")}</span>
          <input
            data-slot="input"
            type="password"
            autocomplete="current-password"
            required
            value={key}
            onInput={(e) => setKey(e.currentTarget.value)}
          />
        </label>
        <label class="check">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.currentTarget.checked)} />
          <span>{t("remember")}</span>
        </label>
        {error && <Notice tone="danger">{error}</Notice>}
        <Button type="submit" variant="primary" disabled={busy || key === ""}>
          {t("signIn")}
        </Button>
        {props.safeAddress && <small class="hint">{t("safeAddress")}</small>}
      </form>
    </main>
  );
}

/** Privacy mode's state, drawing again when it changes. */
function usePrivacyOn(privacy: PrivacyState): boolean {
  const [on, setOn] = useState(privacy.on);
  useEffect(() => {
    setOn(privacy.on);
    return privacy.subscribe(() => setOn(privacy.on));
  }, [privacy]);
  return on;
}

export function Shell(props: {
  host: Host;
  themes: ThemeState;
  /** Privacy mode, kept where the host build keeps it. */
  privacy: PrivacyState;
  pages: PageDef[];
  brand?: string;
  /** Adds the pages of the extensions that run, after the build's own. */
  extensionPages?: boolean;
  /**
   * Draws an extension page, for a host that shows pages its own way: the desktop app puts each
   * in a webview of its own. Default: a sandboxed frame in the page area.
   */
  extensionPage?: ExtensionPageView;
  /**
   * The host opens without custom CSS when its address ends in ?safe (the panel does): the
   * sign-in page says so, where custom CSS applies too.
   */
  safeAddress?: boolean;
}) {
  const { host, themes, privacy } = props;
  const privacyOn = usePrivacyOn(privacy);
  const masks = useMemo(() => privacyMasks(privacyOn), [privacyOn]);
  useEffect(() => {
    document.documentElement.dataset.privacy = privacyOn ? "on" : "off";
  }, [privacyOn]);
  const [signedIn, setSignedIn] = useState(host.signedIn());
  const [reason, setReason] = useState<string>();
  const [uiVersion, setUiVersion] = useState(0);

  const unauthorized = useCallback(() => {
    host.signOut();
    setReason(t("refused"));
    setSignedIn(false);
  }, [host]);
  const extensionsChanged = useCallback(() => setUiVersion((n) => n + 1), []);
  // Core keeps the themes: the theme state follows it while signed in.
  useEffect(() => (signedIn ? themes.connect(unauthorized) : undefined), [signedIn, themes, unauthorized]);
  const kit = useMemo(
    () => ({ host, api: api(host), themes, privacy, unauthorized, extensionsChanged }),
    [host, themes, privacy, unauthorized, extensionsChanged],
  );
  const extPages = useExtensionPages(
    signedIn && props.extensionPages === true,
    kit,
    uiVersion,
    props.extensionPage ?? ExtensionPage,
  );
  // The shell does not wait for this answer: the first page may show for an instant.
  const hasSetup = props.pages.some((p) => p.id === SETUP_PAGE);
  const [setupOffered, setSetupOffered] = useState(true);
  useEffect(() => {
    if (!signedIn || !hasSetup) return;
    let live = true;
    kit.api.setup().then((r) => {
      if (!live) return;
      // A refused key is left to the page that loads next: this read is the shell's own, and
      // signing out here could undo a sign-in that is under way in the desktop app.
      if (r.status === 401) return;
      const gate = setupGate(r, location.hash);
      setSetupOffered(gate.offered);
      if (gate.open) location.replace(`#/${SETUP_PAGE}`);
    });
    return () => {
      live = false;
    };
  }, [signedIn, hasSetup, kit]);
  const pages = useMemo(
    () => [...props.pages.filter((p) => setupOffered || p.id !== SETUP_PAGE), ...extPages],
    [props.pages, extPages, setupOffered],
  );
  const [page, setPage] = useState(() => currentId(pages));

  useEffect(() => {
    const onHash = (e?: HashChangeEvent) => {
      if (e) {
        const go = () => location.assign(e.newURL);
        for (const guard of leaveGuards) {
          if (guard(go)) {
            history.replaceState(null, "", e.oldURL);
            return;
          }
        }
      }
      setPage(currentId(pages));
    };
    onHash(); // extension pages arrive after the first render
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [pages]);

  // A change of page opens the new page at its top: the window keeps its scroll position from one
  // page to the next. A change inside a page (a tab or a filter in the address) keeps it.
  // useLayoutEffect resets the scroll synchronously on commit, before paint and before any
  // interaction with the new page can race with a deferred effect.
  const shownPage = useRef(page);
  useLayoutEffect(() => {
    if (shownPage.current === page) return;
    shownPage.current = page;
    window.scrollTo(0, 0);
  }, [page]);

  const active = pages.find((p) => p.id === page);
  const navId = useId();
  // Laid out as a bar (the theme's top navigation, or any navigation in a narrow window), the
  // navigation scrolls sideways when it is wider than the window: the current page's entry is
  // brought into view when the page opens, and when the entries or the bar's size change.
  const navRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const bar = navRef.current;
    if (!bar) return;
    const show = () => {
      const entry = bar.querySelector('[aria-current="page"]');
      if (!entry || bar.scrollWidth <= bar.clientWidth) return;
      const b = bar.getBoundingClientRect();
      const e = entry.getBoundingClientRect();
      // Past the entry by a little, so the cue at the bar's edge does not cover it.
      const margin = 24;
      if (e.left < b.left + margin) bar.scrollLeft -= b.left + margin - e.left;
      else if (e.right > b.right - margin) bar.scrollLeft += e.right - (b.right - margin);
    };
    show();
    if (typeof ResizeObserver === "undefined") return;
    const sizes = new ResizeObserver(show);
    sizes.observe(bar);
    return () => sizes.disconnect();
  }, [page, signedIn, pages]);
  useEffect(() => {
    document.documentElement.dataset.page = signedIn ? page : "signin";
    document.title =
      active && signedIn ? `${active.title} · ${props.brand ?? "pluxy"}` : (props.brand ?? "pluxy");
  }, [page, signedIn, active, props.brand]);

  if (!signedIn) {
    return (
      <div class="shell" data-part="shell">
        <SignIn
          host={host}
          reason={reason}
          safeAddress={props.safeAddress}
          onDone={() => {
            setReason(undefined);
            setSignedIn(true);
          }}
        />
      </div>
    );
  }

  return (
    <KitContext.Provider value={kit}>
      <PrivacyContext.Provider value={masks}>
        <div class="shell" data-part="shell">
          <nav class="nav" data-part="nav" data-slot="sidebar" aria-label={t("navigation")} ref={navRef}>
            <a class="brand" href={`#/${pages[0]?.id ?? ""}`}>
              {props.brand ?? "pluxy"}
            </a>
            {PAGE_GROUPS.map((group) => {
              const listed = pages.filter((p) => p.group === group);
              if (listed.length === 0) return null;
              const label = `${navId}-${group}`;
              return (
                // biome-ignore lint/a11y/useSemanticElements: a fieldset groups form controls; this groups links
                <div
                  key={group}
                  class="nav-group"
                  data-part="nav-group"
                  data-group={group}
                  role="group"
                  aria-labelledby={label}
                >
                  <span class="nav-group-label" id={label}>
                    {t(`group.${group}`)}
                  </span>
                  <ul>
                    {listed.map((p) => (
                      <li key={p.id}>
                        <a
                          href={`#/${p.id}`}
                          class="nav-item"
                          data-part="nav-item"
                          aria-current={p.id === page ? "page" : undefined}
                          title={p.tip ?? p.title}
                        >
                          <span class="full">{p.title}</span>
                          <span class="short" aria-hidden="true">
                            {p.short}
                          </span>
                        </a>
                      </li>
                    ))}
                  </ul>
                </div>
              );
            })}
            <button
              type="button"
              class="nav-item privacy"
              data-part="privacy-toggle"
              aria-pressed={privacyOn}
              aria-label={t("privacy")}
              title={t(privacyOn ? "privacyDisable" : "privacyEnable")}
              onClick={() => privacy.set(!privacyOn)}
            >
              <span class="full">{t("privacy")}</span>
              <span class="short" aria-hidden="true">
                {t("privacyShort")}
              </span>
            </button>
            {host.features.has("signOut") && (
              <button
                type="button"
                class="nav-item signout"
                aria-label={t("signOut")}
                title={t("signOut")}
                onClick={() => {
                  host.signOut();
                  setSignedIn(false);
                }}
              >
                <span class="full">{t("signOut")}</span>
                <span class="short" aria-hidden="true">
                  {t("signOutShort")}
                </span>
              </button>
            )}
          </nav>
          <main class="main" data-part="main">
            <div class="content">{active?.render()}</div>
          </main>
        </div>
      </PrivacyContext.Provider>
    </KitContext.Provider>
  );
}
