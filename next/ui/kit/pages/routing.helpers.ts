// What Order & fallback works out without drawing anything: a provider's accounts in their groups
// (a group is the accounts of one priority), where an arrow or a drop moves an account, and a
// fallback in the editor: its draft, what is wrong with it and how it reads as one line.

import type { Account, ChainStep, ServedModel } from "../api";
import { accountName, providerName } from "../parts/account";
import { strings } from "../strings";

const t = strings("routing.words", {
  first: "First",
  backup: "Backup {count}",
  priority: "Priority {priority}",
  noName: "Enter the model a tool asks for.",
  noStep: "Add at least one step.",
  noModel: "Each step needs a model.",
  repeats: "Step {n} repeats step {m}.",
  exists: "There is already a fallback for {model}. Edit it instead.",
  only: "only {accounts}",
});

// ---- account order ----------------------------------------------------------------------------

/** The accounts of one priority. */
export interface Tier {
  priority: number;
  accounts: Account[];
}

/** Accounts in groups by priority (an account with none is at 0), the highest first. */
export function groupTiers(accounts: Account[]): Tier[] {
  const groups = new Map<number, Account[]>();
  for (const account of accounts) {
    const priority = account.priority ?? 0;
    const members = groups.get(priority) ?? [];
    members.push(account);
    groups.set(priority, members);
  }
  return [...groups]
    .sort(([a], [b]) => b - a)
    .map(([priority, members]) => ({ priority, accounts: members }));
}

/** A group's name by its place: First, then Backup 1, Backup 2. */
export const tierName = (index: number) => (index === 0 ? t("first") : t("backup", { count: index }));

/** A group's priority as the muted words beside its name. */
export const priorityWord = (priority: number) => t("priority", { priority });

/**
 * The priority an account moves to when it goes one group up or down: the neighbouring group's, or,
 * past the first or the last group, one beyond it, which makes a new group.
 */
export function movePriority(tiers: Tier[], priority: number, direction: "up" | "down"): number {
  const index = tiers.findIndex((tier) => tier.priority === priority);
  return tiers[index + (direction === "up" ? -1 : 1)]?.priority ?? priority + (direction === "up" ? 1 : -1);
}

/** Whether a move changes anything: a sole member of the first or last group would only be renumbered. */
export function canMove(tiers: Tier[], priority: number, direction: "up" | "down"): boolean {
  const index = tiers.findIndex((tier) => tier.priority === priority);
  const tier = tiers[index];
  if (!tier) return false;
  const edge = direction === "up" ? index === 0 : index === tiers.length - 1;
  return !(edge && tier.accounts.length === 1);
}

/** One provider's accounts in their groups: order is kept per provider, never across them. */
export interface ProviderOrder {
  extension: string;
  name: string;
  tiers: Tier[];
}

/**
 * What the page orders: a section for each provider with two or more accounts, and the names of the
 * providers with one account each, so with nothing to order. An account core could not read has no
 * provider and is in neither.
 */
export function orderOf(accounts: Account[]): { sections: ProviderOrder[]; single: string[] } {
  const by = new Map<string, Account[]>();
  for (const a of accounts) {
    if (!a.extension) continue;
    const list = by.get(a.extension) ?? [];
    list.push(a);
    by.set(a.extension, list);
  }
  const named = [...by].map(([extension, list]) => ({ extension, name: providerName(extension), list }));
  named.sort((a, b) => a.name.localeCompare(b.name) || a.extension.localeCompare(b.extension));
  return {
    sections: named
      .filter((p) => p.list.length > 1)
      .map((p) => ({ extension: p.extension, name: p.name, tiers: groupTiers(p.list) })),
    single: named.filter((p) => p.list.length === 1).map((p) => p.name),
  };
}

const tiersOf = (accounts: Account[], a: Account) =>
  groupTiers(accounts.filter((x) => x.extension === a.extension));

/**
 * The priority an arrow sets for an account, or nothing when it would change nothing (a sole member
 * of the first group going up, of the last going down) or the account cannot be moved (one set in the
 * settings file). The groups are those of the account's provider alone.
 */
export function moveOf(accounts: Account[], id: string, direction: "up" | "down"): number | undefined {
  const a = accounts.find((x) => x.id === id);
  if (!a || a.source === "config") return undefined;
  const tiers = tiersOf(accounts, a);
  const priority = a.priority ?? 0;
  return canMove(tiers, priority, direction) ? movePriority(tiers, priority, direction) : undefined;
}

/** Where a row is dropped: on a group, or above the first or below the last. */
export type DropTarget = { group: number } | { edge: "first" | "last" };

/**
 * The priority a drop sets: the group's own, or one beyond the first or last group. Nothing when the
 * account is already there: dropped on its own group, or a sole member of the first or last group
 * dropped past it.
 */
export function dropOf(accounts: Account[], id: string, target: DropTarget): number | undefined {
  const a = accounts.find((x) => x.id === id);
  if (!a || a.source === "config") return undefined;
  const tiers = tiersOf(accounts, a);
  const priority = a.priority ?? 0;
  if ("group" in target) {
    return target.group !== priority && tiers.some((tier) => tier.priority === target.group)
      ? target.group
      : undefined;
  }
  const first = target.edge === "first";
  const at = first ? tiers[0] : tiers[tiers.length - 1];
  if (!at || (at.priority === priority && at.accounts.length === 1)) return undefined;
  return first ? at.priority + 1 : at.priority - 1;
}

// ---- fallbacks --------------------------------------------------------------------------------

/** The most steps a fallback has (core's limit). */
export const MAX_STEPS = 8;

