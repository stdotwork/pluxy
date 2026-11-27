// The application frame: the navigation with its destinations and the status block, a header
// strip over the page (its name, its tabs, its actions), an optional list of topics beside the
// page, and the pane, which is the only part that scrolls. The shell (shell.tsx) shows its pages
// in it; a host can show it by itself, with the destinations visible but not choosable and one
// pane of its own, where it has no session to show pages for.

import type { ComponentChildren, Ref } from "preact";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { DESTINATIONS, type Destination, type TopicSection } from "./destinations";
import { Icon, type IconName } from "./icons";
import { strings } from "./strings";
import type { ThemeState } from "./theme/state";
import { HeadContext, useCopy } from "./ui";

const t = strings("frame", {
  navigation: "Main",
  skip: "Skip to main content",
  search: "Search",
  "dest.home": "Home",
  "dest.accounts": "Accounts",
  "dest.activity": "Activity",
  "dest.tools": "Tools",
  "dest.settings": "Settings",
  tabHeading: "{destination}: {page}",
  tabs: "{name} pages",
  topics: "{name} topics",
  "section.core": "pluxy",
  "section.device": "This device",
  "section.extensions": "From extensions",
  statusMenu: "{status}. Open the menu",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Could not copy: this window may not reach the clipboard.",
  working: "Working…",
});

export const DEST_ICONS: Record<Destination, IconName> = {
  home: "home",
  accounts: "accounts",
  activity: "activity",
  tools: "tools",
  settings: "settings",
};

/** A destination's name, as the navigation shows it. */
export function destinationLabel(dest: Destination): string {
  return t(`dest.${dest}`);
}

/** One item of the status block's menu. */
export type StatusItem =
  /** An address shown in full, with a Copy button. */
  | { kind: "address"; value: string }
  /** Something to do, such as restarting pluxy: it shows that it runs, and why it failed. */
  | { kind: "action"; label: string; run(): Promise<void> }
  | { kind: "link"; label: string; href: string };

/** What the status block at the navigation's foot says. The host fills it. */
export interface FrameStatus {
  /** running: pluxy answers. down: it runs, or should, and does not answer. */
  state: "running" | "down" | "stopped" | "unknown";
  /** The state in a word or two: "Running", "Not answering". */
  word: string;
  /** Where pluxy runs: "this computer :8317", or the other machine's address. */
  where: string;
  /** With items, the block is a button that opens them as a menu. */
  menu?: StatusItem[];
  /** With no items, where a click on the block leads; without either it is not a control. */
  href?: string;
}

/** A link in the frame: an entry of the tabs, the topics or the way back. */
export interface FrameLink {
  label: string;
  href: string;
  current?: boolean;
  tip?: string;
}

/** How the navigation is drawn: the theme's `nav` option, until the window is too narrow for it. */
type Shown = "sidebar" | "rail" | "top" | "bar";

/** Below this width the navigation is a rail whatever the theme says, and topics are a select. */
const NARROW = "(max-width: 819.98px)";
/** Below this width the navigation is a bar of icons across the top. */
const TINY = "(max-width: 559.98px)";

function useMedia(query: string): boolean {
  const [on, setOn] = useState(() => typeof matchMedia === "function" && matchMedia(query).matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const media = matchMedia(query);
    const read = () => setOn(media.matches);
    read();
    media.addEventListener("change", read);
    return () => media.removeEventListener("change", read);
  }, [query]);
  return on;
}

