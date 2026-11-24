// Direction C, "Flow": the panel for someone who wants to see the machine. The first screen is a
// map of the pipeline, drawn left to right: client keys, then models and fallback chains, then
// accounts by provider, with each link as thick as its share of the requests. The navigation is
// that same line, top to bottom: Clients, Routing, Accounts, Activity, then System. Map,
// Accounts, Activity, Routing and Clients are designed here; the other pages are today's, shown
// inside this shell as stops on the line.

import type { VNode } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Notice } from "../../ui";
import type { DesignProps } from "../index";
import { pageAt, type Session, SessionProvider, useSession } from "../session";
import { Accounts } from "./accounts";
import { Activity } from "./activity";
import barlow400 from "./barlow-400.woff2" with { type: "file" };
import barlow600 from "./barlow-600.woff2" with { type: "file" };
import condensed500 from "./barlow-semi-condensed-500.woff2" with { type: "file" };
import condensed600 from "./barlow-semi-condensed-600.woff2" with { type: "file" };
import { Mark, showFavicon, Wordmark } from "./brand";
import base from "./c.css" with { type: "file" };
import { Clients } from "./clients";
import { FleetProvider, useConditions, useFleet, useNow } from "./data";
import { Icon, type IconName } from "./icons";
import { type Address, parseAddress } from "./lib";
import { Overview } from "./overview";
import screens from "./pages.css" with { type: "file" };
import { Routing } from "./routing";

export const styles = [base, screens];

// Barlow and Barlow Semi Condensed (SIL Open Font License 1.1, The Barlow Project Authors), Latin
// subset: two weights of each, 90 kB in all. The faces are declared here because a stylesheet
// served as a file cannot name a hashed font file.
if (typeof FontFace !== "undefined" && typeof document !== "undefined") {
  for (const [family, weight, href] of [
    ["Barlow", "400", barlow400],
    ["Barlow", "600", barlow600],
    ["Barlow Semi Condensed", "500", condensed500],
    ["Barlow Semi Condensed", "600", condensed600],
  ] as const) {
    document.fonts.add(new FontFace(family, `url("${href}") format("woff2")`, { weight, display: "swap" }));
  }
}

type StopId = "map" | "clients" | "routing" | "accounts" | "activity" | "system";

interface Stop {
  id: StopId;
  name: string;
  icon: IconName;
  /** The first parts of an address that this stop's own view answers. */
  owns: string[];
  /** Today's pages that sit under the stop, by their ID, with the name they have here. */
  subs: [id: string, name: string][];
}

/** The line: the order a request travels in, then the system that carries it. */
const STOPS: Stop[] = [
  { id: "map", name: "Map", icon: "map", owns: ["", "map", "status"], subs: [] },
  { id: "clients", name: "Clients", icon: "clients", owns: ["clients"], subs: [["setup", "Set up pluxy"]] },
  {
    id: "routing",
    name: "Routing",
    icon: "routing",
    owns: ["routing"],
    subs: [["routing/tiers", "Tiers and policy"]],
  },
  {
    id: "accounts",
    name: "Accounts",
    icon: "accounts",
    owns: ["accounts"],
    subs: [
      ["add-account", "Add account"],
      ["quota", "Usage windows"],
    ],
  },
  {
    id: "activity",
    name: "Activity",
    icon: "activity",
    owns: ["activity", "usage"],
    subs: [["logs", "Logs"]],
  },
  {
    id: "system",
    name: "System",
    icon: "system",
    owns: ["system"],
    subs: [
      ["config", "Settings"],
      ["extensions", "Extensions"],
      ["themes", "Themes"],
    ],
  },
];

interface Place {
  stop: Stop;
  /** The sub-page that is open, by today's page ID. */
  sub?: string;
  title: string;
  view: VNode;
}

