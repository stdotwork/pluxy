// Account order: for each provider with two or more accounts, its accounts in groups from First
// through Backup {n}, shown as a stack of the shared AccountRow with live state and limits. An
// arrow moves an account one group, a drag onto a group joins it, and under "Split by weight" a
// button sets how many shares of its group's requests an account takes.

import { useEffect, useRef, useState } from "preact/hooks";
import type { Account } from "../api";
import { Icon } from "../icons";
import {
  AccountRow,
  accountCondition,
  accountFix,
  type Condition,
  conditionUnchecked,
  type Outcome,
  ProviderMark,
  StatePane,
  useNaming,
  type Windows,
} from "../parts";
import { strings } from "../strings";
import { Button, Empty } from "../ui";
import {
  type DropTarget,
  dropOf,
  moveOf,
  orderOf,
  type ProviderOrder,
  priorityWord,
  tierName,
} from "./routing.helpers";

const t = strings("routing.order", {
  heading: "Account order",
  intro:
    "pluxy tries the first group of a provider's accounts first, and a backup only when every account above it is busy, cooling down or out.",
  providerCount: { one: "1 account · {serving} serving", other: "{count} accounts · {serving} serving" },
  single: "One account each, so nothing to order: {providers}.",
  nothing:
    "Every provider has one account, so there is nothing to order yet. With a second account at a provider, you choose here which goes first.",
  add: "+ Add an account",
  noAccounts: "No accounts yet.",
  noAccountsText: "Add an account first, then choose here which one pluxy uses first.",
  up: "↑",
  down: "↓",
  upFor: "Move {name} up a group",
  downFor: "Move {name} down a group",
  alreadyFirst: "Already first",
  alreadyLast: "Already last",
  weight: "Weight {n}",
  changeWeight: "Change {name}'s weight",
  weightLabel: "Weight",
  weightAbout: "This account takes {n} shares of its group's requests. 0 takes none; empty is 1.",
  inFile: "In the settings file",
  drag: "Drag to change the order",
  newFirst: "Drop here to make a new first group",
  newLast: "Drop here to make a new last group",
});

const ADD = "#/add-account";

/** Where the keyboard goes again after the rows are drawn anew: a control by its focus key. */
function useRestoreFocus(root: { current: HTMLElement | null }, busy: boolean) {
  const key = useRef("");
  // After every draw, once nothing is being saved: the control's element may be a new one.
  useEffect(() => {
    if (!key.current || busy) return;
    const wanted = key.current;
    key.current = "";
    const all = [...(root.current?.querySelectorAll<HTMLElement>("[data-focus-key]") ?? [])];
    const found = all.find((e) => e.dataset.focusKey === wanted);
    if (document.activeElement === found) return;
    const target =
      found && !(found as HTMLButtonElement).disabled
        ? found
        : (found?.closest(".order-arrows")?.querySelector<HTMLElement>("button:not(:disabled)") ?? null);
    target?.focus();
  });
  return (next: string) => {
    key.current = next;
  };
}

export interface OrderProps {
  accounts: Account[];
  windows: Map<string, Windows>;
  running?: ReadonlySet<string>;
  /** Accounts share requests by weight. */
  weighted: boolean;
  /** A move or a weight is being saved. */
  busy: boolean;
  /** Keep every row fix outlined because another primary button (such as the fallback editor) is open. */
  quietAllFixes?: boolean;
  /** Saves an account's priority or weight. */
  change: (id: string, change: { priority?: number; weight?: number | null }) => Promise<unknown>;
  /** Turns an account on in place (shared AccountRow fix). */
  onTurnOn: (id: string) => Promise<Outcome> | Outcome;
  /** Clears an account's cooldown in place (shared AccountRow fix). */
  onClearCooldown: (id: string) => Promise<Outcome> | Outcome;
}

