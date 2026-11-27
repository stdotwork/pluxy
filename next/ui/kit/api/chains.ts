// Fallback chains (next/internal/core/managechains.go): what pluxy tries for a model when no
// account can serve it. A chain is kept under the model name a tool asks for.

import type { Host } from "../host";
import { one } from "./path";

/** One step of a chain: a model, and optionally the one provider (an extension's ID) and the accounts (their IDs) that serve it. */
export interface ChainStep {
  model: string;
  provider?: string;
  accounts?: string[];
}

export interface Chain {
  /** The model a tool asks for. */
  name: string;
  steps: ChainStep[];
}

export const chainsApi = (host: Host) => ({
  chains: () => host.call<{ chains: Chain[] }>("GET", "/chains"),
  /**
   * Sets name's chain and replies with it. 409 for a settings file that is a config.yaml, which
   * cannot hold chains; 422 (refused) for more than 8 steps, a step that names another chain, a
   * repeat, a step no account serves or an account core does not know.
   */
  saveChain: (name: string, steps: ChainStep[]) => host.call<Chain>("PUT", `/chains/${one(name)}`, { steps }),
  /** Removes name's chain; 404 when there is none. */
  deleteChain: (name: string) => host.call<void>("DELETE", `/chains/${one(name)}`),
});