function placeOf(session: Session, address: Address): Place {
  const first = address.parts[0] ?? "";
  const own = STOPS.find((s) => s.owns.includes(first));
  // Today's Routing page (tiers, weights, the retry policy) is one stop under this direction's.
  const tiers =
    first === "routing" && address.parts[1] === "tiers"
      ? session.pages.find((p) => p.id === "routing")
      : undefined;
  if (tiers && own) {
    return {
      stop: own,
      sub: "routing/tiers",
      title: "Tiers and policy",
      view: (
        <div class="content dc-legacy" key="routing/tiers">
          {tiers.render()}
        </div>
      ),
    };
  }
  if (own && own.id !== "system") {
    const view =
      own.id === "map" ? (
        <Overview address={address} />
      ) : own.id === "clients" ? (
        <Clients address={address} />
      ) : own.id === "routing" ? (
        <Routing address={address} />
      ) : own.id === "accounts" ? (
        <Accounts address={address} />
      ) : (
        <Activity address={address} />
      );
    return { stop: own, title: own.name, view };
  }
  // Today's pages, shown as they are. The path has no filters of this direction's in it.
  const page = pageAt(session.pages, session.path);
  const system = STOPS[STOPS.length - 1] as Stop;
  if (!page) {
    return {
      stop: system,
      title: "Not found",
      view: (
        <div class="dc-page">
          <Notice tone="warn">Nothing is at this address.</Notice>
          <p>
            <a href="#/map">Back to the map</a>
          </p>
        </div>
      ),
    };
  }
  const stop = STOPS.find((s) => s.subs.some(([id]) => id === page.id)) ?? system;
  return {
    stop,
    sub: page.id,
    title: page.title,
    view: (
      <div class="content dc-legacy" key={page.id}>
        {page.render()}
      </div>
    ),
  };
}

function SignIn(props: { session: Session }) {
  const { session } = props;
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  return (
    <main class="dc-signin">
      <form
        class="dc-card"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const why = await session.signIn(key, remember);
          setBusy(false);
          setError(why);
        }}
      >
        <span class="dc-brand">
          <Mark size={30} />
          <Wordmark />
        </span>
        <h1>Sign in with the management key from core's config.</h1>
        <label>
          Management key
          <span class="dc-field">
            <input
              type="password"
              autocomplete="current-password"
              required
              value={key}
              onInput={(e) => setKey(e.currentTarget.value)}
            />
          </span>
        </label>
        <label class="dc-check">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.currentTarget.checked)} />
          Remember on this device
        </label>
        {(error ?? session.reason) && <Notice tone="danger">{error ?? session.reason}</Notice>}
        <button type="submit" class="dc-btn" data-kind="primary" disabled={busy || key === ""}>
          Sign in
        </button>
        <small>If custom CSS hides the panel, add ?safe to the end of its address.</small>
      </form>
    </main>
  );
}

