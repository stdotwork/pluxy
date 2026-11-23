// Where a request for a model would go now, and the fallback chains (GET /route, GET /chains;
// next/internal/route/explain.go, next/internal/core/managechains.go).

import type { Host } from "../host";

/** One step of a fallback chain: a model, at one provider or on named accounts when given. */
export interface ChainStep {
  model: string;
  provider?: string;
  accounts?: string[];
}

export interface Chain {
  name: string;
  steps: ChainStep[];
}

/** An account a request would try, in its turn. */
export interface PlanStep {
  /** The account's ID; `account` is its public ID. */
  id: string;
  /** The chain's step the account is tried for, from 1; absent with no chain. */
  step?: number;
  account: string;
  provider: string;
  model: string;
  /** What the soonest-reset strategy ranked the account by; absent under the others. */
  rank?: { reset?: string; window_seconds?: number; full_until?: string };
}

/** An account that serves the model and would not be tried now. */
export interface PlanLeftOut {
  id: string;
  step?: number;
  account: string;
  provider?: string;
  model?: string;
  /** A health state (cooling, exhausted, needs_login, banned, disabled), `busy` or `skipped`. */
  state: string;
  /** When the block that holds it ends, if core knows. */
  until?: string;
}

/** What a request for a model would do now. Asking sends nothing upstream. */
export interface RoutePlan {
  model: string;
  chain?: ChainStep[];
  order: PlanStep[];
  left_out: PlanLeftOut[];
}

export const routeApi = (host: Host) => ({
  routePlan: (model: string) => host.call<RoutePlan>("GET", `/route?model=${encodeURIComponent(model)}`),
  chains: () => host.call<{ chains: Chain[] }>("GET", "/chains"),
});
