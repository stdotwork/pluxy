// Direction A, "Console": the panel for someone who lives in a terminal. A narrow icon rail and
// a command menu (Ctrl or Cmd+K) are the way around; every action has a key, shown in the hint
// line at the bottom. Dense, monospaced, hairlines in place of cards, one accent, and colour kept
// for state. Overview, Accounts and Requests are designed here; the other pages are today's,
// shown inside this shell.

import type { VNode } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Account, Status } from "../../api";
import { accountIdentities, usePrivacy } from "../../privacy";
import type { PageDef } from "../../shell";
import type { ThemeState } from "../../theme/state";
import type { DesignProps } from "../index";
import { pageAt, type Session, SessionProvider, useSession } from "../session";
import base from "./a.css" with { type: "file" };
import { Accounts } from "./accounts";
import { Mark, showFavicon, Wordmark } from "./brand";
import { bindingCommands, type Command, CommandMenu } from "./command";
import { usePoll } from "./data";
import { Icon, type IconName } from "./icons";
import { type KeyBinding, KeyRegistry, KeysContext, useKeys } from "./keys";
import legacy from "./legacy.css" with { type: "file" };
import { clock, standingOf } from "./lib";
import { Overview } from "./overview";
import { Blank, Btn, Kbd } from "./parts";
import plex400 from "./plex-mono-400.woff2" with { type: "file" };
import plex500 from "./plex-mono-500.woff2" with { type: "file" };
import plex600 from "./plex-mono-600.woff2" with { type: "file" };
import { Requests } from "./requests";
import screens from "./screens.css" with { type: "file" };

export const styles = [base, screens, legacy];

// IBM Plex Mono (SIL Open Font License 1.1), Latin subset, three weights: 45 kB in all. The faces
// are declared here because a stylesheet served as a file cannot name a hashed font file.
if (typeof FontFace !== "undefined" && typeof document !== "undefined") {
  for (const [weight, href] of [
    ["400", plex400],
    ["500", plex500],
    ["600", plex600],
  ] as const) {
    document.fonts.add(
      new FontFace("IBM Plex Mono", `url("${href}") format("woff2")`, { weight, display: "swap" }),
    );
  }
}

type Dest = "overview" | "accounts" | "requests" | "routing" | "settings";

const DESTS: { id: Dest; label: string; href: string; icon: IconName; keys: string }[] = [
  { id: "overview", label: "Overview", href: "#/", icon: "overview", keys: "g o" },
  { id: "accounts", label: "Accounts", href: "#/accounts", icon: "accounts", keys: "g a" },
  { id: "requests", label: "Requests", href: "#/usage", icon: "requests", keys: "g r" },
  { id: "routing", label: "Routing", href: "#/routing", icon: "routing", keys: "g t" },
  { id: "settings", label: "Settings", href: "#/config", icon: "settings", keys: "g s" },
];

/** Today's pages, by the destination each sits under and the name its view has there. */
const LEGACY: Record<string, { dest: Dest; label: string }> = {
  "add-account": { dest: "accounts", label: "Add account" },
  quota: { dest: "accounts", label: "Quota" },
  logs: { dest: "requests", label: "Log" },
  routing: { dest: "routing", label: "Routing" },
  config: { dest: "settings", label: "Config" },
  extensions: { dest: "settings", label: "Extensions" },
  themes: { dest: "settings", label: "Themes" },
  system: { dest: "settings", label: "System" },
  setup: { dest: "settings", label: "Setup" },
};

interface View {
  label: string;
  href: string;
  current: boolean;
}

interface Place {
  dest: Dest;
  /** The view's name, for the title. */
  title: string;
  views: View[];
  /** What data-page says, for styles keyed on today's page IDs. */
  pageId: string;
  render: () => VNode;
}

