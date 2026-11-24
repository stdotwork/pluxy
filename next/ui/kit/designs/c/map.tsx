// The map: requests drawn left to right from client key to model to account, each link as thick
// as its share of the requests in the range. The links are an SVG behind real buttons, so every
// node can be focused, pressed and read aloud; the same numbers are a set of tables in MapTable.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { type Fleet, type Flow, MAX_MODELS } from "./data";
import {
  HEAD_H,
  layoutMap,
  type MapAccount,
  type MapInput,
  type MapLayout,
  type MapNode,
  ribbon,
} from "./flow";
import { Icon } from "./icons";
import { type Condition, compact, count, share, untilWords } from "./lib";
import { ProviderTile } from "./parts";

export type Pick = { kind: "key" | "model" | "account"; ref: string };

export const samePick = (a: Pick | undefined, b: Pick | undefined) =>
  a?.kind === b?.kind && a?.ref === b?.ref;

/** What the map draws, from the fleet and a flow: every key, model and account with its requests. */
export function mapInput(
  fleet: Fleet,
  flow: Flow | undefined,
  conditions: Map<string, Condition>,
): Omit<MapInput, "expanded" | "width"> {
  const groups = (list: Flow["summary"]["keys"] | undefined) => new Map((list ?? []).map((g) => [g.name, g]));
  const byKey = groups(flow?.summary.keys);
  const byModel = groups(flow?.summary.models);
  const byAccount = groups(flow?.summary.accounts);
  const keyIds = new Set([...(fleet.keys ?? []).map((k) => k.id), ...byKey.keys()]);
  const keys = [...keyIds].map((id) => ({
    ref: id,
    label: id ? fleet.keyName(id) : "No client key",
    requests: byKey.get(id)?.total_requests ?? 0,
    failed: byKey.get(id)?.failure_count ?? 0,
  }));
  const chains = new Map((fleet.chains ?? []).map((c) => [c.name, c.steps.length]));
  const modelIds = new Set([...byModel.keys(), ...chains.keys()]);
  const models = [...modelIds].map((id) => ({
    ref: id,
    label: id,
    requests: byModel.get(id)?.total_requests ?? 0,
    failed: byModel.get(id)?.failure_count ?? 0,
    chain: chains.get(id),
  }));
  const accounts: MapAccount[] = (fleet.accounts ?? []).map((a) => ({
    ref: a.id,
    label: fleet.mask(a.id),
    requests: byAccount.get(a.id)?.total_requests ?? 0,
    failed: byAccount.get(a.id)?.failure_count ?? 0,
    provider: a.extension ?? "",
    flagged: conditions.get(a.id)?.key !== "available",
  }));
  // An account core no longer has, with requests in the range: its links still need an end.
  const known = new Set(accounts.map((a) => a.ref));
  for (const [id, g] of byAccount) {
    if (known.has(id) || !id) continue;
    accounts.push({
      ref: id,
      label: fleet.mask(id),
      requests: g.total_requests,
      failed: g.failure_count,
      provider: g.provider ?? "",
      flagged: false,
    });
  }
  return { keys, models, accounts, keyModel: flow?.keyModel ?? [], modelAccount: flow?.modelAccount ?? [] };
}

/** The width of an element, followed as it changes. */
function useWidth(): [{ current: HTMLDivElement | null }, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const sizes = new ResizeObserver(() => setWidth(el.clientWidth));
    sizes.observe(el);
    return () => sizes.disconnect();
  }, []);
  return [ref, width];
}

const COLUMN_NAMES = ["Client keys", "Models", "Accounts"] as const;

