import type { Host } from "../host";

export interface UsageEvent {
  seq: number;
  id?: string;
  timestamp: string;
  /** The client API path the request came to, such as /v1/chat/completions. */
  endpoint?: string;
  stream?: boolean;
  model: string;
  /** The model's name as sent upstream, and as the reply named it, when either differs. */
  upstream_model?: string;
  served_model?: string;
  provider?: string;
  account?: string;
  key_id?: string;
  client_ip?: string;
  failed: boolean;
  status?: number;
  /** The code of the error that ended a failed request, such as `quota` or `upstream`. */
  error_code?: string;
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
  /** What the upstream reported; an absent kind was not reported, which is not zero. */
  tokens: {
    input_tokens?: number;
    output_tokens?: number;
    reasoning_tokens?: number;
    cached_tokens?: number;
    cache_creation_tokens?: number;
    total_tokens?: number;
  };
}

export interface UsageTried {
  account: string;
  provider: string;
  model?: string;
  result: string;
}

export interface UsageEventsReply {
  /** Whether core records usage; absent from a core that predates it. */
  enabled?: boolean;
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
  /**
   * One page of requests by core's own query, such as `before=0&limit=50&since=1h` for the newest
   * fifty of the last hour: `before` pages newest first, `after` oldest first, and the filters
   * are those of usageEvents plus `until`.
   */
  usageEventsPage: (query: string) => host.call<UsageEventsReply>("GET", `/usage/events?${query}`),
});
