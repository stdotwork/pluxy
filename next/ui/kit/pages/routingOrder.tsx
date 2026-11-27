// Account order: for each provider with two or more accounts, its accounts in groups from First
// through Backup {n}. An arrow moves an account one group, a drag onto a group joins it, and under
// "Split by weight" a button sets how many shares of its group's requests an account takes.

import { useEffect, useRef, useState } from "preact/hooks";
import type { Account } from "../api";
import { Icon } from "../icons";
import {
  accountCondition,
  accountHref,
  type Condition,
  conditionWord,
  GroupMark,
  StatePane,
  useNaming,
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
  running?: ReadonlySet<string>;
  /** Accounts share requests by weight. */
  weighted: boolean;
  /** A move or a weight is being saved. */
  busy: boolean;
  /** Saves an account's priority or weight. */
  change: (id: string, change: { priority?: number; weight?: number | null }) => Promise<unknown>;
}

function Row(props: {
  a: Account;
  c: Condition;
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
    <li class="order-row" data-part="order-row" data-account={a.id} data-condition={c.kind}>
      <div class="order-line">
        {config ? (
          <span class="order-handle" aria-hidden="true" />
        ) : (
          <span
            class="order-handle"
            draggable
            title={t("drag")}
            aria-hidden="true"
            onDragStart={props.onDragStart}
            onDragEnd={props.onDragEnd}
          >
            <Icon name="grip" />
          </span>
        )}
        <GroupMark c={c} />
        <a class="order-name" href={accountHref(a.id)} title={name}>
          {name}
        </a>
        {c.kind !== "serving" && <span class="order-condition">{conditionWord(c)}</span>}
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
                {t("weight", { n: a.weight ?? 1 })}
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
  const [dragging, setDragging] = useState<Account>();
  const { sections, single } = orderOf(props.accounts);
  const running = props.running;

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
    const moved = dragging;
    setDragging(undefined);
    if (!moved) return;
    const priority = dropOf(props.accounts, moved.id, target);
    if (priority !== undefined) void props.change(moved.id, { priority });
  };
  const allowDrop = (section: ProviderOrder) => (e: DragEvent) => {
    if (dragging?.extension === section.extension) e.preventDefault();
  };

  if (props.accounts.length === 0) {
    return (
      <StatePane kind="empty" title={t("noAccounts")} primary={{ label: t("add"), href: ADD }}>
        {t("noAccountsText")}
      </StatePane>
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
        <Empty
          action={
            <a class="btn" data-part="button" data-variant="quiet" href={ADD}>
              {t("add")}
            </a>
          }
        >
          {t("nothing")}
        </Empty>
      ) : (
        sections.map((section) => (
          <section
            key={section.extension}
            class="order-provider"
            data-extension={section.extension}
            data-dragging={dragging?.extension === section.extension ? "" : undefined}
            aria-label={section.name}
          >
            <h3 class="order-provider-name">{section.name}</h3>
            <div
              class="order-edge"
              aria-hidden="true"
              data-edge="first"
              onDragOver={allowDrop(section)}
              onDrop={() => dropOn({ edge: "first" })}
            >
              {t("newFirst")}
            </div>
            {section.tiers.map((tier, index) => (
              // biome-ignore lint/a11y/noStaticElementInteractions: a drop target for the pointer; the arrows do the same for the keyboard
              <div
                key={tier.priority}
                class="order-tier"
                data-part="tier"
                data-priority={tier.priority}
                onDragOver={allowDrop(section)}
                onDrop={() => dropOn({ group: tier.priority })}
              >
                <div class="order-tier-head">
                  <h4>{tierName(index)}</h4>
                  <span class="muted">{priorityWord(tier.priority)}</span>
                </div>
                <ul class="order-rows" aria-label={tierName(index)}>
                  {tier.accounts.map((a) => {
                    const name = naming.of(a);
                    return (
                      <Row
                        key={a.id}
                        a={a}
                        c={accountCondition(a, running)}
                        name={name}
                        accounts={props.accounts}
                        weighted={props.weighted}
                        editing={weight?.id === a.id ? weight.text : undefined}
                        onEdit={(text, save) => edit(a, text, save)}
                        onEditText={(text) => setWeight({ id: a.id, text })}
                        move={(direction) => {
                          const priority = moveOf(props.accounts, a.id, direction);
                          if (priority !== undefined) void props.change(a.id, { priority });
                        }}
                        setFocus={setFocus}
                        onDragStart={(e) => {
                          e.dataTransfer?.setData("text/plain", a.id);
                          if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
                          const row = (e.currentTarget as HTMLElement).closest(".order-row");
                          if (row) e.dataTransfer?.setDragImage(row, 0, 0);
                          setDragging(a);
                        }}
                        onDragEnd={() => setDragging(undefined)}
                      />
                    );
                  })}
                </ul>
              </div>
            ))}
            <div
              class="order-edge"
              aria-hidden="true"
              data-edge="last"
              onDragOver={allowDrop(section)}
              onDrop={() => dropOn({ edge: "last" })}
            >
              {t("newLast")}
            </div>
          </section>
        ))
      )}
      {sections.length > 0 && single.length > 0 && (
        <p class="order-single muted">{t("single", { providers: single.join(", ") })}</p>
      )}
    </div>
  );
}

export const orderHeading = () => t("heading");
