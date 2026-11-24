// The map's geometry: three columns (client keys, models, accounts by provider) and the links
// between them, each as thick as its share of the requests. A pure function of the data and the
// width, so the same numbers draw the picture and fill its table.
//
// It stays readable at any size by two rules. A row is never thinner than a line of text, however
// small its share, so a node can always be read and pressed. And a long column folds: the busiest
// rows and those that need someone stay, the rest become one "+N more" row per group that carries
// their links, until it is opened.

import type { Link } from "./data";

export interface MapItem {
  ref: string;
  label: string;
  requests: number;
  failed: number;
}

export interface MapAccount extends MapItem {
  provider: string;
  /** Needs someone, or is not serving: it stays on the map and gets a second line. */
  flagged: boolean;
}

export interface MapModel extends MapItem {
  /** The number of steps, for a fallback chain. */
  chain?: number;
}

export type NodeKind = "key" | "model" | "account" | "more";

export interface MapNode {
  /** "key:<id>", "model:<name>", "account:<id>" or "more:<column>:<group>". */
  id: string;
  col: 0 | 1 | 2;
  kind: NodeKind;
  ref: string;
  label: string;
  requests: number;
  failed: number;
  y: number;
  h: number;
  provider?: string;
  chain?: number;
  flagged?: boolean;
  /** For a "+N more" row: how many it stands for, and what opens it. */
  folded?: number;
  group?: string;
}

export interface MapGroup {
  provider: string;
  y: number;
  /** Where its last node ends. */
  bottom: number;
  accounts: number;
  requests: number;
}

export interface MapLink {
  id: string;
  from: string;
  to: string;
  requests: number;
  failed: number;
  /** The centre line's two ends, and the thickness at each (a short row squeezes its links). */
  y0: number;
  y1: number;
  t0: number;
  t1: number;
  /** The provider the link ends at, for a link to an account. */
  provider?: string;
}

export interface MapLayout {
  cols: { x: number; w: number }[];
  nodes: MapNode[];
  links: MapLink[];
  groups: MapGroup[];
  width: number;
  height: number;
  /** Pixels per request. */
  scale: number;
  compact: boolean;
  /** Maps a key, model or account to the node that shows it (itself, or its "+N more" row). */
  nodeOf(kind: "key" | "model" | "account", ref: string): string | undefined;
}

export interface MapInput {
  keys: MapItem[];
  models: MapModel[];
  accounts: MapAccount[];
  keyModel: Link[];
  modelAccount: Link[];
  /** What is opened: "keys", "models", or a provider's ID. */
  expanded: ReadonlySet<string>;
  width: number;
}

export const HEAD_H = 26;
const GROUP_GAP = 14;

const byWeight = <T extends MapItem>(a: T, b: T) => b.requests - a.requests || a.label.localeCompare(b.label);

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

/** Splits a column into what stays and what folds: the first `keep` by weight, plus every must. */
function fold<T extends MapItem>(items: T[], keep: number, must: (item: T) => boolean): [T[], T[]] {
  if (items.length <= keep + 1) return [items, []];
  const sorted = [...items].sort(byWeight);
  const shown = sorted.filter((item, i) => i < keep || must(item));
  const rest = sorted.filter((item, i) => !(i < keep || must(item)));
  return rest.length > 1 ? [shown, rest] : [sorted, []];
}

