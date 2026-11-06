// The kit's shell: signing in, navigation and the page area, laid out by the theme's layout
// options. A host build passes the pages it offers; the shell knows nothing else about them.

import type { VNode } from "preact";
import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import { api } from "./api";
import type { Host } from "./host";
import type { ThemeState } from "./theme/state";
import { Button, KitContext, Notice } from "./ui";

/**
 * A page in the navigation. Extension pages will be pages too: their render mounts the
 * extension's isolated document, so the shell and the theme hooks treat them like any other.
 */
export interface PageDef {
  /** Stable ID: the address is #/<id>, and the root element carries data-page="<id>". */
  id: string;
  title: string;
  /** A label of up to about six characters, for the rail navigation. */
  short: string;
  render: () => VNode;
}

function currentId(pages: PageDef[]): string {
  const id = location.hash.replace(/^#\/?/, "").split("/")[0] ?? "";
  return pages.some((p) => p.id === id) ? id : (pages[0]?.id ?? "");
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
        <p class="muted">Sign in with the management key from core's config.</p>
        <label class="field" data-part="field">
          <span class="label">Management key</span>
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
          <span>Remember on this device</span>
        </label>
        {error && <Notice tone="danger">{error}</Notice>}
        <Button type="submit" variant="primary" disabled={busy || key === ""}>
          Sign in
        </Button>
      </form>
    </main>
  );
}

export function Shell(props: { host: Host; themes: ThemeState; pages: PageDef[]; brand?: string }) {
  const { host, themes, pages } = props;
  const [signedIn, setSignedIn] = useState(host.signedIn());
  const [reason, setReason] = useState<string>();
  const [page, setPage] = useState(() => currentId(pages));

  useEffect(() => {
    const onHash = () => setPage(currentId(pages));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [pages]);

  const unauthorized = useCallback(() => {
    host.signOut();
    setReason("Core refused the management key. Sign in again.");
    setSignedIn(false);
  }, [host]);
  const kit = useMemo(() => ({ host, api: api(host), themes, unauthorized }), [host, themes, unauthorized]);

  const active = pages.find((p) => p.id === page);
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
        <nav class="nav" data-part="nav" aria-label="Main">
          <a class="brand" href={`#/${pages[0]?.id ?? ""}`}>
            {props.brand ?? "pluxy"}
          </a>
          <ul>
            {pages.map((p) => (
              <li key={p.id}>
                <a
                  href={`#/${p.id}`}
                  class="nav-item"
                  data-part="nav-item"
                  aria-current={p.id === page ? "page" : undefined}
                  title={p.title}
                >
                  <span class="full">{p.title}</span>
                  <span class="short" aria-hidden="true">
                    {p.short}
                  </span>
                </a>
              </li>
            ))}
          </ul>
          {host.features.has("signOut") && (
            <button
              type="button"
              class="nav-item signout"
              onClick={() => {
                host.signOut();
                setSignedIn(false);
              }}
            >
              <span class="full">Sign out</span>
              <span class="short" aria-hidden="true">
                Out
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