export function Frame(props: {
  themes: ThemeState;
  brand: string;
  /** Where the product name leads; left out, it is not a link. */
  home?: string;
  /** The destinations to show, with where each leads. One with no href is shown but cannot be chosen. */
  destinations: { id: Destination; href?: string }[];
  /** The destination the page belongs to. */
  current?: Destination;
  status?: FrameStatus;
  /** The page's name: the header strip's heading, unless the page's PageHead gives another. */
  title: string;
  /** The pages above this one, outermost first: the header strip shows them as the way back. */
  above?: FrameLink[];
  /** The destination's tabs, drawn in the header strip (beside the page with `tabs: side`). */
  tabs?: FrameLink[];
  /** The destination's topics, drawn beside the page; in a narrow window, as a select above it. */
  topics?: { section: TopicSection; links: FrameLink[] }[];
  /**
   * The Search field under the product name, which opens the command menu. Left out where there
   * is nothing to search (a window that is not connected). by says how it was opened: a click
   * from the keyboard (Enter or Space on the focused field) has no pointer.
   */
  search?: { open(by: "keyboard" | "pointer"): void; keys: string[]; shortcut: string };
  /** Controls at the header strip's right end, after the page's own actions. */
  headerEnd?: ComponentChildren;
  /** When this changes the pane goes back to its top, as a new page starts at its top. */
  page?: string;
  /** The whole frame, for the shell to find the heading in. */
  rootRef?: Ref<HTMLDivElement>;
  /** Said to assistive technology when it changes: the page that opened. */
  announced?: string;
  children: ComponentChildren;
}) {
  const { themes } = props;
  const [, bump] = useState(0);
  useEffect(() => themes.subscribe(() => bump((n) => n + 1)), [themes]);
  const layout = themes.resolved.layout;
  const narrow = useMedia(NARROW);
  const tiny = useMedia(TINY);
  const shown: Shown = tiny ? "bar" : narrow ? "rail" : layout.nav;
  const icons = shown === "rail" || shown === "bar";

  // The pane is the only part that scrolls: a change of page opens the new page at its top. A
  // change inside a page (a filter in the address) keeps the position.
  const pane = useRef<HTMLElement>(null);
  const shownPage = useRef(props.page);
  // A layout effect: the scroll is reset on commit, before paint and before anything on the new
  // page can be reached.
  useLayoutEffect(() => {
    if (shownPage.current === props.page) return;
    shownPage.current = props.page;
    pane.current?.scrollTo(0, 0);
  }, [props.page]);

  // What the page's PageHead gives the strip: its title and its actions.
  const [headTitle, setHeadTitle] = useState<string>();
  const [actions] = useState(actionsOutlet);
  const head = useMemo(() => ({ setTitle: setHeadTitle, setActions: actions.set }), [actions]);
  const name = headTitle ?? props.title;

  const currentLabel = props.current ? destinationLabel(props.current) : name;
  const tabs = props.tabs ?? [];
  const topics = props.topics ?? [];
  // With `tabs: side` a destination's tabs sit beside the page, where topics do.
  const tabsBeside = tabs.length > 0 && layout.tabs === "side" && !narrow;
  const tabsInStrip = tabs.length > 0 && !tabsBeside;
  // A page that is one of its destination's tabs is headed by the destination: the tabs say
  // which page it is.
  const heading = tabs.length > 0 ? currentLabel : name;

  const entry = (d: { id: Destination; href?: string }) => {
    const label = destinationLabel(d.id);
    const body = (
      <>
        <Icon name={DEST_ICONS[d.id]} />
        <span class="nav-label">{label}</span>
      </>
    );
    return (
      <li key={d.id}>
        {d.href !== undefined ? (
          <a
            href={d.href}
            class="nav-item"
            data-part="nav-item"
            data-dest={d.id}
            aria-current={d.id === props.current ? "page" : undefined}
            title={icons ? label : undefined}
          >
            {body}
          </a>
        ) : (
          <span
            class="nav-item"
            data-part="nav-item"
            data-dest={d.id}
            aria-disabled="true"
            title={icons ? label : undefined}
          >
            {body}
          </span>
        )}
      </li>
    );
  };
  const byId = new Map(props.destinations.map((d) => [d.id, d]));
  const main = DESTINATIONS.filter((id) => id !== "settings").flatMap((id) => byId.get(id) ?? []);
  const settings = byId.get("settings");

  const sideList = (label: string, groups: { section?: TopicSection; links: FrameLink[] }[]) =>
    narrow ? (
      <label class="topics-select" data-part="topics-select">
        <span class="sr-only">{label}</span>
        <select
          data-slot="select-trigger"
          value={groups.flatMap((g) => g.links).find((l) => l.current)?.href ?? ""}
          onChange={(e) => location.assign(e.currentTarget.value)}
        >
          {groups.map((g) =>
            g.section && groups.length > 1 ? (
              <optgroup key={g.section} label={t(`section.${g.section}`)}>
                {g.links.map((l) => (
                  <option key={l.href} value={l.href}>
                    {l.label}
                  </option>
                ))}
              </optgroup>
            ) : (
              g.links.map((l) => (
                <option key={l.href} value={l.href}>
                  {l.label}
                </option>
              ))
            ),
          )}
        </select>
      </label>
    ) : (
      <nav class="topics" data-part="topics" aria-label={label}>
        {groups.map((g) => (
          <TopicGroup
            key={g.section ?? ""}
            section={groups.length > 1 ? g.section : undefined}
            links={g.links}
          />
        ))}
      </nav>
    );

  const choosable = props.destinations.some((d) => d.href !== undefined);
  const navigation = (
    <>
      {props.home !== undefined ? (
        <a class="brand" data-part="brand" href={props.home} title={props.brand}>
          <span>{props.brand}</span>
        </a>
      ) : (
        <span class="brand" data-part="brand" title={props.brand}>
          <span>{props.brand}</span>
        </span>
      )}
      {props.search && (
        <button
          type="button"
          class="nav-search"
          data-part="search"
          aria-keyshortcuts={props.search.shortcut}
          title={icons ? t("search") : undefined}
          onClick={(e) => props.search?.open(e.detail === 0 ? "keyboard" : "pointer")}
        >
          <Icon name="search" size={14} />
          <span class="nav-label">{t("search")}</span>
          <kbd class="nav-search-keys">{props.search.keys.join(" ")}</kbd>
        </button>
      )}
      <ul class="nav-list">{main.map(entry)}</ul>
      {settings && <ul class="nav-list nav-foot">{entry(settings)}</ul>}
      {props.status && <Status status={props.status} compact={shown !== "sidebar"} />}
    </>
  );

  return (
    <div
      class="shell"
      data-part="shell"
      data-frame={shown}
      data-narrow={narrow ? "" : undefined}
      ref={props.rootRef}
    >
      <a
        class="skip-link"
        href={`#/${props.page ?? ""}`}
        onClick={(e) => {
          // Not a jump to an element ID: the address's hash names the page.
          e.preventDefault();
          pane.current?.focus();
        }}
      >
        {t("skip")}
      </a>
      {choosable ? (
        <nav class="nav" data-part="nav" data-slot="sidebar" aria-label={t("navigation")}>
          {navigation}
        </nav>
      ) : (
        // Nothing here can be chosen, so it is not a navigation landmark, and not the part
        // "nav": that one means a window whose pages can be opened.
        <div class="nav" data-part="nav-idle" data-slot="sidebar">
          {navigation}
        </div>
      )}
      <div class="frame-body">
        <header class="strip" data-part="header">
          <div class="strip-title">
            {(props.above ?? []).map((link) => (
              <span key={link.href} class="crumb">
                <a href={link.href}>{link.label}</a>
                <span class="crumb-sep" aria-hidden="true">
                  /
                </span>
              </span>
            ))}
            <h1
              data-part="page-title"
              tabIndex={-1}
              aria-label={
                heading !== name ? t("tabHeading", { destination: heading, page: name }) : undefined
              }
            >
              {heading}
            </h1>
          </div>
          {tabsInStrip && (
            <nav class="strip-tabs" data-part="header-tabs" aria-label={t("tabs", { name: currentLabel })}>
              {tabs.map((tab) => (
                <a
                  key={tab.href}
                  href={tab.href}
                  class="strip-tab"
                  data-part="header-tab"
                  aria-current={tab.current ? "page" : undefined}
                  title={tab.tip}
                >
                  {tab.label}
                </a>
              ))}
            </nav>
          )}
          <div class="strip-end">
            <div class="actions" data-part="header-actions">
              <actions.Out />
            </div>
            {props.headerEnd}
          </div>
        </header>
        <div class="frame-pane" data-topics={topics.length > 0 || tabsBeside ? "" : undefined}>
          {tabsBeside && sideList(t("tabs", { name: currentLabel }), [{ links: tabs }])}
          {topics.length > 0 && sideList(t("topics", { name: currentLabel }), topics)}
          <main class="main" data-part="main" tabIndex={-1} ref={pane}>
            <HeadContext.Provider value={head}>
              <div class="content">{props.children}</div>
            </HeadContext.Provider>
          </main>
        </div>
      </div>
      <div class="sr-only" data-part="page-announcer" aria-live="polite" aria-atomic="true">
        {props.announced}
      </div>
    </div>
  );
}

