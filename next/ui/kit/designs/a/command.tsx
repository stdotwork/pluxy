// The command menu (Ctrl or Cmd+K): go to a page, find an account, run an action, all by typing
// a few letters. It is the way around direction A; the rail and the keys are shortcuts to it.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { Icon, type IconName } from "./icons";
import { type KeyBinding, keyCap } from "./keys";
import { matchScore } from "./lib";
import { Kbd } from "./parts";

export interface Command {
  id: string;
  group: "Go to" | "Accounts" | "Actions";
  label: string;
  /** Said after the label in a quieter voice: an account's provider and state, a page's place. */
  detail?: string;
  /** More words to match on that are not shown. */
  words?: string;
  icon?: IconName;
  keys?: string;
  run: () => void;
}

const GROUPS: Command["group"][] = ["Go to", "Accounts", "Actions"];
/** Without anything typed the menu shows a few of each; typing searches them all. */
const IDLE_PER_GROUP: Record<Command["group"], number> = { "Go to": 12, Accounts: 6, Actions: 12 };

export function rankCommands(commands: Command[], typed: string): Command[] {
  const query = typed.trim();
  if (!query) {
    return GROUPS.flatMap((g) => commands.filter((c) => c.group === g).slice(0, IDLE_PER_GROUP[g]));
  }
  return commands
    .map((c, i) => ({
      c,
      i,
      score: Math.max(
        matchScore(c.label, query) * 2,
        matchScore(`${c.label} ${c.detail ?? ""} ${c.words ?? ""} ${c.group}`, query),
      ),
    }))
    .filter((x) => x.score > 0)
    .sort((a, b) => b.score - a.score || a.i - b.i)
    .slice(0, 50)
    .map((x) => x.c);
}

/** Bindings as commands, for the actions a screen registered with useKeys. */
export function bindingCommands(bindings: KeyBinding[]): Command[] {
  const seen = new Set<string>();
  return bindings
    .filter((b) => !b.keyOnly && !seen.has(b.label) && seen.add(b.label))
    .map((b) => ({
      id: `key:${b.keys}`,
      group: "Actions" as const,
      label: b.label,
      keys: b.keys,
      run: b.run,
    }));
}

export function CommandMenu(props: { commands: Command[]; onClose: () => void; loadingAccounts?: boolean }) {
  const [typed, setTyped] = useState("");
  const [at, setAt] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const shown = useMemo(() => rankCommands(props.commands, typed), [props.commands, typed]);
  const current = Math.min(at, Math.max(0, shown.length - 1));

  // Focus goes to the field as the menu appears, in the same frame (a layout effect): what is
  // typed straight after Ctrl+K lands in it. It comes back to where it was when the menu closes.
  useLayoutEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    input.current?.focus();
    return () => before?.focus?.();
  }, []);
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  }, [current, shown]);

  const run = (c: Command | undefined) => {
    if (!c) return;
    props.onClose();
    c.run();
  };
  const grouped = typed.trim() === "";
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a click on the backdrop closes the menu; Escape does the same
    // biome-ignore lint/a11y/useKeyWithClickEvents: Escape is handled on the field
    <div
      class="da-cmd-back"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose();
      }}
    >
      <div class="da-cmd" role="dialog" aria-modal="true" aria-label="Command menu">
        <div class="da-cmd-field">
          <Icon name="search" size={16} />
          <input
            ref={input}
            type="text"
            role="combobox"
            aria-expanded="true"
            aria-controls="da-cmd-list"
            aria-activedescendant={shown[current] ? `da-cmd-${current}` : undefined}
            aria-label="Go to a page, find an account or run an action"
            placeholder="Go to, find an account, run an action"
            autocomplete="off"
            spellcheck={false}
            value={typed}
            onInput={(e) => {
              setTyped(e.currentTarget.value);
              setAt(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
                e.preventDefault();
                setAt(shown.length ? (current + 1) % shown.length : 0);
              } else if (e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)) {
                e.preventDefault();
                setAt(shown.length ? (current - 1 + shown.length) % shown.length : 0);
              } else if (e.key === "Enter") {
                e.preventDefault();
                run(shown[current]);
              } else if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                props.onClose();
              }
            }}
          />
          <Kbd keys="Escape" />
        </div>
        <div class="da-cmd-list" id="da-cmd-list" role="listbox" aria-label="Results" ref={list}>
          {shown.length === 0 && (
            <p class="da-cmd-none">
              Nothing matches “{typed}”. Try a page, an account’s name or an action such as “refresh”.
            </p>
          )}
          {shown.map((c, i) => (
            <div key={c.id}>
              {grouped && shown[i - 1]?.group !== c.group && (
                <div class="da-cmd-group" role="presentation">
                  {c.group}
                  {c.group === "Accounts" && props.loadingAccounts ? " (loading)" : ""}
                </div>
              )}
              {/* biome-ignore lint/a11y/useKeyWithClickEvents: the field handles the keys for every option */}
              <div
                id={`da-cmd-${i}`}
                class="da-cmd-item"
                role="option"
                tabIndex={-1}
                aria-selected={i === current}
                onClick={() => run(c)}
                onPointerMove={() => i !== current && setAt(i)}
              >
                <Icon name={c.icon ?? "arrowRight"} size={14} />
                <span class="da-cmd-label">{c.label}</span>
                {c.detail && <span class="da-cmd-detail">{c.detail}</span>}
                {!grouped && <span class="da-cmd-kind">{c.group}</span>}
                {c.keys && <Kbd keys={c.keys} />}
              </div>
            </div>
          ))}
        </div>
        <div class="da-cmd-foot" aria-hidden="true">
          <span>
            <kbd>{keyCap("ArrowUp")}</kbd>
            <kbd>{keyCap("ArrowDown")}</kbd> move
          </span>
          <span>
            <kbd>{keyCap("Enter")}</kbd> run
          </span>
          <span class="da-cmd-count da-num">
            {shown.length} of {props.commands.length}
          </span>
        </div>
      </div>
    </div>
  );
}
