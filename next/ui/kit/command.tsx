// The command menu: a search over the pages, the accounts, a few actions and the settings topics,
// opened from the navigation's Search field or by Command K (Control K off Apple computers). Arrow
// keys move, Enter does, Escape closes and puts focus back where it was. It opens at once: only an
// opening by the pointer fades in. The same menu serves the panel and the desktop app.

import type { ComponentChildren } from "preact";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  accountCommands,
  arrange,
  type Command,
  type Group,
  type GroupId,
  marks,
  opensMenu,
  pageCommands,
  pushRecent,
  RECENTS_PREF,
  readRecents,
  step,
} from "./commands";
import type { PageDef } from "./destinations";
import { DEST_ICONS, destinationLabel } from "./frame";
import { Icon } from "./icons";
import { useNaming } from "./parts/hooks";
import { strings } from "./strings";
import { useKit, useLoad } from "./ui";

const t = strings("command", {
  title: "Search pluxy",
  placeholder: "Search pages, accounts and settings",
  results: "Results",
  "group.recent": "Recent",
  "group.actions": "Actions",
  "group.pages": "Pages",
  "group.accounts": "Accounts",
  "group.settings": "Settings",
  none: "Nothing matches “{typed}”.",
  noneHint: "Try a page, an account's name or a setting.",
  loading: "Loading accounts…",
  accountsFailed: "Couldn't read accounts.",
  tryAgain: "Try again",
  more: {
    one: "{count} more. Keep typing to narrow it.",
    other: "{count} more. Keep typing to narrow it.",
  },
  count: { one: "{count} result", other: "{count} results" },
  toMove: "to move",
  toOpen: "to open",
  copyAddress: "Copy address for tools",
  copied: "Copied",
  copyFailed: "Couldn't copy: this window may not reach the clipboard.",
  addAccount: "Add an account",
  refresh: "Refresh this page",
  toDark: "Switch to dark appearance",
  toLight: "Switch to light appearance",
});

/** Pages that only show what core holds, so a refresh (reading it all again) loses nothing. */
const REFRESHABLE = new Set([
  "status",
  "accounts",
  "account",
  "usage",
  "activity/usage",
  "logs",
  "system",
  "extensions",
  "tools",
]);

/**
 * Whether the menu offers Refresh on this page. An account's "Sign in again" (its address ends in
 * /sign-in) holds a sign-in under way in the page itself, which no leave guard asks about: a
 * refresh would drop it.
 */
export const canRefresh = (page: string, hash = location.hash) =>
  REFRESHABLE.has(page) && !(page === "account" && /\/sign-in\/?(\?|$)/.test(hash));

/** How long a row says "Copied" before the menu closes. */
const NOTE_MS = 700;
const RETRY = "action:accounts-again";
/** How long accounts take to load before the menu shows that it waits: a fast reply shows nothing. */
const LOADING_SHOWS_AFTER_MS = 150;

export interface CommandMenuProps {
  /** How it was opened: only an opening by the pointer fades in. */
  by: "keyboard" | "pointer";
  pages: PageDef[];
  /** The page that shows now. */
  page: string;
  /** The address coding tools reach pluxy at, when it is known. */
  address?: string;
  /** Reads the page again; left out where the menu does not offer Refresh. */
  onRefresh?: () => void;
  onClose: () => void;
}