/**
 * The page's actions are given by its PageHead, deep in the pane, and drawn in the strip: set
 * hands them over, and Out draws the latest. Only Out draws again when they change, not the frame
 * and not the page.
 */
function actionsOutlet() {
  let given: ComponentChildren;
  let changed = () => {};
  return {
    set(next: ComponentChildren) {
      given = next;
      changed();
    },
    Out() {
      const [, bump] = useState(0);
      const drawn = useRef<ComponentChildren>(undefined);
      drawn.current = given;
      useLayoutEffect(() => {
        changed = () => bump((n) => n + 1);
        // Given before this listened (the page's effects ran first): draw it now.
        if (drawn.current !== given) changed();
        return () => {
          changed = () => {};
        };
      }, []);
      return <>{given}</>;
    },
  };
}

function TopicGroup(props: { section?: TopicSection; links: FrameLink[] }) {
  const id = useId();
  const list = (
    <ul>
      {props.links.map((l) => (
        <li key={l.href}>
          <a
            href={l.href}
            class="topic"
            data-part="topic"
            aria-current={l.current ? "page" : undefined}
            title={l.tip}
          >
            {l.label}
          </a>
        </li>
      ))}
    </ul>
  );
  if (!props.section) return list;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset groups form controls; this groups links
    <div class="nav-group" data-part="nav-group" data-group={props.section} role="group" aria-labelledby={id}>
      <span class="nav-group-label" id={id}>
        {t(`section.${props.section}`)}
      </span>
      {list}
    </div>
  );
}