export function layoutMap(input: MapInput): MapLayout {
  const width = Math.max(300, input.width);
  const compact = width < 620;

  // ---- which rows show --------------------------------------------------------------------
  const nodeFor = new Map<string, string>();
  const nodes: MapNode[] = [];
  const moreOf = (col: 0 | 1 | 2, group: string, rest: MapItem[], provider?: string): MapNode => ({
    id: `more:${col}:${group}`,
    col,
    kind: "more",
    ref: group,
    group,
    label: `${rest.length} more`,
    requests: rest.reduce((n, r) => n + r.requests, 0),
    failed: rest.reduce((n, r) => n + r.failed, 0),
    y: 0,
    h: 0,
    provider,
    folded: rest.length,
  });

  const keyKeep = input.expanded.has("keys") ? Number.POSITIVE_INFINITY : compact ? 5 : 7;
  const [keys, keysRest] = fold(input.keys, keyKeep, () => false);
  const keyNodes: MapNode[] = keys.map((k) => ({
    ...k,
    id: `key:${k.ref}`,
    col: 0,
    kind: "key",
    y: 0,
    h: 0,
  }));
  for (const k of keys) nodeFor.set(`key:${k.ref}`, `key:${k.ref}`);
  if (keysRest.length > 0) {
    keyNodes.push(moreOf(0, "keys", keysRest));
    for (const k of keysRest) nodeFor.set(`key:${k.ref}`, "more:0:keys");
  }

  const modelKeep = input.expanded.has("models") ? Number.POSITIVE_INFINITY : compact ? 8 : 12;
  const [models, modelsRest] = fold(input.models, modelKeep, (m) => !!m.chain);
  const modelNodes: MapNode[] = models.map((m) => ({
    ...m,
    id: `model:${m.ref}`,
    col: 1,
    kind: "model",
    y: 0,
    h: 0,
  }));
  for (const m of models) nodeFor.set(`model:${m.ref}`, `model:${m.ref}`);
  if (modelsRest.length > 0) {
    modelNodes.push(moreOf(1, "models", modelsRest));
    for (const m of modelsRest) nodeFor.set(`model:${m.ref}`, "more:1:models");
  }

  // Accounts by provider, the busiest provider first. A long fleet folds per provider.
  const byProvider = new Map<string, MapAccount[]>();
  for (const a of input.accounts) {
    const list = byProvider.get(a.provider) ?? [];
    list.push(a);
    byProvider.set(a.provider, list);
  }
  const providers = [...byProvider.entries()]
    .map(([provider, list]) => ({ provider, list, requests: list.reduce((n, a) => n + a.requests, 0) }))
    .sort((a, b) => b.requests - a.requests || a.provider.localeCompare(b.provider));
  const budget = compact ? 10 : 16;
  const perProvider =
    input.accounts.length <= budget
      ? Number.POSITIVE_INFINITY
      : Math.max(2, Math.floor(budget / providers.length));
  const accountGroups = providers.map((p) => {
    const keep = input.expanded.has(p.provider) ? Number.POSITIVE_INFINITY : perProvider;
    // Up to two accounts that need someone stay on the map although others are busier.
    const must = new Set(
      [...p.list]
        .filter((a) => a.flagged)
        .sort(byWeight)
        .slice(0, 2)
        .map((a) => a.ref),
    );
    const [shown, rest] = fold(p.list, keep, (a) => must.has(a.ref));
    const list: MapNode[] = [...shown].sort(byWeight).map((a) => ({
      ...a,
      id: `account:${a.ref}`,
      col: 2,
      kind: "account",
      y: 0,
      h: 0,
    }));
    for (const a of shown) nodeFor.set(`account:${a.ref}`, `account:${a.ref}`);
    if (rest.length > 0) {
      list.push(moreOf(2, p.provider, rest, p.provider));
      for (const a of rest) nodeFor.set(`account:${a.ref}`, `more:2:${p.provider}`);
    }
    return { ...p, nodes: list };
  });

  // ---- links between the rows that show -----------------------------------------------------
  const merged = new Map<string, MapLink>();
  const add = (kindFrom: "key" | "model", kindTo: "model" | "account", l: Link) => {
    const from = nodeFor.get(`${kindFrom}:${l.from}`);
    const to = nodeFor.get(`${kindTo}:${l.to}`);
    if (!from || !to || l.requests <= 0) return;
    const id = `${from}>${to}`;
    const kept = merged.get(id);
    if (kept) {
      kept.requests += l.requests;
      kept.failed += l.failed;
    } else
      merged.set(id, { id, from, to, requests: l.requests, failed: l.failed, y0: 0, y1: 0, t0: 0, t1: 0 });
  };
  for (const l of input.keyModel) add("key", "model", l);
  for (const l of input.modelAccount) add("model", "account", l);
  const links = [...merged.values()];

  // ---- order: each column near what it connects to ------------------------------------------
  const accountNodes = accountGroups.flatMap((g) => g.nodes);
  const position = new Map<string, number>();
  for (const [i, n] of accountNodes.entries()) position.set(n.id, i);
  const towards = (list: MapNode[], side: "from" | "to") => {
    const other = side === "from" ? "to" : "from";
    const centre = (n: MapNode) => {
      let sum = 0;
      let weight = 0;
      for (const l of links) {
        if (l[side] !== n.id) continue;
        const at = position.get(l[other]);
        if (at === undefined) continue;
        sum += at * l.requests;
        weight += l.requests;
      }
      return weight > 0 ? sum / weight : Number.POSITIVE_INFINITY;
    };
    const centres = new Map(list.map((n) => [n.id, centre(n)]));
    const sorted = [...list].sort((a, b) => {
      // The "+N more" row closes its column.
      if ((a.kind === "more") !== (b.kind === "more")) return a.kind === "more" ? 1 : -1;
      const ca = centres.get(a.id) ?? 0;
      const cb = centres.get(b.id) ?? 0;
      return ca - cb || byWeight(a, b);
    });
    for (const [i, n] of sorted.entries()) position.set(n.id, i);
    return sorted;
  };
  const modelOrder = towards(modelNodes, "from");
  const keyOrder = towards(keyNodes, "from");

  // ---- sizes --------------------------------------------------------------------------------
  const total = Math.max(
    1,
    keyOrder.reduce((n, k) => n + k.requests, 0),
    modelOrder.reduce((n, m) => n + m.requests, 0),
    accountNodes.reduce((n, a) => n + a.requests, 0),
  );
  const band = clamp(width * 0.25, 130, 280);
  // A node is never taller than a few rows, however few nodes share the requests.
  const busiest = Math.max(1, ...[...keyOrder, ...modelOrder, ...accountNodes].map((n) => n.requests));
  const scale = Math.min(band / total, 84 / busiest);
  const rowH = compact ? 30 : 32;
  const flaggedH = compact ? 44 : 46;
  const gap = compact ? 5 : 6;
  const heightOf = (n: MapNode) =>
    Math.max(n.kind === "account" && n.flagged ? flaggedH : rowH, Math.round(n.requests * scale) + 8);

  const stack = (list: MapNode[], top: number) => {
    let y = top;
    for (const n of list) {
      n.y = y;
      n.h = heightOf(n);
      y += n.h + gap;
    }
    return list.length > 0 ? y - gap : top;
  };

  const groups: MapGroup[] = [];
  let ay = 0;
  for (const g of accountGroups) {
    const top = ay;
    const bottom = stack(g.nodes, top + HEAD_H);
    groups.push({ provider: g.provider, y: top, bottom, accounts: g.list.length, requests: g.requests });
    ay = bottom + GROUP_GAP;
  }
  const accountsBottom = groups.length > 0 ? ay - GROUP_GAP : 0;
  const colHeight = (list: MapNode[]) => list.reduce((n, x) => n + heightOf(x) + gap, -gap);
  const keysH = Math.max(0, colHeight(keyOrder));
  const modelsH = Math.max(0, colHeight(modelOrder));
  const height = Math.max(accountsBottom, keysH + HEAD_H, modelsH + HEAD_H, 120);
  // A short column sits a little down its side, so the links fan out; never far from the top.
  const lift = (h: number) => HEAD_H + Math.min(Math.max(0, (height - HEAD_H - h) / 2), 56);
  stack(keyOrder, lift(keysH));
  stack(modelOrder, lift(modelsH));
  nodes.push(...keyOrder, ...modelOrder, ...accountNodes);

  // ---- columns ------------------------------------------------------------------------------
  const wK = clamp(width * 0.2, 82, 190);
  const wM = clamp(width * 0.27, 100, 270);
  const wA = clamp(width * 0.31, 110, 330);
  const between = Math.max(24, (width - wK - wM - wA) / 2);
  const cols = [
    { x: 0, w: wK },
    { x: wK + between, w: wM },
    { x: width - wA, w: wA },
  ];

  // ---- where each link leaves and arrives -----------------------------------------------------
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const thickness = (l: MapLink) => Math.max(1.5, l.requests * scale);
  for (const l of links) {
    const to = byId.get(l.to);
    if (to?.col === 2) l.provider = to.provider;
  }
  const attach = (side: "from" | "to") => {
    const other = side === "from" ? "to" : "from";
    for (const n of nodes) {
      const mine = links
        .filter((l) => l[side] === n.id)
        .sort((a, b) => (byId.get(a[other])?.y ?? 0) - (byId.get(b[other])?.y ?? 0));
      if (mine.length === 0) continue;
      const sum = mine.reduce((s, l) => s + thickness(l), 0);
      // Many thin links on a short row: squeeze them into it.
      const fit = Math.min(1, (n.h - 4) / sum);
      let y = n.y + (n.h - sum * fit) / 2;
      for (const l of mine) {
        const t = thickness(l) * fit;
        if (side === "from") {
          l.y0 = y + t / 2;
          l.t0 = t;
        } else {
          l.y1 = y + t / 2;
          l.t1 = t;
        }
        y += t;
      }
    }
  };
  attach("from");
  attach("to");

  return {
    cols,
    nodes,
    links,
    groups,
    width,
    height,
    scale,
    compact,
    nodeOf: (kind, ref) => nodeFor.get(`${kind}:${ref}`),
  };
}

/** The outline of a link as a band between two columns: flat ends, an S-curve between them. */
export function ribbon(x0: number, x1: number, y0: number, y1: number, t0: number, t1: number): string {
  const xm = (x0 + x1) / 2;
  const a = t0 / 2;
  const b = t1 / 2;
  const f = (n: number) => n.toFixed(1);
  return (
    `M${f(x0)} ${f(y0 - a)}C${f(xm)} ${f(y0 - a)} ${f(xm)} ${f(y1 - b)} ${f(x1)} ${f(y1 - b)}` +
    `L${f(x1)} ${f(y1 + b)}C${f(xm)} ${f(y1 + b)} ${f(xm)} ${f(y0 + a)} ${f(x0)} ${f(y0 + a)}Z`
  );
}