export function CommandMenu(props: CommandMenuProps) {
  const { host, themes, api } = useKit();
  const id = useId();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [note, setNote] = useState<{ id: string; text: string }>();
  const [failed, setFailed] = useState<string>();
  const [recents, setRecents] = useState(() => readRecents(host.prefs.get(RECENTS_PREF)));
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const busy = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const live = useRef(true);
  // The element that had focus when the menu opened: focus goes back to it on close.
  const from = useRef<Element | null>(document.activeElement);
  const byKeyboard = useRef(false);

  const accounts = useLoad(() => api.accounts());
  const naming = useNaming(accounts.data?.accounts);
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (accounts.phase !== "loading") return setLate(false);
    const wait = setTimeout(() => setLate(true), LOADING_SHOWS_AFTER_MS);
    return () => clearTimeout(wait);
  }, [accounts.phase]);

  // Focus is in the field as soon as the menu is on screen: what is typed next goes into it.
  useLayoutEffect(() => {
    input.current?.focus();
  }, []);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
      clearTimeout(timer.current);
    };
  }, []);

  const close = () => {
    const back = from.current;
    if (back instanceof HTMLElement && back.isConnected && back !== document.body) back.focus();
    else document.querySelector<HTMLElement>('[data-part="search"]')?.focus();
    props.onClose();
  };

  const commands = (() => {
    const out = pageCommands(props.pages, DEST_ICONS, destinationLabel);
    const actions: Command[] = [];
    const address = props.address;
    if (address)
      actions.push({
        id: "action:copy-address",
        suggested: true,
        kind: "action",
        label: t("copyAddress"),
        hint: address,
        keywords: ["url", "base", "endpoint"],
        icon: "copy",
        async run() {
          try {
            await navigator.clipboard.writeText(address);
          } catch {
            return { failed: t("copyFailed") };
          }
          return { done: t("copied") };
        },
      });
    if (props.pages.some((p) => p.id === "add-account"))
      actions.push({
        id: "action:add-account",
        suggested: true,
        kind: "action",
        label: t("addAccount"),
        keywords: ["new", "sign in", "login", "connect"],
        icon: "plus",
        href: "#/add-account",
      });
    if (props.onRefresh) {
      const refresh = props.onRefresh;
      actions.push({
        id: "action:refresh",
        suggested: true,
        kind: "action",
        label: t("refresh"),
        keywords: ["reload", "update"],
        icon: "refresh",
        run: () => {
          refresh();
          return undefined;
        },
      });
    }
    const theme = themes.current.theme;
    if (theme.appearance === "both") {
      const dark = themes.resolved.appearance === "dark";
      actions.push({
        id: "action:appearance",
        suggested: true,
        kind: "action",
        label: dark ? t("toLight") : t("toDark"),
        keywords: ["theme", "appearance", "mode", dark ? "light" : "dark"],
        icon: "appearance",
        run: () => {
          themes.set({ appearance: dark ? "light" : "dark" });
          return undefined;
        },
      });
    }
    const named = accounts.data ? accountCommands(accounts.data.accounts, naming.of) : [];
    return [...out, ...actions, ...named];
  })();

  const groups = arrange(query, commands, recents);
  // A reading that failed offers to be made again, whatever is typed.
  const retry: Command | undefined =
    accounts.phase === "failed" && query.trim() !== ""
      ? {
          id: RETRY,
          kind: "action",
          label: t("tryAgain"),
          icon: "refresh",
          run: () => {
            accounts.reload();
            return { stay: true };
          },
        }
      : undefined;
  const flat = [...groups.flatMap((g) => g.items), ...(retry ? [retry] : [])];
  const at = Math.min(active, Math.max(0, flat.length - 1));

  // The row the keys moved to scrolls into view; a row the pointer is over does not move the list.
  useEffect(() => {
    if (!byKeyboard.current) return;
    byKeyboard.current = false;
    list.current?.querySelector(`[id="${id}-${at}"]`)?.scrollIntoView?.({ block: "nearest" });
  }, [at, id]);

  const choose = async (c: Command) => {
    if (busy.current) return;
    let outcome: Awaited<ReturnType<NonNullable<Command["run"]>>>;
    if (c.run) {
      busy.current = true;
      try {
        outcome = await c.run();
      } catch (e) {
        outcome = { failed: e instanceof Error ? e.message : String(e) };
      }
      busy.current = false;
      if (!live.current) return;
    }
    if (outcome && "failed" in outcome) return setFailed(outcome.failed);
    if (outcome && "stay" in outcome) return;
    if (c.id !== RETRY) {
      const next = pushRecent(recents, c.id);
      setRecents(next);
      host.prefs.set(RECENTS_PREF, JSON.stringify(next));
    }
    if (outcome && "done" in outcome) {
      setFailed(undefined);
      setNote({ id: c.id, text: outcome.done });
      timer.current = setTimeout(close, NOTE_MS);
      return;
    }
    close();
    if (c.href) location.hash = c.href.slice(1);
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape" || opensMenu(e)) {
      // The key that opens the menu closes it.
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === "Tab") {
      // The options are reached by the arrow keys: focus stays in the field.
      e.preventDefault();
    } else if (e.isComposing) {
      // A letter being composed (an input method) is not a command yet.
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      byKeyboard.current = true;
      setActive(step(at, e.key === "ArrowDown" ? 1 : -1, flat.length));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const c = flat[at];
      if (c) void choose(c);
    }
  };

  // The row number of each group's first command, in the order the keys move through them.
  const first = new Map<GroupId, number>();
  let rows = 0;
  for (const g of groups) {
    first.set(g.id, rows);
    rows += g.items.length;
  }
  const shown = (g: Group) => (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset groups form controls; this groups options
    <div
      key={g.id}
      role="group"
      aria-labelledby={`${id}-${g.id}`}
      class="command-group"
      data-part="command-group"
      data-group={g.id}
    >
      <div class="command-heading" id={`${id}-${g.id}`}>
        {t(`group.${g.id as GroupId}`)}
      </div>
      {g.items.map((c, i) => (
        <Row
          key={c.id}
          command={c}
          query={query}
          rowId={`${id}-${(first.get(g.id) ?? 0) + i}`}
          index={(first.get(g.id) ?? 0) + i}
          selected={(first.get(g.id) ?? 0) + i === at}
          hint={c.kind !== "setting" || g.id === "recent"}
          note={note?.id === c.id ? note.text : undefined}
          onPoint={(i) => setActive(i)}
          onChoose={choose}
        />
      ))}
      {g.more > 0 && <div class="command-more">{t("more", { count: g.more })}</div>}
    </div>
  );
  const waiting = accounts.phase === "loading" && query.trim() !== "";
  const nothing = flat.length === 0 && !waiting;

  return (
    <div
      class="command-backdrop"
      data-part="command-backdrop"
      onPointerDown={(e) => {
        if (e.target === e.currentTarget) close();
      }}
    >
      <div
        class="command"
        data-part="command"
        data-by={props.by}
        data-slot="dialog-popup"
        role="dialog"
        aria-modal="true"
        aria-label={t("title")}
        onKeyDown={onKeyDown}
      >
        <div class="command-field">
          <Icon name="search" />
          <input
            ref={input}
            data-part="command-input"
            data-slot="input"
            type="text"
            role="combobox"
            aria-expanded="true"
            aria-controls={`${id}-list`}
            aria-activedescendant={flat.length > 0 ? `${id}-${at}` : undefined}
            aria-autocomplete="list"
            aria-label={t("title")}
            placeholder={t("placeholder")}
            autocomplete="off"
            autocapitalize="off"
            spellcheck={false}
            value={query}
            onInput={(e) => {
              setQuery(e.currentTarget.value);
              setActive(0);
              setFailed(undefined);
            }}
          />
          <kbd class="command-key">esc</kbd>
        </div>
        <div
          class="command-list"
          data-part="command-list"
          id={`${id}-list`}
          role="listbox"
          aria-label={t("results")}
          ref={list}
        >
          {groups.map(shown)}
          {retry && (
            <div class="command-group" role="presentation">
              <div class="command-note">{t("accountsFailed")}</div>
              <Row
                command={retry}
                query=""
                rowId={`${id}-${rows}`}
                index={rows}
                selected={rows === at}
                onPoint={(i) => setActive(i)}
                onChoose={choose}
              />
            </div>
          )}
          {waiting && late && (
            <div class="command-skeleton" data-part="command-loading" aria-hidden="true">
              <i />
              <i />
            </div>
          )}
          {nothing && (
            <div class="command-empty" data-part="command-empty">
              <b>{t("none", { typed: query.trim() })}</b>
              <span>{t("noneHint")}</span>
            </div>
          )}
        </div>
        {failed !== undefined && (
          <div class="command-error" role="alert">
            {failed}
          </div>
        )}
        <div class="command-foot" aria-hidden="true">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t("toMove")}
          </span>
          <span>
            <kbd>↵</kbd> {t("toOpen")}
          </span>
        </div>
        <div class="sr-only" role="status" aria-live="polite">
          {waiting
            ? t("loading")
            : query.trim() === ""
              ? ""
              : nothing
                ? t("none", { typed: query.trim() })
                : t("count", { count: flat.length })}
        </div>
      </div>
    </div>
  );
}