/** The status block. With menu items it is a button; Escape and a click outside close the menu. */
function Status(props: { status: FrameStatus; compact: boolean }) {
  const { status } = props;
  const items = status.menu ?? [];
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const block = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  const says = `${status.word}, ${status.where}`;

  useEffect(() => {
    if (!open) return;
    const outside = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      setOpen(false);
      block.current?.focus();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", key);
    };
  }, [open]);

  const body = (
    <>
      <i class="status-dot" aria-hidden="true" />
      <span class="status-text">
        <b>{status.word}</b>
        <span class="mono">{status.where}</span>
      </span>
    </>
  );
  if (items.length === 0 && status.href !== undefined) {
    return (
      <div class="status" data-part="status" data-state={status.state}>
        <a class="status-block" href={status.href} aria-label={says} title={props.compact ? says : undefined}>
          {body}
        </a>
      </div>
    );
  }
  if (items.length === 0) {
    return (
      <div class="status" data-part="status" data-state={status.state}>
        <div class="status-block" role="status" aria-label={says} title={props.compact ? says : undefined}>
          {body}
        </div>
      </div>
    );
  }
  return (
    <div
      class="status"
      data-part="status"
      data-state={status.state}
      ref={wrap}
      onFocusOut={(e) => {
        // Tabbing out of the menu closes it; focus that only left the window keeps it.
        const next = e.relatedTarget as Node | null;
        if (next && !wrap.current?.contains(next)) setOpen(false);
      }}
    >
      <button
        type="button"
        class="status-block"
        ref={block}
        aria-label={t("statusMenu", { status: says })}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        title={props.compact ? says : undefined}
        onClick={() => setOpen(!open)}
      >
        {body}
      </button>
      {open && (
        <div class="status-menu" data-part="status-menu" id={menuId}>
          {items.map((item) =>
            item.kind === "address" ? (
              <StatusAddress key={`a:${item.value}`} value={item.value} />
            ) : item.kind === "action" ? (
              <StatusAction key={`r:${item.label}`} label={item.label} run={item.run} />
            ) : (
              <a key={`l:${item.href}`} class="status-item" href={item.href} onClick={() => setOpen(false)}>
                <span>{item.label}</span>
                <Icon name="arrow" size={14} />
              </a>
            ),
          )}
        </div>
      )}
    </div>
  );
}

function StatusAddress(props: { value: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <>
      <div class="status-address">
        <code class="mono" title={props.value}>
          {props.value}
        </code>
        <button type="button" class="status-copy" onClick={copy}>
          <Icon name={state === "copied" ? "check" : "copy"} size={14} />
          <span aria-live="polite">{state === "copied" ? t("copied") : t("copy")}</span>
        </button>
      </div>
      {state === "failed" && (
        <p class="status-error" role="alert">
          {t("copyFailed")}
        </p>
      )}
    </>
  );
}

function StatusAction(props: { label: string; run(): Promise<void> }) {
  const [running, setRunning] = useState(false);
  const [failed, setFailed] = useState<string>();
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  return (
    <>
      <button
        type="button"
        class="status-item"
        aria-busy={running}
        onClick={async () => {
          if (running) return;
          setRunning(true);
          setFailed(undefined);
          let why: string | undefined;
          try {
            await props.run();
          } catch (e) {
            why = e instanceof Error ? e.message : String(e);
          }
          // The menu may have closed, or the frame gone, while it ran.
          if (!live.current) return;
          setRunning(false);
          setFailed(why);
        }}
      >
        <span>{props.label}</span>
        {running && <span class="status-note">{t("working")}</span>}
      </button>
      {failed !== undefined && (
        <p class="status-error" role="alert">
          {failed}
        </p>
      )}
    </>
  );
}
