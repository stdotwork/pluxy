// The kit's shell: signing in, and the pages shown in the application frame (frame.tsx), laid out
// by the theme's layout options. A host build passes the pages it offers and what the status
// block says; the shell knows nothing else about them.

import type { VNode } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "./api";
import { DESTINATIONS, destinationHome, type PageDef, place } from "./destinations";
import { ExtensionPage, type ExtensionPageProps } from "./extensions/page";
import { Focus, FocusedContext, FocusGround } from "./focus";
import { destinationLabel, Frame, type FrameLink, type FrameStatus } from "./frame";
import type { Host, Reply } from "./host";
import { Icon } from "./icons";
import { PrivacyContext, type PrivacyState, privacyMasks, usePrivacyOn } from "./privacy";
import { rich, strings } from "./strings";
import type { ThemeState } from "./theme/state";
import { Button, type Kit, KitContext, Notice } from "./ui";

export type { Destination, PageDef, TopicSection } from "./destinations";

const t = strings("shell", {
  signInTitle: "Sign in to pluxy",
  signInHint: "Sign in with pluxy's management key.",
  signInWhere:
    "It is kept in pluxy's config file, on the computer pluxy runs on. There, run {command} to print it; {help} shows where the file is.",
  key: "Management key",
  remember: "Remember on this device",
  signIn: "Sign in",
  refused: "Core refused the management key. Sign in again.",
  safeAddress: "If custom CSS hides the panel, add ?safe to the URL to disable it.",
  namesHidden: "Names hidden",
  showNames: "Show names again",
});

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
 * How long the shell waits for core's answer about Setup before it shows the first page anyway,
 * when the address names no page: until then the window shows the focused window's bare ground,
 * so a first run goes straight to Setup without showing Home for an instant.
 */
export const SETUP_GATE_WAIT_MS = 3000;

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

/**
 * How much of path an address of a page matches, or 0 for none: an ID or an exact address matches
 * itself and anything under it ("logs" matches "logs/abc"); a prefix ending in "/" matches what
 * starts with it and has more after it ("accounts/" matches "accounts/acc_1", not "accounts").
 */
function matched(path: string, address: string): number {
  if (address.endsWith("/"))
    return path.length > address.length && path.startsWith(address) ? address.length : 0;
  return path === address || path.startsWith(`${address}/`) ? address.length : 0;
}

/**
 * The page the address (hash) opens: the page whose ID or one of its addresses (PageDef.addresses)
 * matches it, the longest match first. What follows a "?" is ignored when matching. An address
 * whose first part names a destination with no page of that ID (such as #/activity) opens that
 * destination's first page; any other unknown address opens the first page.
 */