/** A command's name, with what the query found in it marked. */
function Marked(props: { text: string; query: string }) {
  const found = marks(props.text, props.query);
  if (found.length === 0) return <>{props.text}</>;
  const out: ComponentChildren[] = [];
  let at = 0;
  for (const [start, end] of found) {
    if (start > at) out.push(props.text.slice(at, start));
    out.push(<mark key={start}>{props.text.slice(start, end)}</mark>);
    at = end;
  }
  if (at < props.text.length) out.push(props.text.slice(at));
  return <>{out}</>;
}

function Row(props: {
  command: Command;
  query: string;
  rowId: string;
  index: number;
  selected: boolean;
  /** Whether to say where it is: a settings topic under the Settings heading need not. */
  hint?: boolean;
  note?: string;
  onPoint: (index: number) => void;
  onChoose: (c: Command) => void;
}) {
  const c = props.command;
  return (
    // biome-ignore lint/a11y/useFocusableInteractive: the field holds focus and names the active option
    // biome-ignore lint/a11y/useKeyWithClickEvents: the keys are the field's: arrows move, Enter does
    <div
      role="option"
      id={props.rowId}
      class="command-item"
      data-part="command-item"
      data-kind={c.kind}
      aria-selected={props.selected}
      onPointerMove={() => !props.selected && props.onPoint(props.index)}
      onClick={() => props.onChoose(c)}
    >
      <Icon name={c.icon} />
      <span class="command-label">
        <Marked text={c.label} query={props.query} />
      </span>
      {c.hint !== undefined && props.hint !== false && <span class="command-hint">{c.hint}</span>}
      {props.note !== undefined ? (
        <span class="command-done" role="status">
          <Icon name="check" size={14} />
          {props.note}
        </span>
      ) : (
        c.shortcut && (
          <span class="command-shortcut" data-part="command-shortcut">
            {c.shortcut.map((k) => (
              <kbd key={k}>{k}</kbd>
            ))}
          </span>
        )
      )}
    </div>
  );
}