export function FlowMap(props: {
  fleet: Fleet;
  flow: Flow | undefined;
  /** The flow of the selection alone, drawn in full over the faded whole. */
  picked?: Flow;
  pick?: Pick;
  onPick: (pick: Pick | undefined) => void;
  conditions: Map<string, Condition>;
  now: number;
  rangeWords: string;
}) {
  const { fleet, flow, pick, conditions } = props;
  const [box, width] = useWidth();
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [hover, setHover] = useState<string>();
  const input = useMemo(() => mapInput(fleet, flow, conditions), [fleet, flow, conditions]);
  const layout = useMemo<MapLayout | undefined>(
    () => (width > 0 ? layoutMap({ ...input, expanded, width }) : undefined),
    [input, expanded, width],
  );

  // What the selection carries, by node and by link, folded the way the map is.
  const over = useMemo(() => {
    if (!layout || !pick || !props.picked) return undefined;
    const nodes = new Map<string, number>();
    const links = new Map<string, { requests: number; failed: number }>();
    const s = props.picked.summary;
    const addNode = (kind: "key" | "model" | "account", list: typeof s.keys) => {
      for (const g of list) {
        const id = layout.nodeOf(kind, g.name);
        if (id) nodes.set(id, (nodes.get(id) ?? 0) + g.total_requests);
      }
    };
    addNode("key", s.keys);
    addNode("model", s.models);
    addNode("account", s.accounts);
    const addLinks = (from: "key" | "model", to: "model" | "account", list: Flow["keyModel"]) => {
      for (const l of list) {
        const a = layout.nodeOf(from, l.from);
        const b = layout.nodeOf(to, l.to);
        if (!a || !b) continue;
        const id = `${a}>${b}`;
        const kept = links.get(id) ?? { requests: 0, failed: 0 };
        links.set(id, { requests: kept.requests + l.requests, failed: kept.failed + l.failed });
      }
    };
    addLinks("key", "model", props.picked.keyModel);
    addLinks("model", "account", props.picked.modelAccount);
    return { nodes, links };
  }, [layout, pick, props.picked]);

  // One tab stop for the whole map; the arrow keys move inside it.
  const [focusId, setFocusId] = useState<string>();
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  const tabStop = layout?.nodes.some((n) => n.id === focusId) ? focusId : layout?.nodes[0]?.id;
  const move = (from: MapNode, key: string) => {
    if (!layout) return;
    const column = (col: number) => layout.nodes.filter((n) => n.col === col).sort((a, b) => a.y - b.y);
    const own = column(from.col);
    const at = own.findIndex((n) => n.id === from.id);
    let to: MapNode | undefined;
    if (key === "ArrowDown") to = own[at + 1];
    else if (key === "ArrowUp") to = own[at - 1];
    else if (key === "Home") to = own[0];
    else if (key === "End") to = own[own.length - 1];
    else if (key === "ArrowLeft" || key === "ArrowRight") {
      const next = column(from.col + (key === "ArrowRight" ? 1 : -1));
      const mid = from.y + from.h / 2;
      to = next.sort((a, b) => Math.abs(a.y + a.h / 2 - mid) - Math.abs(b.y + b.h / 2 - mid))[0];
    }
    if (!to) return;
    setFocusId(to.id);
    buttons.current.get(to.id)?.focus();
  };

  const total = flow?.summary.total_requests ?? 0;
  const columnCounts = [input.keys.length, input.models.length, input.accounts.length];
  // The models whose links to accounts were read: only for those is a missing link a fact.
  const linksRead = new Set((flow?.summary.models ?? []).slice(0, MAX_MODELS).map((m) => m.name));
  const pickedId = pick && layout ? layout.nodeOf(pick.kind, pick.ref) : undefined;
  // The links a hovered or selected node touches stay strong; the others step back.
  const lit = hover ?? (over ? undefined : pickedId);

  return (
    <div class="dc-map" ref={box} data-compact={layout?.compact ? "" : undefined}>
      {layout && (
        <>
          <div class="dc-map-cols" aria-hidden="true">
            {layout.cols.map((c, i) => (
              <span key={COLUMN_NAMES[i]} class="dc-map-col" style={{ left: `${c.x}px`, width: `${c.w}px` }}>
                {COLUMN_NAMES[i]} <span class="dc-map-col-n">{columnCounts[i]}</span>
              </span>
            ))}
          </div>
          {/* biome-ignore lint/a11y/useSemanticElements: a fieldset groups form controls; this groups the map's nodes */}
          <div
            class="dc-map-body"
            role="group"
            aria-label={`Request flow in ${props.rangeWords}: client keys, then models, then accounts. Arrow keys move between nodes; Enter filters the page by one.`}
            style={{ height: `${layout.height}px` }}
            data-picked={over ? "" : undefined}
          >
            <svg
              class="dc-map-links"
              width={layout.width}
              height={layout.height}
              viewBox={`0 0 ${layout.width} ${layout.height}`}
              aria-hidden="true"
              focusable="false"
            >
              <defs>
                <pattern
                  id="dc-hatch"
                  width="5"
                  height="5"
                  patternUnits="userSpaceOnUse"
                  patternTransform="rotate(45)"
                >
                  <rect width="5" height="5" class="dc-hatch-ground" />
                  <rect width="2.2" height="5" class="dc-hatch-line" />
                </pattern>
              </defs>
              {layout.links.map((l) => {
                const from = layout.nodes.find((n) => n.id === l.from);
                const to = layout.nodes.find((n) => n.id === l.to);
                if (!from || !to) return null;
                const c0 = layout.cols[from.col];
                const c1 = layout.cols[to.col];
                if (!c0 || !c1) return null;
                const x0 = c0.x + c0.w + 3;
                const x1 = c1.x - 3;
                const slot = l.provider !== undefined ? fleet.providers.of(l.provider).slot : undefined;
                const part = over?.links.get(l.id);
                const ratio = over ? (part ? Math.min(1, part.requests / l.requests) : 0) : 1;
                const shown = over ? part : l;
                // Where a request failed is at an account: the links from the keys stay plain.
                const failRatio =
                  to.col === 2 && shown && shown.requests > 0 ? shown.failed / shown.requests : 0;
                const t0 = Math.max(over && part ? 1.5 : 0, l.t0 * ratio);
                const t1 = Math.max(over && part ? 1.5 : 0, l.t1 * ratio);
                const f0 = failRatio > 0 ? Math.max(1.5, t0 * failRatio) : 0;
                const f1 = failRatio > 0 ? Math.max(1.5, t1 * failRatio) : 0;
                const touches = lit !== undefined && (l.from === lit || l.to === lit);
                return (
                  <g
                    key={l.id}
                    class="dc-link"
                    data-slot-p={slot}
                    data-lit={touches ? "" : undefined}
                    data-dim={lit !== undefined && !touches ? "" : undefined}
                  >
                    <title>
                      {`${from.label} to ${to.label}: ${count(l.requests)} requests (${share(l.requests, total)})` +
                        (l.failed ? `, ${count(l.failed)} failed` : "")}
                    </title>
                    {over && <path class="dc-link-ghost" d={ribbon(x0, x1, l.y0, l.y1, l.t0, l.t1)} />}
                    {t0 > 0 && <path class="dc-link-band" d={ribbon(x0, x1, l.y0, l.y1, t0, t1)} />}
                    {f0 > 0 && (
                      <path
                        class="dc-link-failed"
                        d={ribbon(x0, x1, l.y0 + (t0 - f0) / 2, l.y1 + (t1 - f1) / 2, f0, f1)}
                      />
                    )}
                  </g>
                );
              })}
              {!over &&
                layout.nodes.map((n) => {
                  // Requests for a model that reached no account: a short failed link that ends.
                  const c = layout.cols[1];
                  if (!c || n.kind !== "model" || !linksRead.has(n.ref)) return null;
                  const sent = layout.links.reduce((sum, l) => (l.from === n.id ? sum + l.requests : sum), 0);
                  const lost = n.requests - sent;
                  if (lost <= 0) return null;
                  const t = Math.min(10, Math.max(2.5, lost * layout.scale));
                  const x = c.x + c.w + 3;
                  const y = n.y + n.h - t / 2 - 5;
                  return (
                    <g key={`lost:${n.id}`} class="dc-link">
                      <title>{`${n.label}: ${count(lost)} requests reached no account`}</title>
                      <path class="dc-link-failed" d={ribbon(x, x + 20, y, y, t, t)} />
                      <path class="dc-link-end" d={`M${x + 23} ${y - 4}l8 8m0 -8l-8 8`} />
                    </g>
                  );
                })}
              {layout.groups.map((g) => {
                const c = layout.cols[2];
                if (!c) return null;
                return (
                  <rect
                    key={g.provider}
                    class="dc-map-spine"
                    data-slot-p={fleet.providers.of(g.provider).slot}
                    x={c.x - 3}
                    y={g.y + HEAD_H}
                    width={3}
                    height={Math.max(0, g.bottom - g.y - HEAD_H)}
                    rx={1.5}
                  />
                );
              })}
            </svg>
            {layout.groups.map((g) => {
              const c = layout.cols[2];
              const p = fleet.providers.of(g.provider);
              if (!c) return null;
              return (
                <div
                  key={g.provider}
                  class="dc-map-group"
                  style={{ left: `${c.x}px`, top: `${g.y}px`, width: `${c.w}px` }}
                >
                  <ProviderTile p={p} />
                  <span class="dc-ellipsis">{p.name}</span>
                  <span class="dc-muted dc-num">{g.accounts}</span>
                </div>
              );
            })}
            {layout.nodes.map((n) => {
              const c = layout.cols[n.col];
              if (!c) return null;
              const condition = n.kind === "account" ? conditions.get(n.ref) : undefined;
              const selected = pickedId === n.id && n.kind !== "more";
              const carried = over ? (over.nodes.get(n.id) ?? 0) : n.requests;
              const quiet = over ? carried === 0 && !selected : false;
              const until = condition ? untilWords(condition, props.now) : undefined;
              const state = condition && condition.key !== "available" ? condition : undefined;
              const selectable = n.kind !== "more" && n.ref !== "";
              const what =
                n.kind === "key"
                  ? "Client key"
                  : n.kind === "model"
                    ? n.chain
                      ? "Fallback chain"
                      : "Model"
                    : n.kind === "account"
                      ? "Account"
                      : "";
              const label =
                n.kind === "more"
                  ? `Show ${n.folded} more ${n.col === 0 ? "client keys" : n.col === 1 ? "models" : `accounts of ${fleet.providers.of(n.provider).name}`}, ${count(n.requests)} requests`
                  : `${what} ${n.label}` +
                    (n.kind === "account" ? `, ${fleet.providers.of(n.provider).name}` : "") +
                    (condition ? `, ${condition.word}${until ? `, ${until}` : ""}` : "") +
                    `, ${count(n.requests)} requests, ${share(n.requests, total)} of all` +
                    (n.failed ? `, ${count(n.failed)} failed` : "");
              return (
                <button
                  key={n.id}
                  type="button"
                  class="dc-node"
                  data-kind={n.kind}
                  data-slot-p={
                    n.kind === "account" || n.provider ? fleet.providers.of(n.provider).slot : undefined
                  }
                  data-quiet={quiet ? "" : undefined}
                  data-tone={state?.tone}
                  style={{ left: `${c.x}px`, top: `${n.y}px`, width: `${c.w}px`, height: `${n.h}px` }}
                  tabIndex={tabStop === n.id ? 0 : -1}
                  aria-pressed={selectable ? selected : undefined}
                  aria-label={label}
                  title={n.label}
                  ref={(el) => {
                    if (el) buttons.current.set(n.id, el);
                    else buttons.current.delete(n.id);
                  }}
                  onFocus={() => setFocusId(n.id)}
                  onMouseEnter={() => setHover(n.id)}
                  onMouseLeave={() => setHover(undefined)}
                  onKeyDown={(e) => {
                    if (["ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) {
                      e.preventDefault();
                      move(n, e.key);
                    }
                  }}
                  onClick={() => {
                    if (n.kind === "more" && n.group) {
                      setExpanded(new Set([...expanded, n.group]));
                      return;
                    }
                    if (!selectable) return;
                    props.onPick(selected ? undefined : { kind: n.kind as Pick["kind"], ref: n.ref });
                  }}
                >
                  <span class="dc-node-row">
                    {n.kind === "key" && <Icon name="key" size={14} />}
                    {n.kind === "model" && n.chain && <Icon name="chain" size={14} />}
                    {n.kind === "more" && <Icon name="plus" size={13} />}
                    {state && <Icon name={state.icon} size={15} class="dc-node-state" />}
                    <span class="dc-node-name">{n.label}</span>
                    {carried > 0 && <span class="dc-node-count">{compact(carried)}</span>}
                  </span>
                  {state && (
                    <span class="dc-node-note">
                      {state.word}
                      {until ? ` · ${until.replace("Resets in ", "")}` : ""}
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          {expanded.size > 0 && (
            <button type="button" class="dc-map-fold" onClick={() => setExpanded(new Set())}>
              Show fewer
            </button>
          )}
        </>
      )}
    </div>
  );
}

/** The map as it looks before anything is read: the three columns, with no numbers in them. */
export function MapSkeleton(props: { words: string }) {
  const rows = [3, 5, 6];
  return (
    <div class="dc-map dc-map-skeleton" role="status">
      <span class="dc-sr">{props.words}</span>
      <div class="dc-map-skel-cols" aria-hidden="true">
        {rows.map((n, i) => (
          <div key={COLUMN_NAMES[i]}>
            <span class="dc-map-col">{COLUMN_NAMES[i]}</span>
            {Array.from({ length: n }, (_, j) => (
              <span key={j} class="dc-skel dc-map-skel-node" />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

const useAnnounce = (text: string) => {
  const [said, setSaid] = useState("");
  useEffect(() => setSaid(text), [text]);
  return said;
};

/**
 * The map's numbers as tables: every client key, model and account with its requests, and each
 * link between them. It is the map for a keyboard or a screen reader, and for exact numbers.
 */
export function MapTable(props: {
  fleet: Fleet;
  flow: Flow | undefined;
  pick?: Pick;
  onPick: (pick: Pick | undefined) => void;
  conditions: Map<string, Condition>;
  now: number;
}) {
  const { fleet, flow, pick, conditions } = props;
  const input = useMemo(() => mapInput(fleet, flow, conditions), [fleet, flow, conditions]);
  const total = flow?.summary.total_requests ?? 0;
  const said = useAnnounce(pick ? `Filtered by ${pick.kind} ${pick.ref}` : "");
  const name = (kind: Pick["kind"], ref: string, label: string) =>
    ref ? (
      <button
        type="button"
        class="dc-rowpick"
        aria-pressed={samePick(pick, { kind, ref })}
        onClick={() => props.onPick(samePick(pick, { kind, ref }) ? undefined : { kind, ref })}
      >
        {label}
      </button>
    ) : (
      label
    );
  const sorted = <T extends { requests: number; label: string }>(list: T[]) =>
    [...list].sort((a, b) => b.requests - a.requests || a.label.localeCompare(b.label));
  const links = [
    ...(flow?.keyModel ?? []).map((l) => ({ ...l, a: fleet.keyName(l.from), b: l.to })),
    ...(flow?.modelAccount ?? []).map((l) => ({ ...l, a: l.from, b: fleet.mask(l.to) })),
  ].sort((x, y) => y.requests - x.requests);
  return (
    <div class="dc-maptable">
      <span class="dc-sr" role="status">
        {said}
      </span>
      <div class="dc-tablewrap">
        <table class="dc-table" aria-label="Client keys">
          <thead>
            <tr>
              <th scope="col">Client key</th>
              <th scope="col" class="dc-r">
                Requests
              </th>
              <th scope="col" class="dc-r">
                Share
              </th>
              <th scope="col" class="dc-r">
                Failed
              </th>
            </tr>
          </thead>
          <tbody>
            {sorted(input.keys).map((k) => (
              <tr key={k.ref}>
                <th scope="row" class="dc-mono">
                  {name("key", k.ref, k.label)}
                </th>
                <td class="dc-r dc-num">{count(k.requests)}</td>
                <td class="dc-r dc-num">{share(k.requests, total)}</td>
                <td class="dc-r dc-num">{count(k.failed)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div class="dc-tablewrap">
        <table class="dc-table" aria-label="Models">
          <thead>
            <tr>
              <th scope="col">Model</th>
              <th scope="col" class="dc-r">
                Requests
              </th>
              <th scope="col" class="dc-r">
                Share
              </th>
              <th scope="col" class="dc-r">
                Failed
              </th>
            </tr>
          </thead>
          <tbody>
            {sorted(input.models).map((m) => (
              <tr key={m.ref}>
                <th scope="row">
                  {name("model", m.ref, m.label)}
                  {m.chain ? <span class="dc-muted"> · chain of {m.chain}</span> : null}
                </th>
                <td class="dc-r dc-num">{count(m.requests)}</td>
                <td class="dc-r dc-num">{share(m.requests, total)}</td>
                <td class="dc-r dc-num">{count(m.failed)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div class="dc-tablewrap">
        <table class="dc-table" aria-label="Accounts">
          <thead>
            <tr>
              <th scope="col">Account</th>
              <th scope="col">Provider</th>
              <th scope="col">State</th>
              <th scope="col" class="dc-r">
                Requests
              </th>
              <th scope="col" class="dc-r">
                Share
              </th>
              <th scope="col" class="dc-r">
                Failed
              </th>
            </tr>
          </thead>
          <tbody>
            {sorted(input.accounts).map((a) => {
              const c = conditions.get(a.ref);
              const until = c ? untilWords(c, props.now) : undefined;
              return (
                <tr key={a.ref}>
                  <th scope="row">{name("account", a.ref, a.label)}</th>
                  <td>{fleet.providers.of(a.provider).name}</td>
                  <td>{c ? `${c.word}${until ? ` · ${until}` : ""}` : "–"}</td>
                  <td class="dc-r dc-num">{count(a.requests)}</td>
                  <td class="dc-r dc-num">{share(a.requests, total)}</td>
                  <td class="dc-r dc-num">{count(a.failed)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div class="dc-tablewrap">
        <table class="dc-table" aria-label="Links: where requests went">
          <thead>
            <tr>
              <th scope="col">From</th>
              <th scope="col">To</th>
              <th scope="col" class="dc-r">
                Requests
              </th>
              <th scope="col" class="dc-r">
                Failed
              </th>
            </tr>
          </thead>
          <tbody>
            {links.map((l) => (
              <tr key={`${l.a}>${l.b}`}>
                <th scope="row">{l.a}</th>
                <td>{l.b}</td>
                <td class="dc-r dc-num">{count(l.requests)}</td>
                <td class="dc-r dc-num">{count(l.failed)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
