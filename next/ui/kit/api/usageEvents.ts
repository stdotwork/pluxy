import type { Host } from "../host";

export interface UsageEvent {
  seq: number;
  id?: string;
  timestamp: string;
  model: string;
  provider?: string;
  account?: string;
  key_id?: string;
  failed: boolean;
  status?: number;
  /** The step of the model's fallback chain the last attempt was for, from 1; absent for a model with no chain. */
  chain_step?: number;
  /** How many times pluxy sent the request upstream, counting the one that ended it; absent from an older record. */
  attempts?: number;
  /**
   * What each attempt came to, in order, when there was more than one or the request failed:
   * the account by its public ID, and `result` "ok" or the code of the error that ended the
   * attempt (such as `quota`, `rate_limit`, `auth`, `upstream`).
   */
  tried?: UsageTried[];
  latency_ms: number;
  tokens: { total_tokens?: number };
}

export interface UsageTried {
  account: string;
  provider: string;
  model?: string;
  result: string;
}

export interface UsageEventsReply {
  events: UsageEvent[];
  next: number;
  more: boolean;
  lost: boolean;
}

export type UsageFilters = Record<"since" | "model" | "provider" | "account" | "key_id" | "failed", string>;

export const usageEventsApi = (host: Host) => ({
  usageEvents: (filters: UsageFilters, after: number) => {
    const query = new URLSearchParams({ after: String(after), limit: "1000" });
    for (const [key, value] of Object.entries(filters)) if (value.trim()) query.set(key, value.trim());
    return host.call<UsageEventsReply>("GET", "/usage/events?" + query);
  },
});
