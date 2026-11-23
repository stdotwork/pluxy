// What every direction's shell needs and none should write again: signing in and out, the kit
// and privacy contexts today's pages read, the page list (the build's pages, Setup only where
// core offers it, then the extension pages) and the address's path. It draws nothing: a direction
// draws its own sign-in form and navigation from it. The rules are the ones today's Shell follows
// (kit/shell.tsx).

import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import { api } from "../api";
import { ExtensionPage } from "../extensions/page";
import { PrivacyContext, privacyMasks } from "../privacy";
import { type PageDef, SETUP_PAGE, setupGate } from "../shell";
import { type Kit, KitContext } from "../ui";
import type { DesignProps } from "./index";

export interface Session {
  signedIn: boolean;
  /** Why core signed the person out (it refused the key), for the sign-in form. */
  reason?: string;
  /** Checks the key with core and signs in; resolves to why it was refused, or undefined. */
  signIn(key: string, remember: boolean): Promise<string | undefined>;
  signOut(): void;
  /** Whether the host can sign out (the desktop app cannot). */
  canSignOut: boolean;
  kit: Kit;
  privacyOn: boolean;
  setPrivacy(on: boolean): void;
  /** Today's pages this core offers, extension pages last. */
  pages: PageDef[];
  /** The address's path after "#/", such as "accounts/acct-1"; "" when it names none. */
  path: string;
}

const pathNow = () => location.hash.replace(/^#\/?/, "");

/** The page of pages that path is on or under ("config/common" is on "config"), if any. */
export function pageAt(pages: PageDef[], path: string): PageDef | undefined {
  return pages
    .filter((p) => path === p.id || path.startsWith(`${p.id}/`))
    .sort((a, b) => b.id.length - a.id.length)[0];
}

export function useSession(props: DesignProps): Session {
  const { host, themes, privacy } = props;
  const [privacyOn, setPrivacyOn] = useState(privacy.on);
  useEffect(() => {
    setPrivacyOn(privacy.on);
    return privacy.subscribe(() => setPrivacyOn(privacy.on));
  }, [privacy]);
  useEffect(() => {
    document.documentElement.dataset.privacy = privacyOn ? "on" : "off";
  }, [privacyOn]);

  const [signedIn, setSignedIn] = useState(host.signedIn());
  const [reason, setReason] = useState<string>();
  const unauthorized = useCallback(() => {
    host.signOut();
    setReason("Core refused the management key. Sign in again.");
    setSignedIn(false);
  }, [host]);
  // Core keeps the themes: the theme state follows it while signed in.
  useEffect(() => (signedIn ? themes.connect(unauthorized) : undefined), [signedIn, themes, unauthorized]);

  const [uiVersion, setUiVersion] = useState(0);
  const extensionsChanged = useCallback(() => setUiVersion((n) => n + 1), []);
  const kit = useMemo<Kit>(
    () => ({ host, api: api(host), themes, privacy, unauthorized, extensionsChanged }),
    [host, themes, privacy, unauthorized, extensionsChanged],
  );

  // The pages of the extensions that run, from core; uiVersion asks again after an extension changed.
  const [extPages, setExtPages] = useState<PageDef[]>([]);
  useEffect(() => {
    if (!signedIn) {
      setExtPages([]);
      return;
    }
    let live = true;
    kit.api.ui().then((r) => {
      if (!live) return;
      if (r.status === 401) return kit.unauthorized();
      const ui = r.body;
      if (!ui) return;
      setExtPages(
        ui.extensions.flatMap((ext) =>
          ext.pages.map((page) => ({
            id: `ext/${ext.id}/${page.id}`,
            title: page.title,
            short: page.short,
            group: "extensions" as const,
            render: () => (
              <ExtensionPage
                ext={ext}
                page={page}
                pagesOrigin={ui.pages_origin}
                pagesError={ui.pages_error}
              />
            ),
          })),
        ),
      );
    });
    return () => {
      live = false;
    };
  }, [signedIn, kit, uiVersion]);

  // Setup is offered only by a core that has its calls, and opens by itself on a first run.
  const hasSetup = props.pages.some((p) => p.id === SETUP_PAGE);
  const [setupOffered, setSetupOffered] = useState(true);
  useEffect(() => {
    if (!signedIn || !hasSetup) return;
    let live = true;
    kit.api.setup().then((r) => {
      if (!live || r.status === 401) return;
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

  const [path, setPath] = useState(pathNow);
  useEffect(() => {
    const follow = () => setPath(pathNow());
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);

  return {
    signedIn,
    reason,
    async signIn(key, remember) {
      const why = await host.signIn(key, remember);
      if (!why) {
        setReason(undefined);
        setSignedIn(true);
      }
      return why;
    },
    signOut() {
      host.signOut();
      setSignedIn(false);
    },
    canSignOut: host.features.has("signOut"),
    kit,
    privacyOn,
    setPrivacy: (on) => privacy.set(on),
    pages,
    path,
  };
}

/** Gives what is inside it the kit and privacy mode, as today's Shell does: any kit page works there. */
export function SessionProvider(props: { session: Session; children: ComponentChildren }) {
  const masks = useMemo(() => privacyMasks(props.session.privacyOn), [props.session.privacyOn]);
  return (
    <KitContext.Provider value={props.session.kit}>
      <PrivacyContext.Provider value={masks}>{props.children}</PrivacyContext.Provider>
    </KitContext.Provider>
  );
}