/** One step in the editor. id keeps its row the same row while steps move. */
export interface StepDraft {
  id: number;
  model: string;
  /** An extension's ID, or "" for any provider. */
  provider: string;
  /** The IDs of the accounts it is limited to; none: any account of its provider. */
  accounts: string[];
}

export interface FallbackDraft {
  /** The model a tool asks for. */
  name: string;
  /** An existing fallback: its name cannot change. */
  existing: boolean;
  steps: StepDraft[];
  /** The id of the next step added. */
  next: number;
}

const blank = (id: number): StepDraft => ({ id, model: "", provider: "", accounts: [] });

/** A new fallback: step 1 is its own model (set as the name is typed), step 2 is empty. */
export const newDraft = (): FallbackDraft => ({
  name: "",
  existing: false,
  steps: [blank(0), blank(1)],
  next: 2,
});

/** An existing fallback as a draft. */
export function draftOf(name: string, steps: ChainStep[]): FallbackDraft {
  return {
    name,
    existing: true,
    steps: steps.map((s, id) => ({
      id,
      model: s.model,
      provider: s.provider ?? "",
      accounts: [...(s.accounts ?? [])],
    })),
    next: steps.length,
  };
}

/** Sets the name a tool asks for; step 1 follows it for as long as it still holds the name. */
export function withName(d: FallbackDraft, name: string): FallbackDraft {
  const [first, ...rest] = d.steps;
  const follows = !d.existing && first !== undefined && first.model === d.name;
  return { ...d, name, steps: first && follows ? [{ ...first, model: name }, ...rest] : d.steps };
}

/** Whether another step can be added. */
export const canAddStep = (d: FallbackDraft) => d.steps.length < MAX_STEPS;

/** Adds an empty step at the end, unless there are MAX_STEPS. */
export const addStep = (d: FallbackDraft): FallbackDraft =>
  canAddStep(d) ? { ...d, steps: [...d.steps, blank(d.next)], next: d.next + 1 } : d;

/** Changes one step. */
export const changeStep = (d: FallbackDraft, id: number, change: Partial<StepDraft>): FallbackDraft => ({
  ...d,
  steps: d.steps.map((s) => (s.id === id ? { ...s, ...change } : s)),
});

export const removeStep = (d: FallbackDraft, id: number): FallbackDraft => ({
  ...d,
  steps: d.steps.filter((s) => s.id !== id),
});

/** Moves a step one place up or down. */
export function moveStep(d: FallbackDraft, id: number, direction: "up" | "down"): FallbackDraft {
  const from = d.steps.findIndex((s) => s.id === id);
  const to = from + (direction === "up" ? -1 : 1);
  if (from < 0 || to < 0 || to >= d.steps.length) return d;
  const steps = [...d.steps];
  const [moved] = steps.splice(from, 1);
  if (moved) steps.splice(to, 0, moved);
  return { ...d, steps };
}

/** The provider that serves a model by that exact name, when one does: what picking it sets Provider to. */
export const providerOfModel = (model: string, served?: ServedModel[]) =>
  served?.find((m) => m.id === model)?.extension;

/** The steps core takes: no provider and no accounts are left out, as core reads a missing one. */
export function stepsOf(d: FallbackDraft): ChainStep[] {
  return d.steps.map((s) => ({
    model: s.model.trim(),
    ...(s.provider ? { provider: s.provider } : {}),
    ...(s.accounts.length ? { accounts: s.accounts } : {}),
  }));
}

const sameStep = (a: StepDraft, b: StepDraft) =>
  a.model.trim() === b.model.trim() &&
  a.provider === b.provider &&
  a.accounts.length === b.accounts.length &&
  a.accounts.every((id) => b.accounts.includes(id));

/**
 * What is wrong with a draft that core would refuse for the same reason, in the order the editor
 * reads: no name, a name that already has a fallback (a new one), no step, a step with no model, a
 * step that repeats an earlier one. names are the models that already have a fallback.
 */
export function draftProblem(d: FallbackDraft, names: readonly string[]): string | undefined {
  const name = d.name.trim();
  if (!name) return t("noName");
  if (!d.existing && names.includes(name)) return t("exists", { model: name });
  if (d.steps.length === 0) return t("noStep");
  if (d.steps.some((s) => !s.model.trim())) return t("noModel");
  for (let i = 1; i < d.steps.length; i++) {
    const at = d.steps[i] as StepDraft;
    const earlier = d.steps.findIndex((s, j) => j < i && sameStep(s, at));
    if (earlier >= 0) return t("repeats", { n: i + 1, m: earlier + 1 });
  }
  return undefined;
}

/** Whether no step names the model the tool asks for, so requests for it would never reach it. */
export const skipsItself = (d: FallbackDraft) =>
  d.name.trim() !== "" && !d.steps.some((s) => s.model.trim() === d.name.trim());

/**
 * A step as one phrase: its model, then in brackets its provider and, when limited, "only" and the
 * accounts' names. A model alone, with no provider, is just the model.
 */
export function stepWords(step: ChainStep, accountNames: (id: string) => string): string {
  const parts = [step.provider ? providerName(step.provider) : ""];
  if (step.accounts?.length) parts.push(t("only", { accounts: step.accounts.map(accountNames).join(", ") }));
  const inside = parts.filter(Boolean).join(", ");
  return inside ? `${step.model} (${inside})` : step.model;
}

/** The accounts of one provider by name, for a step's checkboxes: ones set in the settings file too. */
export const accountsOf = (accounts: Account[], extension: string) =>
  accounts
    .filter((a) => a.extension === extension)
    .sort((a, b) => accountName(a).localeCompare(accountName(b)) || a.id.localeCompare(b.id));