function Row(props: {
  a: Account;
  c: Condition;
  windows?: Windows;
  running?: ReadonlySet<string>;
  unchecked: boolean;
  quietFix: boolean;
  moved: boolean;
  name: string;
  accounts: Account[];
  weighted: boolean;
  editing?: string;
  onEdit: (text: string | undefined, save?: boolean) => void;
  onEditText: (text: string) => void;
  move: (direction: "up" | "down") => void;
  setFocus: (key: string) => void;
  onDragStart: (e: DragEvent) => void;
  onDragEnd: () => void;
  onTurnOn: (id: string) => Promise<Outcome> | Outcome;
  onClearCooldown: (id: string) => Promise<Outcome> | Outcome;
}) {
  const { a, c, name } = props;
  const config = a.source === "config";
  const input = useRef<HTMLInputElement>(null);
  const editing = props.editing !== undefined;
  useEffect(() => {
    if (editing) input.current?.focus();
  }, [editing]);
  const shares = props.editing?.trim() === "" ? 1 : Number(props.editing);
  return (
    <li
      class="order-row"
      data-part="order-row"
      data-account={a.id}
      data-condition={c.kind}
      data-moved={props.moved ? "" : undefined}
    >
      <div class="order-line">
        {config ? (
          <span class="order-handle" data-locked="" aria-hidden="true" />
        ) : (
          <span
            class="order-handle"
            draggable
            title={t("drag")}
            aria-hidden="true"
            onDragStart={props.onDragStart}
            onDragEnd={props.onDragEnd}
          >
            <Icon name="grip" size={14} />
          </span>
        )}
        <div class="order-account">
          <AccountRow
            account={a}
            windows={props.windows}
            running={props.running}
            unchecked={props.unchecked}
            grouped
            quietFix={props.quietFix}
            onTurnOn={props.onTurnOn}
            onClearCooldown={props.onClearCooldown}
          />
        </div>
        <div class="order-controls">
          {config ? (
            <a class="order-file" href={`#/config/source?account=${encodeURIComponent(a.id)}`}>
              {t("inFile")}
            </a>
          ) : (
            <>
              {props.weighted && (
                <Button
                  label={t("changeWeight", { name })}
                  focusKey={`weight ${a.id}`}
                  onClick={() => props.onEdit(a.weight === undefined ? "" : String(a.weight))}
                >
                  <span class="num">{t("weight", { n: a.weight ?? 1 })}</span>
                </Button>
              )}
              <span class="order-arrows">
                {(["up", "down"] as const).map((direction) => {
                  const why =
                    moveOf(props.accounts, a.id, direction) === undefined
                      ? t(direction === "up" ? "alreadyFirst" : "alreadyLast")
                      : undefined;
                  const whyId = `order-${direction}-${a.id}-why`;
                  return (
                    <span key={direction} title={why}>
                      <Button
                        disabled={!!why}
                        focusKey={`${direction} ${a.id}`}
                        describedBy={why ? whyId : undefined}
                        label={t(direction === "up" ? "upFor" : "downFor", { name })}
                        onClick={() => {
                          props.setFocus(`${direction} ${a.id}`);
                          props.move(direction);
                        }}
                      >
                        {t(direction)}
                      </Button>
                      {why && (
                        <span id={whyId} hidden>
                          {why}
                        </span>
                      )}
                    </span>
                  );
                })}
              </span>
            </>
          )}
        </div>
      </div>
      {editing && (
        <label class="order-weight field" data-part="field">
          <span class="label">{t("weightLabel")}</span>
          <input
            ref={input}
            data-slot="input"
            type="number"
            min="0"
            step="1"
            aria-label={t("changeWeight", { name })}
            data-focus-key={`weight ${a.id}`}
            value={props.editing}
            onInput={(e) => props.onEditText(e.currentTarget.value)}
            onBlur={() => props.onEdit(undefined, true)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                props.setFocus(`weight ${a.id}`);
                props.onEdit(undefined, true);
              }
              if (e.key === "Escape") {
                e.preventDefault();
                props.setFocus(`weight ${a.id}`);
                props.onEdit(undefined, false);
              }
            }}
          />
          <small class="hint">{t("weightAbout", { n: Number.isFinite(shares) ? shares : 1 })}</small>
        </label>
      )}
    </li>
  );
}

