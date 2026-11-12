// The kit's shell: signing in, navigation and the page area, laid out by the theme's layout
// options. A host build passes the pages it offers; the shell knows nothing else about them.

import type { VNode } from "preact";
import { useCallback, useEffect, useId, useMemo, useState } from "preact/hooks";
import { api } from "./api";
import { ExtensionPage, type ExtensionPageProps } from "./extensions/page";
import type { Host } from "./host";
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

function SignIn(props: { host: Host; onDone: () => void; reason?: string }) {
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
    </main>
  );
}

export function Shell(props: {
  host: Host;
  themes: ThemeState;
  pages: PageDef[];
  brand?: string;
  /** Adds the pages of the extensions that run, after the build's own. */
  extensionPages?: boolean;
  /**
   * Draws an extension page, for a host that shows pages its own way: the desktop app puts each
   * in a webview of its own. Default: a sandboxed frame in the page area.
   */
  extensionPage?: ExtensionPageView;
}) {
  const { host, themes } = props;
  const [signedIn, setSignedIn] = useState(host.signedIn());
  const [reason, setReason] = useState<string>();
  const [uiVersion, setUiVersion] = useState(0);

  const unauthorized = useCallback(() => {
    host.signOut();
    setReason(t("refused"));
    setSignedIn(false);
  }, [host]);
  const extensionsChanged = useCallback(() => setUiVersion((n) => n + 1), []);
  const kit = useMemo(
    () => ({ host, api: api(host), themes, unauthorized, extensionsChanged }),
    [host, themes, unauthorized, extensionsChanged],
  );
  const extPages = useExtensionPages(
    signedIn && props.extensionPages === true,
    kit,
    uiVersion,
    props.extensionPage ?? ExtensionPage,
  );
  const pages = useMemo(() => [...props.pages, ...extPages], [props.pages, extPages]);
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

  const active = pages.find((p) => p.id === page);
  const navId = useId();
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
      <div class="shell" data-part="shell">
        <nav class="nav" data-part="nav" aria-label={t("navigation")}>
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
          {host.features.has("signOut") && (
            <button
              type="button"
              class="nav-item signout"
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
    </KitContext.Provider>
  );
}