/** Where an address leads: one of this direction's screens, or one of today's pages in this shell. */
export function placeOf(path: string, pages: PageDef[]): Place {
  const [first = ""] = path.split(/[/?]/);
  const rest = path.slice(first.length).replace(/^\//, "");
  const classic = first === "classic" ? pageAt(pages, rest) : undefined;
  const legacy = classic ?? (first in LEGACY || first === "ext" ? pageAt(pages, path) : undefined);
  const has = (id: string) => pages.some((p) => p.id === id);
  const viewsOf = (dest: Dest, current: string): View[] => {
    const v = (label: string, href: string, id: string): View => ({ label, href, current: current === id });
    if (dest === "accounts")
      return [
        v("Accounts", "#/accounts", "accounts"),
        v("Add account", "#/add-account", "add-account"),
        v("Quota", "#/quota", "quota"),
      ];
    if (dest === "requests")
      return [
        v("Totals", "#/usage", "totals"),
        v("List", "#/usage/list", "list"),
        v("Log", "#/logs", "logs"),
      ];
    if (dest === "settings")
      return [
        ...["config", "extensions", "themes", "system", "setup"]
          .filter(has)
          .map((id) => v(LEGACY[id]?.label ?? id, `#/${id}`, id)),
        ...pages.filter((p) => p.group === "extensions").map((p) => v(p.title, `#/${p.id}`, p.id)),
      ];
    return [];
  };
  if (legacy) {
    const known = LEGACY[legacy.id];
    const dest: Dest = classic
      ? legacy.id === "status"
        ? "overview"
        : legacy.id === "usage"
          ? "requests"
          : legacy.id === "accounts"
            ? "accounts"
            : (known?.dest ?? "settings")
      : (known?.dest ?? "settings");
    return {
      dest,
      title: classic ? `${legacy.title} (today's page)` : (known?.label ?? legacy.title),
      views: viewsOf(dest, classic ? "" : legacy.id),
      pageId: legacy.id,
      render: () => (
        <div class="da-legacy" key={legacy.id}>
          <div class="content">{legacy.render()}</div>
        </div>
      ),
    };
  }
  if (first === "accounts")
    return {
      dest: "accounts",
      title: "Accounts",
      views: viewsOf("accounts", "accounts"),
      pageId: "accounts",
      render: () => <Accounts rest={rest} />,
    };
  if (first === "usage") {
    const list = rest.startsWith("list");
    return {
      dest: "requests",
      title: list ? "Requests" : "Request totals",
      views: viewsOf("requests", list ? "list" : "totals"),
      pageId: "usage",
      render: () => <Requests rest={rest} />,
    };
  }
  if (first === "" || first === "status")
    return { dest: "overview", title: "Overview", views: [], pageId: "status", render: () => <Overview /> };
  return {
    dest: "overview",
    title: "Not found",
    views: [],
    pageId: "",
    render: () => (
      <Blank title="No such page" action={<Btn href="#/">Overview</Btn>}>
        Nothing in the panel has the address #/{path}.
      </Blank>
    ),
  };
}

function SignIn(props: { session: Session }) {
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState(props.session.reason);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    document.title = "Sign in · pluxy";
  }, []);
  return (
    <main class="da-signin">
      <form
        class="da-signin-form"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const why = await props.session.signIn(key, remember);
          setBusy(false);
          if (why) setError(why);
        }}
      >
        <h1 class="da-signin-brand">
          <Mark size={28} />
          <Wordmark height={22} />
        </h1>
        <p class="da-quiet">Sign in with the management key from core's config.</p>
        <label class="da-field">
          <span>Management key</span>
          <input
            type="password"
            autocomplete="current-password"
            required
            autofocus
            value={key}
            onInput={(e) => setKey(e.currentTarget.value)}
          />
        </label>
        <label class="da-check">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.currentTarget.checked)} />
          <span>Remember on this device</span>
        </label>
        {error && (
          <p class="da-fault" role="alert">
            <Icon name="warning" size={14} />
            <span class="da-fault-text">{error}</span>
          </p>
        )}
        <button type="submit" class="da-btn" data-variant="primary" disabled={busy || key === ""}>
          <span>{busy ? "Signing in" : "Sign in"}</span>
          <Kbd keys="Enter" />
        </button>
      </form>
    </main>
  );
}

