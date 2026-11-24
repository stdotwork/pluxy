// Direction B, "Plain": the panel for someone who set pluxy up once and wants to be told, in
// words, whether it is fine. It is laid out like a letter on a plain page: a top line of five
// words, one reading typeface, almost no boxes or rules, and colour kept for state alone. Home,
// Accounts, Usage and Clients are designed here; the other pages are today's, under Settings.

import type { VNode } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ThemeState } from "../../theme/state";
import type { DesignProps } from "../index";
import { pageAt, type Session, SessionProvider, useSession } from "../session";
import { Accounts } from "./accounts";
import base from "./b.css" with { type: "file" };
import { type BrandState, showFavicon, Wordmark } from "./brand";
import { Clients } from "./clients";
import { CoreProvider, useCoreState } from "./core";
import { Home } from "./home";
import { Icon } from "./icons";
import legacy from "./legacy.css" with { type: "file" };
import literata from "./literata.woff2" with { type: "file" };
import { Btn, Trouble } from "./parts";
import screens from "./screens.css" with { type: "file" };
import { SETTINGS_PAGES, Settings } from "./settings";
import { Usage } from "./usage";

export const styles = [base, screens, legacy];

// Literata (SIL Open Font License 1.1, The Literata Project Authors), the variable upright with
// its weight and optical-size axes, Latin subset: one file of 110 kB. The face is declared here
// because a stylesheet served as a file cannot name a hashed font file.
if (typeof FontFace !== "undefined" && typeof document !== "undefined") {
  document.fonts.add(
    new FontFace("Literata", `url("${literata}") format("woff2")`, { weight: "200 900", display: "swap" }),
  );
}

type Dest = "home" | "accounts" | "usage" | "clients" | "settings";

const DESTS: { id: Dest; label: string; href: string }[] = [
  { id: "home", label: "Home", href: "#/" },
  { id: "accounts", label: "Accounts", href: "#/accounts" },
  { id: "usage", label: "Usage", href: "#/usage" },
  { id: "clients", label: "Clients", href: "#/clients" },
  { id: "settings", label: "Settings", href: "#/settings" },
];

interface Place {
  dest: Dest;
  title: string;
  /** What data-page says: today's page ID where today's page shows, so its own styles apply. */
  page: string;
  render(): VNode;
}

/** Where an address leads: one of this direction's screens, or one of today's pages under a destination. */
function placeOf(session: Session, themes: ThemeState): Place {
  const [pathname = "", query = ""] = session.path.split("?");
  const [first = "", ...restParts] = pathname.split("/");
  const rest = restParts.join("/");
  if (first === "" || first === "status" || first === "home")
    return { dest: "home", title: "Home", page: "home", render: () => <Home /> };
  if (first === "accounts")
    return { dest: "accounts", title: "Accounts", page: "accounts", render: () => <Accounts rest={rest} /> };
  if (first === "usage")
    return { dest: "usage", title: "Usage", page: "usage", render: () => <Usage query={query} /> };
  if (first === "clients")
    return { dest: "clients", title: "Clients", page: "clients", render: () => <Clients /> };
  if (first === "settings")
    return {
      dest: "settings",
      title: "Settings",
      page: "settings",
      render: () => <Settings session={session} themes={themes} />,
    };
  const today = pageAt(session.pages, session.path) ?? pageAt(session.pages, pathname);
  if (today) {
    const dest: Dest = today.id === "add-account" ? "accounts" : "settings";
    const name = SETTINGS_PAGES.find(([id]) => id === today.id)?.[1] ?? today.title;
    return {
      dest,
      title: name,
      page: today.id,
      render: () => (
        <div class="db-page db-today">
          <p class="db-back">
            <a href={dest === "accounts" ? "#/accounts" : "#/settings"}>
              <Icon name="arrowLeft" /> {dest === "accounts" ? "Accounts" : "Settings"}
            </a>
          </p>
          <div class="db-legacy">{today.render()}</div>
        </div>
      ),
    };
  }
  return {
    dest: "home",
    title: "Nothing here",
    page: "missing",
    render: () => (
      <article class="db-page">
        <h1>Nothing here.</h1>
        <p class="db-lead">
          This address leads to no page of the panel. <a href="#/">Go home</a>
        </p>
      </article>
    ),
  };
}

