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
  latency_ms: number;
  tokens: { total_tokens?: number };
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