/** Light or dark as it shows now, and the person's choice behind it. */
function useAppearance(themes: ThemeState) {
  const [, bump] = useState(0);
  useEffect(() => themes.subscribe(() => bump((n) => n + 1)), [themes]);
  return { shown: themes.resolved.appearance, choice: themes.choices.appearance };
}

/** Core's own state in a few words, for the top bar: reachable, its build, its config, its extensions. */
function CoreState(props: { status: ReturnType<typeof usePoll<Status>> }) {
  const { status } = props;
  const s = status.data;
  if (status.loading) return <span class="da-core da-quiet">core …</span>;
  if (status.error || !s) {
    return (
      <span class="da-core" data-tone="danger" role="status" title={status.error}>
        <Icon name="noProvider" size={14} />
        core unreachable
      </span>
    );
  }
  const down = s.extensions.filter((e) => e.current && !["active", "idle"].includes(e.state));
  const current = s.extensions.filter((e) => e.current).length;
  const pending = s.desired.revision !== s.active.revision || !!s.desired.error;
  return (
    <span
      class="da-core"
      title={`pluxy ${s.version} (${s.commit}), config revision ${s.active.revision}, ${current} extensions`}
    >
      <span class="da-core-dot" data-tone={down.length || pending ? "warn" : "ok"} />
      <span class="da-core-part">core {s.version}</span>
      <span class="da-core-part da-quiet da-num" data-tone={pending ? "warn" : undefined}>
        {pending
          ? `rev ${s.active.revision} (${s.desired.revision} not applied)`
          : `rev ${s.active.revision}`}
      </span>
      <a class="da-core-part da-num" href="#/extensions" data-tone={down.length ? "danger" : undefined}>
        {down.length ? `${down.length} of ${current} ext down` : `${current} ext`}
      </a>
      {s.safe_mode && (
        <span class="da-core-part" data-tone="warn">
          safe mode
        </span>
      )}
    </span>
  );
}