function SignIn(props: { session: Session }) {
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refused, setRefused] = useState<string>();
  const why = refused ?? props.session.reason;
  return (
    <main class="db-signin">
      <Wordmark height={44} />
      <h1>Sign in to see how pluxy is doing.</h1>
      <p class="db-lead">
        The management key is in pluxy's config file, and pluxy prints it when it first starts.
      </p>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setRefused(await props.session.signIn(key, remember));
          setBusy(false);
        }}
      >
        <label class="db-field">
          <span>Management key</span>
          <input
            type="password"
            autocomplete="current-password"
            value={key}
            onInput={(e) => setKey(e.currentTarget.value)}
            required
          />
        </label>
        <label class="db-check">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.currentTarget.checked)} />
          <span>Remember on this device</span>
        </label>
        {why && <Trouble quiet>{why}</Trouble>}
        <Btn kind="primary" type="submit" disabled={busy || !key}>
          Sign in
        </Btn>
      </form>
    </main>
  );
}

const BRAND: Record<string, BrandState> = {
  ok: "ok",
  waiting: "waiting",
  needs: "needs",
  unknown: "unknown",
};

function Shell(props: { session: Session; themes: ThemeState }) {
  const { session } = props;
  const core = useCoreState();
  const place = placeOf(session, props.themes);
  const state = BRAND[core.tone] ?? "unknown";
  useEffect(() => showFavicon(state), [state]);
  useEffect(() => {
    document.title = `${place.title} · pluxy`;
    document.documentElement.dataset.page = place.page;
  }, [place.title, place.page]);
  // Another page starts at its top, as a new sheet does.
  const main = useRef<HTMLElement>(null);
  const shown = useRef(session.path);
  useEffect(() => {
    if (shown.current.split("?")[0] === session.path.split("?")[0]) return;
    shown.current = session.path;
    window.scrollTo(0, 0);
  }, [session.path]);
  return (
    <CoreProvider value={core}>
      <div class="db-shell">
        <button type="button" class="db-skip" onClick={() => main.current?.focus()}>
          Skip to the page
        </button>
        <header class="db-top">
          <a class="db-brand" href="#/" aria-label="pluxy, home">
            <Wordmark state={state} />
          </a>
          <nav aria-label="Main">
            <ul>
              {DESTS.map((d) => (
                <li key={d.id}>
                  <a href={d.href} aria-current={place.dest === d.id ? "page" : undefined}>
                    {d.label}
                  </a>
                </li>
              ))}
            </ul>
          </nav>
          <button
            type="button"
            class="db-privacy"
            aria-pressed={session.privacyOn}
            title={
              session.privacyOn
                ? "Privacy mode is on: account names are hidden"
                : "Hide account names while you share your screen"
            }
            onClick={() => session.setPrivacy(!session.privacyOn)}
          >
            <Icon name={session.privacyOn ? "eyeOff" : "eye"} />
            <span>{session.privacyOn ? "Names hidden" : "Hide names"}</span>
          </button>
        </header>
        <main id="db-main" class="db-main" tabIndex={-1} ref={main}>
          {place.render()}
        </main>
      </div>
    </CoreProvider>
  );
}

export default function App(props: DesignProps) {
  const session = useSession(props);
  useEffect(() => {
    if (!session.signedIn) {
      showFavicon("unknown");
      document.title = "Sign in · pluxy";
      document.documentElement.dataset.page = "signin";
    }
  }, [session.signedIn]);
  if (!session.signedIn) return <SignIn session={session} />;
  return (
    <SessionProvider session={session}>
      <Shell session={session} themes={props.themes} />
    </SessionProvider>
  );
}