function Nav(props: { session: Session; place: Place }) {
  const { session, place } = props;
  const fleet = useFleet();
  const conditions = useConditions(fleet, useNow(15_000));
  const needs = [...conditions.values()].filter((c) => c.key !== "available" && c.key !== "disabled").length;
  const has = (id: string) => session.pages.some((p) => p.id === id.split("/")[0]);
  const subsOf = (stop: Stop): [string, string][] => {
    const own = stop.subs.filter(([id]) => has(id));
    if (stop.id !== "system") return own;
    return [
      ...own,
      ...session.pages.filter((p) => p.group === "extensions").map((p): [string, string] => [p.id, p.title]),
    ];
  };
  // Lying down in a narrow window, the line scrolls sideways: the current stop is kept in view.
  const line = useRef<HTMLUListElement>(null);
  useEffect(() => {
    const el = line.current?.querySelector<HTMLElement>(".dc-stop[aria-current]");
    if (el && line.current && line.current.scrollWidth > line.current.clientWidth) {
      line.current.scrollLeft = el.offsetLeft - 40;
    }
  }, [place.stop.id]);
  return (
    <nav class="dc-nav" aria-label="Main">
      <a class="dc-brand" href="#/map" aria-label="pluxy: the map">
        <Mark />
        <Wordmark />
      </a>
      <ul class="dc-line-nav" ref={line}>
        {STOPS.map((stop) => {
          const here = place.stop.id === stop.id;
          const subs = subsOf(stop);
          return (
            <li key={stop.id}>
              <a
                class="dc-stop"
                href={`#/${stop.id}`}
                aria-current={here && !place.sub ? "page" : here ? "true" : undefined}
                title={stop.name}
              >
                <span class="dc-stop-dot">
                  <Icon name={stop.icon} size={15} />
                </span>
                <span class="dc-stop-name">{stop.name}</span>
                {stop.id === "accounts" && needs > 0 && (
                  <span class="dc-nav-count" title={`${needs} not serving`}>
                    <span class="dc-sr">, </span>
                    {needs}
                    <span class="dc-sr"> need you</span>
                  </span>
                )}
              </a>
              {subs.length > 0 && (
                <ul class="dc-substops dc-line-nav-sub">
                  {subs.map(([id, name]) => (
                    <li key={id}>
                      <a
                        class="dc-substop"
                        href={`#/${id}`}
                        aria-current={place.sub === id ? "page" : undefined}
                      >
                        <span>{name}</span>
                      </a>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
      <div class="dc-nav-foot">
        <button
          type="button"
          class="dc-nav-tool"
          aria-pressed={session.privacyOn}
          aria-label="Privacy mode"
          title={
            session.privacyOn
              ? "Turn off privacy mode to show full account details"
              : "Turn on privacy mode to hide account emails and identifiers"
          }
          onClick={() => session.setPrivacy(!session.privacyOn)}
        >
          <Icon name={session.privacyOn ? "eyeOff" : "eye"} size={17} />
          <span>Privacy mode{session.privacyOn ? " is on" : ""}</span>
        </button>
        {session.canSignOut && (
          <button
            type="button"
            class="dc-nav-tool"
            aria-label="Sign out"
            title="Sign out"
            onClick={session.signOut}
          >
            <Icon name="signOut" size={17} />
            <span>Sign out</span>
          </button>
        )}
      </div>
    </nav>
  );
}

function Shell(props: { session: Session }) {
  const { session } = props;
  const address = parseAddress(session.path);
  const place = placeOf(session, address);
  const has = (id: string) => session.pages.some((p) => p.id === id.split("/")[0]);
  const subs = place.stop.subs.filter(([id]) => has(id));

  useEffect(() => {
    document.title = `${place.title} · pluxy`;
    // Today's pages style themselves by this attribute.
    document.documentElement.dataset.page = place.sub ?? place.stop.id;
  }, [place.title, place.sub, place.stop.id]);

  // A change of page opens the new page at its top; a change of filter keeps the place.
  const where = `${place.stop.id}/${place.sub ?? ""}/${place.stop.id === "accounts" ? (address.parts[1] ?? "") : ""}`;
  const shown = useRef(where);
  useLayoutEffect(() => {
    if (shown.current === where) return;
    shown.current = where;
    window.scrollTo(0, 0);
  }, [where]);

  return (
    <div class="dc-shell">
      <a class="dc-skip" href="#dc-main">
        Skip to the page
      </a>
      <Nav session={session} place={place} />
      <main class="dc-main" id="dc-main" tabIndex={-1}>
        {subs.length > 0 && (
          <ul class="dc-subnav" aria-label={`${place.stop.name}: more`}>
            <li>
              <a href={`#/${place.stop.id}`} aria-current={place.sub ? undefined : "page"}>
                {place.stop.name}
              </a>
            </li>
            {subs.map(([id, name]) => (
              <li key={id}>
                <a href={`#/${id}`} aria-current={place.sub === id ? "page" : undefined}>
                  {name}
                </a>
              </li>
            ))}
          </ul>
        )}
        {place.view}
      </main>
    </div>
  );
}

export default function App(props: DesignProps) {
  const session = useSession(props);
  useEffect(() => showFavicon(), []);
  if (!session.signedIn) return <SignIn session={session} />;
  return (
    <SessionProvider session={session}>
      <FleetProvider>
        <Shell session={session} />
      </FleetProvider>
    </SessionProvider>
  );
}