export function currentId(pages: PageDef[], hash = location.hash): string {
  // What follows a ? is the page's own business (its filters), not a page's name.
  const path = (hash.replace(/^#\/?/, "").split("?")[0] ?? "").replace(/\/$/, "");
  let best: PageDef | undefined;
  let length = 0;
  for (const p of pages) {
    for (const address of [p.id, ...(p.addresses ?? [])]) {
      const n = matched(path, address);
      if (n > length) {
        best = p;
        length = n;
      }
    }
  }
  if (best) return best.id;
  // An address whose first part names a destination opens that destination's first page.
  const named = DESTINATIONS.find((d) => d === path.split("/")[0]);
  const home = named && destinationHome(pages, named);
  return home?.id ?? pages[0]?.id ?? "";
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
            dest: "settings" as const,
            kind: "topic" as const,
            section: "extensions" as const,
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

export function SignIn(props: { host: Host; onDone: () => void; reason?: string; safeAddress?: boolean }) {
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState(props.reason);
  const [busy, setBusy] = useState(false);
  return (
    <Focus
      part="main"
      below={
        <>
          <p data-part="signin-where">
            {rich(t("signInWhere"), {
              command: <code class="mono">pluxy key management --show-key</code>,
              help: <code class="mono">pluxy help</code>,
            })}
          </p>
          {props.safeAddress && <p class="hint">{t("safeAddress")}</p>}
        </>
      }
    >
      <form
        class="signin-form"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const why = await props.host.signIn(key, remember);
          setBusy(false);
          if (why) setError(why);
          else props.onDone();
        }}
      >
        <div class="focus-head">
          <h1 data-part="page-title">{t("signInTitle")}</h1>
          <p class="focus-lead">{t("signInHint")}</p>
        </div>
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
      </form>
    </Focus>
  );
}

/**
 * The header strip's button while names are hidden: it says so, and turns hiding off. While names
 * show there is no button: turning hiding on is the Hide names setting in General.
 */
export function NamesHiddenButton(props: { privacy: PrivacyState }) {
  const hidden = usePrivacyOn(props.privacy);
  if (!hidden) return null;
  return (
    <button
      type="button"
      class="strip-button privacy"
      data-part="privacy-toggle"
      aria-pressed="true"
      title={t("showNames")}
      onClick={() => props.privacy.set(false)}
    >
      <Icon name="eyeOff" />
      <span>{t("namesHidden")}</span>
    </button>
  );
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
  /** What the status block at the navigation's foot says: where pluxy runs, and its state. */
  status?: FrameStatus;
}) {
  const { themes, privacy } = props;
  const privacyOn = usePrivacyOn(privacy);
  const masks = useMemo(() => privacyMasks(privacyOn), [privacyOn]);
  useEffect(() => {
    document.documentElement.dataset.privacy = privacyOn ? "on" : "off";
  }, [privacyOn]);
  const [signedIn, setSignedIn] = useState(props.host.signedIn());
  // A page that signs the person out (General's Sign out) calls host.signOut(): the shell then
  // goes back to the sign-in form, as it does when core refuses the key.
  const host = useMemo<Host>(
    () => ({
      ...props.host,
      signOut() {
        props.host.signOut();
        setSignedIn(false);
      },
    }),
    [props.host],
  );
  const [reason, setReason] = useState<string>();
  const [uiVersion, setUiVersion] = useState(0);

  const unauthorized = useCallback(() => {
    host.signOut();
    setReason(t("refused"));
    setSignedIn(false);
  }, [host]);
  const extensionsChanged = useCallback(() => setUiVersion((n) => n + 1), []);
  // What core said about Setup: answered (or the wait ran out), and whether this is its first run.
  // The Setup page tells the shell again when it reads it, and when the first run ends.
  const [gate, setGate] = useState<{ answered: boolean; first?: boolean }>({ answered: false });
  const [gateLate, setGateLate] = useState(false);
  const pagesRef = useRef<PageDef[]>([]);
  // The page the address names; at first the build's own pages are all there are.
  const [page, setPage] = useState(() => currentId(props.pages));
  const setupFirstRun = useCallback((first: boolean) => {
    setGate((g) => (g.answered && g.first === first ? g : { answered: true, first }));
    // The page that ended the first run has moved the address on already: follow it now, so the
    // window does not show Setup in the frame for an instant before the address change arrives.
    setPage(currentId(pagesRef.current));
  }, []);
  // Core keeps the themes: the theme state follows it while signed in.
  useEffect(() => (signedIn ? themes.connect(unauthorized) : undefined), [signedIn, themes, unauthorized]);
  const kit = useMemo(
    () => ({ host, api: api(host), themes, privacy, unauthorized, extensionsChanged, setupFirstRun }),
    [host, themes, privacy, unauthorized, extensionsChanged, setupFirstRun],
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
    setGate({ answered: false });
    setGateLate(false);
    const late = setTimeout(() => setGateLate(true), SETUP_GATE_WAIT_MS);
    kit.api.setup().then((r) => {
      if (!live) return;
      clearTimeout(late);
      // A refused key is left to the page that loads next: this read is the shell's own, and
      // signing out here could undo a sign-in that is under way in the desktop app.
      if (r.status === 401) return setGate({ answered: true });
      const gate = setupGate(r, location.hash);
      setSetupOffered(gate.offered);
      setGate({ answered: true, first: r.body?.first_run === true });
      if (gate.open) {
        location.replace(`#/${SETUP_PAGE}`);
        setPage(currentId(pagesRef.current));
      }
    });
    return () => {
      live = false;
      clearTimeout(late);
    };
  }, [signedIn, hasSetup, kit]);
  const pages = useMemo(
    () => [...props.pages.filter((p) => setupOffered || p.id !== SETUP_PAGE), ...extPages],
    [props.pages, extPages, setupOffered],
  );
  pagesRef.current = pages;

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

  const active = pages.find((p) => p.id === page);
  const brand = props.brand ?? "pluxy";
  const title = active && signedIn ? `${active.title} · ${brand}` : brand;
  useEffect(() => {
    document.documentElement.dataset.page = signedIn ? page : "signin";
    document.title = title;
  }, [page, signedIn, title]);

  // After a change of page, focus moves to the new page's heading in the header strip, and the
  // new title goes to a live region, so a keyboard or screen reader user is told where they are.
  // Opening the panel on a page is not a change: nothing takes focus then.
  const rootRef = useRef<HTMLDivElement>(null);
  const [announced, setAnnounced] = useState("");
  const announcedPage = useRef<string | undefined>(undefined);
  useEffect(() => {
    const first = announcedPage.current === undefined;
    const changed = announcedPage.current !== page;
    announcedPage.current = page;
    if (!signedIn || first || !changed) return;
    rootRef.current?.querySelector<HTMLElement>("h1")?.focus({ preventScroll: true });
    setAnnounced(title);
  }, [page, signedIn, title]);

  const placed = useMemo(() => place(pages, page), [pages, page]);
  const destinations = useMemo(
    () =>
      DESTINATIONS.flatMap((id) => {
        const home = destinationHome(pages, id);
        return home ? [{ id, href: `#/${home.id}` }] : [];
      }),
    [pages],
  );

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

  // Waiting for core's answer about Setup on an address that names no page: a first run opens
  // Setup, so nothing else shows until the answer (or the wait) is over.
  const unnamed = location.hash.replace(/^#\/?/, "") === "";
  if (hasSetup && setupOffered && !gate.answered && !gateLate && unnamed) {
    return (
      <div class="shell" data-part="shell">
        <FocusGround />
      </div>
    );
  }

  // Setup on a first run: the focused window, no frame.
  if (page === SETUP_PAGE && gate.first === true && active) {
    return (
      <KitContext.Provider value={kit}>
        <PrivacyContext.Provider value={masks}>
          <FocusedContext.Provider value={true}>
            <div class="shell" data-part="shell" ref={rootRef}>
              {active.render()}
              <p class="sr-only" aria-live="polite">
                {announced}
              </p>
            </div>
          </FocusedContext.Provider>
        </PrivacyContext.Provider>
      </KitContext.Provider>
    );
  }

  const link = (p: PageDef): FrameLink => ({
    label: p.label ?? p.title,
    href: `#/${p.id}`,
    current: p.id === placed?.current,
    tip: p.tip,
  });
  // The way back names a destination's first page as the navigation does: "Tools", not the
  // page's own title.
  const above = (placed?.above ?? []).map((p) => ({
    label: destinationHome(pages, p.dest) === p ? destinationLabel(p.dest) : (p.label ?? p.title),
    href: `#/${p.id}`,
  }));

  return (
    <KitContext.Provider value={kit}>
      <PrivacyContext.Provider value={masks}>
        <Frame
          themes={themes}
          brand={brand}
          home={`#/${pages[0]?.id ?? ""}`}
          destinations={destinations}
          current={placed?.dest}
          status={props.status}
          title={active?.title ?? ""}
          above={above}
          tabs={placed?.tabs.map(link)}
          topics={placed?.topics.map((g) => ({ section: g.section, links: g.pages.map(link) }))}
          page={page}
          rootRef={rootRef}
          announced={announced}
          headerEnd={<NamesHiddenButton privacy={privacy} />}
        >
          {active?.render()}
        </Frame>
      </PrivacyContext.Provider>
    </KitContext.Provider>
  );
}