function KeysSheet(props: { bindings: KeyBinding[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    return () => before?.focus?.();
  }, []);
  const groups = new Map<string, KeyBinding[]>();
  for (const b of props.bindings) {
    const g = b.group ?? "Everywhere";
    if (!groups.get(g)?.some((x) => x.keys === b.keys)) groups.set(g, [...(groups.get(g) ?? []), b]);
  }
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a click on the backdrop closes the sheet; Escape does the same
    // biome-ignore lint/a11y/useKeyWithClickEvents: Escape is handled on the sheet
    <div
      class="da-cmd-back"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose();
      }}
    >
      <div
        class="da-cmd da-keys"
        role="dialog"
        aria-modal="true"
        aria-label="Keys"
        tabIndex={-1}
        ref={ref}
        onKeyDown={(e) => {
          if (e.key === "Escape" || e.key === "?") {
            e.preventDefault();
            e.stopPropagation();
            props.onClose();
          }
        }}
      >
        <div class="da-cmd-field">
          <Icon name="keyboard" size={16} />
          <strong>Keys</strong>
          <span class="da-grow" />
          <Kbd keys="Escape" />
        </div>
        <div class="da-keys-body">
          {[...groups.entries()].map(([group, list]) => (
            <section key={group}>
              <h3>{group}</h3>
              <dl>
                {list.map((b) => (
                  <div key={b.keys}>
                    <dt>
                      <Kbd keys={b.keys} />
                    </dt>
                    <dd>{b.label}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
          <section>
            <h3>Command menu</h3>
            <dl>
              <div>
                <dt>
                  <span class="da-kbd">
                    <kbd>ctrl</kbd>
                    <kbd>k</kbd>
                  </span>
                </dt>
                <dd>Open it (Cmd+K on a Mac)</dd>
              </div>
            </dl>
          </section>
        </div>
      </div>
    </div>
  );
}

const APPEARANCES = ["dark", "light", "system"] as const;

function Shell(props: { session: Session; themes: ThemeState }) {
  const { session, themes } = props;
  const { kit } = session;
  const p = usePrivacy();
  const registry = useMemo(() => new KeyRegistry(), []);
  const place = useMemo(() => placeOf(session.path, session.pages), [session.path, session.pages]);
  const appearance = useAppearance(themes);
  const status = usePoll(kit.api.status, 30_000);
  const [menu, setMenu] = useState(false);
  const [sheet, setSheet] = useState(false);
  const [accounts, setAccounts] = useState<Account[]>();
  const [bindings, setBindings] = useState<KeyBinding[]>([]);
  const main = useRef<HTMLElement>(null);

  useEffect(() => registry.subscribe(() => setBindings(registry.all())), [registry]);
  useEffect(() => {
    document.documentElement.dataset.page = place.pageId;
    document.title = `${place.title} · pluxy`;
  }, [place.pageId, place.title]);
  // A change of destination opens at its top; a change inside one (a row, a filter) keeps the place.
  useEffect(() => {
    main.current?.scrollTo(0, 0);
  }, [place.dest, place.title]);

  const go = (href: string) => {
    location.hash = href;
  };
  const cycleAppearance = () => {
    const next = APPEARANCES[(APPEARANCES.indexOf(appearance.choice) + 1) % APPEARANCES.length] ?? "dark";
    themes.set({ appearance: next });
  };
  const globals: KeyBinding[] = [
    ...DESTS.map((d) => ({
      keys: d.keys,
      label: `Go to ${d.label}`,
      run: () => go(d.href),
      quiet: true,
      group: "Go to",
    })),
    { keys: "?", label: "Show the keys", run: () => setSheet(true), group: "Everywhere", keyOnly: true },
    {
      keys: "t p",
      label: session.privacyOn ? "Turn privacy mode off" : "Turn privacy mode on",
      run: () => session.setPrivacy(!session.privacyOn),
      quiet: true,
      group: "Everywhere",
    },
    {
      keys: "t l",
      label: `Appearance: ${appearance.choice} (change)`,
      run: cycleAppearance,
      quiet: true,
      group: "Everywhere",
    },
  ];
  // The shell's keys are registered once and replaced as their state changes; a screen's come after and win.
  const globalScope = useRef(Symbol("shell"));
  useEffect(() => {
    registry.set(globalScope.current, globals);
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setSheet(false);
        setMenu((v) => !v);
        return;
      }
      // While a dialog is open the keys are its own.
      if (document.querySelector(".da-cmd-back, [role='dialog'][aria-modal='true'], dialog[open]")) return;
      if (registry.handle(e)) e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [registry]);

  // The menu finds accounts: they are read when it opens.
  useEffect(() => {
    if (!menu) return;
    let live = true;
    kit.api.accounts().then((r) => {
      if (live && r.body) setAccounts(r.body.accounts);
    });
    return () => {
      live = false;
    };
  }, [menu, kit]);

  const commands = useMemo<Command[]>(() => {
    if (!menu) return [];
    const now = Date.now();
    const pages: Command[] = [
      ...DESTS.map((d) => ({
        id: `go:${d.id}`,
        group: "Go to" as const,
        label: d.label,
        icon: d.icon,
        keys: d.keys,
        run: () => go(d.href),
      })),
      {
        id: "go:add",
        group: "Go to",
        label: "Add account",
        detail: "Accounts",
        icon: "plus",
        words: "new sign in api key login",
        run: () => go("#/add-account"),
      },
      {
        id: "go:list",
        group: "Go to",
        label: "Request list",
        detail: "Requests",
        icon: "list",
        words: "usage events history",
        run: () => go("#/usage/list"),
      },
      {
        id: "go:failed",
        group: "Go to",
        label: "Failed requests",
        detail: "Requests",
        icon: "failed",
        words: "errors",
        run: () => go("#/usage/list?failed=true"),
      },
      {
        id: "go:log",
        group: "Go to",
        label: "Log",
        detail: "Requests",
        icon: "log",
        words: "logs viewer",
        run: () => go("#/logs"),
      },
      {
        id: "go:quota",
        group: "Go to",
        label: "Quota",
        detail: "Accounts",
        icon: "exhausted",
        words: "usage windows limits",
        run: () => go("#/quota"),
      },
      ...session.pages
        .filter(
          (pg) => (LEGACY[pg.id]?.dest === "settings" || pg.group === "extensions") && pg.id !== "config",
        )
        .map((pg) => ({
          id: `go:${pg.id}`,
          group: "Go to" as const,
          label: LEGACY[pg.id]?.label ?? pg.title,
          detail: pg.group === "extensions" ? "Extension page" : "Settings",
          icon: "settings" as const,
          run: () => go(`#/${pg.id}`),
        })),
      {
        id: "go:keys",
        group: "Go to",
        label: "Client keys",
        detail: "Settings",
        icon: "key",
        words: "api key token",
        run: () => go("#/config/common"),
      },
      ...["status", "accounts", "usage"]
        .filter((id) => session.pages.some((pg) => pg.id === id))
        .map((id) => ({
          id: `classic:${id}`,
          group: "Go to" as const,
          label: `Today's ${session.pages.find((pg) => pg.id === id)?.title} page`,
          detail: "before this design",
          icon: "open" as const,
          words: "classic old legacy bulk import upload",
          run: () => go(`#/classic/${id}`),
        })),
    ];
    const found: Command[] = (accounts ?? []).map((a) => {
      const s = standingOf(a, now);
      return {
        id: `acct:${a.id}`,
        group: "Accounts" as const,
        label: p.text(a.id, accountIdentities(a)),
        detail: [a.extension, s.label].filter(Boolean).join(" · "),
        words: p.on ? "" : (a.label ?? ""),
        icon: "accounts" as const,
        run: () => go(`#/accounts/${encodeURIComponent(a.id)}`),
      };
    });
    const actions: Command[] = [
      ...bindingCommands(bindings),
      {
        id: "do:reload",
        group: "Actions",
        label: "Reload config",
        detail: "core reads its config file again",
        icon: "refresh",
        run: () => void kit.api.reload().then(() => status.reload()),
      },
      {
        id: "do:sheet",
        group: "Actions",
        label: "Show the keys",
        icon: "keyboard",
        keys: "?",
        run: () => setSheet(true),
      },
      ...(session.canSignOut
        ? [
            {
              id: "do:out",
              group: "Actions" as const,
              label: "Sign out",
              icon: "signOut" as const,
              run: () => session.signOut(),
            },
          ]
        : []),
    ];
    return [...pages, ...found, ...actions];
  }, [menu, accounts, bindings, session.pages, p]);

  const hints = bindings.filter((b) => !b.quiet).slice(0, 7);
  return (
    <KeysContext.Provider value={registry}>
      <div class="da" data-dest={place.dest}>
        <a class="da-skip" href="#da-main">
          Skip to the page
        </a>
        <nav class="da-rail" aria-label="Main">
          <a class="da-rail-brand" href="#/" aria-label="pluxy, overview">
            <Mark size={22} />
          </a>
          <ul>
            {DESTS.map((d) => (
              <li key={d.id}>
                <a
                  href={d.href}
                  class="da-rail-item"
                  aria-current={place.dest === d.id ? "page" : undefined}
                  aria-label={d.label}
                >
                  <Icon name={d.icon} size={18} />
                  <span class="da-rail-label">{d.label}</span>
                  <span class="da-tip" aria-hidden="true">
                    {d.label} <Kbd keys={d.keys} />
                  </span>
                </a>
              </li>
            ))}
          </ul>
          <span class="da-grow" />
          <ul class="da-rail-tools">
            <li>
              <button
                type="button"
                class="da-rail-item"
                aria-pressed={session.privacyOn}
                aria-label="Privacy mode"
                onClick={() => session.setPrivacy(!session.privacyOn)}
              >
                <Icon name={session.privacyOn ? "eyeOff" : "eye"} size={18} />
                <span class="da-tip" aria-hidden="true">
                  {session.privacyOn
                    ? "Privacy mode is on: names are masked"
                    : "Privacy mode: mask account names"}{" "}
                  <Kbd keys="t p" />
                </span>
              </button>
            </li>
            <li>
              <button
                type="button"
                class="da-rail-item"
                aria-label={`Appearance: ${appearance.choice}`}
                onClick={cycleAppearance}
              >
                <Icon
                  name={
                    appearance.choice === "system" ? "monitor" : appearance.choice === "dark" ? "moon" : "sun"
                  }
                  size={18}
                />
                <span class="da-tip" aria-hidden="true">
                  Appearance: {appearance.choice} <Kbd keys="t l" />
                </span>
              </button>
            </li>
            {session.canSignOut && (
              <li>
                <button
                  type="button"
                  class="da-rail-item"
                  aria-label="Sign out"
                  onClick={() => session.signOut()}
                >
                  <Icon name="signOut" size={18} />
                  <span class="da-tip" aria-hidden="true">
                    Sign out
                  </span>
                </button>
              </li>
            )}
          </ul>
        </nav>

        <header class="da-top">
          <a class="da-top-brand" href="#/" aria-label="pluxy">
            <Wordmark height={13} />
          </a>
          <span class="da-top-sep" aria-hidden="true">
            /
          </span>
          <h1 class="da-top-title">{DESTS.find((d) => d.id === place.dest)?.label.toLowerCase()}</h1>
          {place.views.length > 0 && (
            <nav class="da-views" aria-label="Views">
              {place.views.map((v, i) => (
                <a key={v.href} href={v.href} aria-current={v.current ? "page" : undefined}>
                  {i < 9 && (
                    <span class="da-views-n da-num" aria-hidden="true">
                      {i + 1}
                    </span>
                  )}
                  {v.label.toLowerCase()}
                </a>
              ))}
            </nav>
          )}
          <span class="da-grow" />
          <CoreState status={status} />
          <button
            type="button"
            class="da-cmd-open"
            onClick={() => setMenu(true)}
            aria-label="Open the command menu"
            aria-keyshortcuts="Control+K Meta+K"
          >
            <Icon name="search" size={14} />
            <span class="da-cmd-open-text">Go to, find, run</span>
            <span class="da-kbd" aria-hidden="true">
              <kbd>ctrl</kbd>
              <kbd>k</kbd>
            </span>
          </button>
        </header>

        <main class="da-main" id="da-main" ref={main} tabIndex={-1}>
          <ViewKeys views={place.views} />
          {place.render()}
        </main>

        <footer class="da-hints">
          {hints.map((b) => (
            <button key={b.keys} type="button" class="da-hint" onClick={b.run} title={b.label}>
              <Kbd keys={b.keys} />
              <span>{b.label.toLowerCase()}</span>
            </button>
          ))}
          <span class="da-grow" />
          {session.privacyOn && <span class="da-hint-flag">privacy on</span>}
          <button type="button" class="da-hint" onClick={() => setSheet(true)}>
            <Kbd keys="?" />
            <span>keys</span>
          </button>
          <span class="da-hint da-num da-hint-clock">
            <Clock />
          </span>
        </footer>

        {menu && (
          <CommandMenu commands={commands} loadingAccounts={!accounts} onClose={() => setMenu(false)} />
        )}
        {sheet && <KeysSheet bindings={bindings} onClose={() => setSheet(false)} />}
      </div>
    </KeysContext.Provider>
  );
}

/** The views of a destination answer to 1, 2, 3… */
function ViewKeys(props: { views: View[] }) {
  useKeys(
    props.views.slice(0, 9).map((v, i) => ({
      keys: String(i + 1),
      label: `View: ${v.label}`,
      run: () => {
        location.hash = v.href;
      },
      quiet: true,
      group: "Views",
    })),
  );
  return null;
}

function Clock() {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  return <time dateTime={new Date(now).toISOString()}>{clock(now)}</time>;
}

export default function App(props: DesignProps) {
  const session = useSession(props);
  useEffect(() => showFavicon(), []);
  if (!session.signedIn) {
    return (
      <div class="da da-out">
        <SignIn session={session} />
      </div>
    );
  }
  return (
    <SessionProvider session={session}>
      <Shell session={session} themes={props.themes} />
    </SessionProvider>
  );
}