export function AccountOrder(props: OrderProps) {
  const root = useRef<HTMLElement>(null);
  const naming = useNaming(props.accounts);
  const setFocus = useRestoreFocus(root, props.busy);
  // The weight being typed: an account and its text. Kept here, not in a row, so a row drawn anew
  // keeps what was typed.
  const [weight, setWeightState] = useState<{ id: string; text: string }>();
  // The same, readable at once: an Enter or an Escape that removes the field is followed by its blur.
  const typing = useRef<{ id: string; text: string } | undefined>(undefined);
  const setWeight = (next?: { id: string; text: string }) => {
    typing.current = next;
    setWeightState(next);
  };
  const [dragging, setDraggingState] = useState<Account>();
  // Readable at once during dragover/drop before a state update flushes.
  const draggingRef = useRef<Account | undefined>(undefined);
  const setDragging = (next?: Account) => {
    draggingRef.current = next;
    setDraggingState(next);
  };
  const [dragOver, setDragOver] = useState<string>();
  const [movedId, setMovedId] = useState<string>();
  const movedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(movedTimer.current), []);

  const markMoved = (id: string) => {
    clearTimeout(movedTimer.current);
    setMovedId(id);
    movedTimer.current = setTimeout(() => setMovedId(undefined), 320);
  };

  const { sections, single } = orderOf(props.accounts);
  const running = props.running;

  // At most one filled (primary) repair button across the order stack.
  const firstRepairId = props.quietAllFixes
    ? undefined
    : sections
        .flatMap((s) => s.tiers.flatMap((tier) => tier.accounts))
        .find((a) => accountFix(a, accountCondition(a, running))?.variant === "primary")?.id;

  const edit = (a: Account, text: string | undefined, save?: boolean) => {
    if (text !== undefined) return setWeight({ id: a.id, text });
    const typed = typing.current?.id === a.id ? typing.current.text.trim() : undefined;
    setWeight(undefined);
    if (!save || typed === undefined) return;
    const next = typed === "" ? null : Number(typed);
    if (next !== null && !(Number.isInteger(next) && next >= 0)) return;
    if (next !== (a.weight ?? null)) void props.change(a.id, { weight: next });
  };
  const dropOn = (target: DropTarget) => {
    const moved = draggingRef.current;
    setDragging(undefined);
    setDragOver(undefined);
    if (!moved) return;
    const priority = dropOf(props.accounts, moved.id, target);
    if (priority !== undefined) {
      markMoved(moved.id);
      void props.change(moved.id, { priority });
    }
  };
  const allowDrop = (section: ProviderOrder, key: string) => (e: DragEvent) => {
    if (draggingRef.current?.extension !== section.extension) return;
    e.preventDefault();
    if (dragOver !== key) setDragOver(key);
  };
  const clearOver = (key: string) => (e: DragEvent) => {
    const target = e.currentTarget as HTMLElement | null;
    if (target && e.relatedTarget instanceof Node && target.contains(e.relatedTarget)) return;
    if (dragOver === key) setDragOver(undefined);
  };

  if (props.accounts.length === 0) {
    return (
      <div class="order-empty card">
        <StatePane kind="empty" title={t("noAccounts")} primary={{ label: t("add"), href: ADD }}>
          {t("noAccountsText")}
        </StatePane>
      </div>
    );
  }
  return (
    <div
      ref={root as { current: HTMLDivElement | null }}
      class="order"
      aria-busy={props.busy ? "true" : undefined}
    >
      <p class="order-intro">{t("intro")}</p>
      {sections.length === 0 ? (
        <div class="order-empty card">
          <Empty
            action={
              <a class="btn" data-part="button" data-variant="quiet" href={ADD}>
                {t("add")}
              </a>
            }
          >
            {t("nothing")}
          </Empty>
        </div>
      ) : (
        <div class="order-providers">
          {sections.map((section) => {
            const total = section.tiers.reduce((n, tier) => n + tier.accounts.length, 0);
            const serving = section.tiers.reduce(
              (n, tier) =>
                n + tier.accounts.filter((a) => accountCondition(a, running).group === "serving").length,
              0,
            );
            const firstEdge = `${section.extension}:first`;
            const lastEdge = `${section.extension}:last`;
            return (
              <section
                key={section.extension}
                class="order-provider card"
                data-extension={section.extension}
                data-dragging={dragging?.extension === section.extension ? "" : undefined}
                aria-label={section.name}
              >
                <header class="order-provider-head">
                  <ProviderMark extension={section.extension} size="small" />
                  <h3 class="order-provider-name">{section.name}</h3>
                  <span class="order-provider-count num">
                    {t("providerCount", { count: total, serving })}
                  </span>
                  <div
                    class="order-edge"
                    aria-hidden="true"
                    data-edge="first"
                    data-over={dragOver === firstEdge ? "" : undefined}
                    onDragOver={allowDrop(section, firstEdge)}
                    onDragLeave={clearOver(firstEdge)}
                    onDrop={() => dropOn({ edge: "first" })}
                  >
                    {t("newFirst")}
                  </div>
                </header>
                {section.tiers.map((tier, index) => {
                  const tierKey = `${section.extension}:${tier.priority}`;
                  return (
                    // biome-ignore lint/a11y/noStaticElementInteractions: a drop target for the pointer; the arrows do the same for the keyboard
                    <div
                      key={tier.priority}
                      class="order-tier"
                      data-part="tier"
                      data-priority={tier.priority}
                      data-first={index === 0 ? "" : undefined}
                      data-over={dragOver === tierKey ? "" : undefined}
                      onDragOver={allowDrop(section, tierKey)}
                      onDragLeave={clearOver(tierKey)}
                      onDrop={() => dropOn({ group: tier.priority })}
                    >
                      <div class="order-tier-head">
                        <h4>{tierName(index)}</h4>
                        <span class="order-tier-priority num">{priorityWord(tier.priority)}</span>
                      </div>
                      <ul class="order-rows" aria-label={tierName(index)}>
                        {tier.accounts.map((a) => {
                          const c = accountCondition(a, running);
                          const name = naming.of(a);
                          return (
                            <Row
                              key={a.id}
                              a={a}
                              c={c}
                              windows={props.windows.get(a.id)}
                              running={running}
                              unchecked={conditionUnchecked(a, c, running)}
                              quietFix={a.id !== firstRepairId}
                              moved={movedId === a.id}
                              name={name}
                              accounts={props.accounts}
                              weighted={props.weighted}
                              editing={weight?.id === a.id ? weight.text : undefined}
                              onEdit={(text, save) => edit(a, text, save)}
                              onEditText={(text) => setWeight({ id: a.id, text })}
                              move={(direction) => {
                                const priority = moveOf(props.accounts, a.id, direction);
                                if (priority !== undefined) {
                                  markMoved(a.id);
                                  void props.change(a.id, { priority });
                                }
                              }}
                              setFocus={setFocus}
                              onDragStart={(e) => {
                                e.dataTransfer?.setData("text/plain", a.id);
                                if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
                                const row = (e.currentTarget as HTMLElement).closest(".order-row");
                                if (row) e.dataTransfer?.setDragImage(row, 0, 0);
                                setDragging(a);
                              }}
                              onDragEnd={() => {
                                setDragging(undefined);
                                setDragOver(undefined);
                              }}
                              onTurnOn={props.onTurnOn}
                              onClearCooldown={props.onClearCooldown}
                            />
                          );
                        })}
                      </ul>
                    </div>
                  );
                })}
                <div
                  class="order-edge"
                  aria-hidden="true"
                  data-edge="last"
                  data-over={dragOver === lastEdge ? "" : undefined}
                  onDragOver={allowDrop(section, lastEdge)}
                  onDragLeave={clearOver(lastEdge)}
                  onDrop={() => dropOn({ edge: "last" })}
                >
                  {t("newLast")}
                </div>
              </section>
            );
          })}
        </div>
      )}
      {sections.length > 0 && single.length > 0 && (
        <p class="order-single muted">{t("single", { providers: single.join(", ") })}</p>
      )}
    </div>
  );
}

export const orderHeading = () => t("heading");
