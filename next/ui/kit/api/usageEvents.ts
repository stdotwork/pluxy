import type { Host } from "../host";

/** The tokens one request reported; each is absent when the provider did not say. */
export interface UsageTokens {
  /** Every input token, cached ones included. */
  input_tokens?: number;
  /** Every output token, reasoning included. */
  output_tokens?: number;
  reasoning_tokens?: number;
  /** Input tokens read from a cache. */
  cached_tokens?: number;
  /** Input tokens written to a cache. */
  cache_creation_tokens?: number;
  /** Input plus output, when either was reported. */
  total_tokens?: number;
}

export interface UsageEvent {
  seq: number;
  id?: string;
  timestamp: string;
  /** The client API path, such as /v1/chat/completions. Absent from an older record. */
  endpoint?: string;
  stream?: boolean;
  /** The model as the client asked for it. */
  model: string;
  /** The account's name for the model, when it differs (an alias, a prefix). */
  upstream_model?: string;
  /** The model the upstream reply says served it, when the reply names one. */
  served_model?: string;
  provider?: string;
  account?: string;
  key_id?: string;
  /** The client key's name when the request arrived; absent when it had none. */
  label?: string;
  failed: boolean;
  status?: number;
  /** The code of the error that ended a failed request, such as rate_limit or cancelled. */
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
  /**
   * The accounts that serve the model and that routing left out of a failed request, and why:
   * the account's public ID, its state and until when. Absent when none were left out.
   */
  left_out?: UsageLeftOut[];
  latency_ms: number;
  tokens: UsageTokens;
}

export interface UsageTried {
  account: string;
  provider: string;
  model?: string;
  result: string;
}

export interface UsageLeftOut {
  account: string;
  provider?: string;
  model?: string;
  state: string;
  until?: string;
}

export interface UsageEventsReply {
  /** Whether core records requests at all (usage.enabled). Absent from an older core. */
  enabled?: boolean;
  events: UsageEvent[];
  next: number;
  more: boolean;
  lost: boolean;
}

export type UsageFilters = Partial<
  Record<"since" | "model" | "provider" | "account" | "key_id" | "failed", string>
>;

const filtered = (filters: UsageFilters, query: URLSearchParams) => {
  for (const [key, value] of Object.entries(filters)) if (value?.trim()) query.set(key, value.trim());
  return query;
};

export const usageEventsApi = (host: Host) => ({
  usageEvents: (filters: UsageFilters, after: number) => {
    const query = filtered(filters, new URLSearchParams({ after: String(after), limit: "1000" }));
    return host.call<UsageEventsReply>("GET", `/usage/events?${query}`);
  },
  /**
   * The events older than the one numbered before (all kept events when it is 0), newest first,
   * at most limit of them; next is the oldest one's number, to read on from.
   */
  usageEventsBefore: (filters: UsageFilters, before: number, limit: number) => {
    const query = filtered(filters, new URLSearchParams({ before: String(before), limit: String(limit) }));
    return host.call<UsageEventsReply>("GET", `/usage/events?${query}`);
  },
});
