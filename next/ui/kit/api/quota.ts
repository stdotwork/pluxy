import type { Host } from "../host";
import { one } from "./path";

export interface QuotaLimit {
  id: string;
  label?: string;
  used_percent?: number;
  used?: number;
  limit?: number;
  remaining?: number;
  unit?: string;
  unlimited?: boolean;
  resets_at?: number;
  window_seconds?: number;
  models?: string[];
  /** A limit on one feature of the plan, not on the account's requests as a whole. */
  feature?: boolean;
}

export interface Quota {
  plan?: string;
  plan_ends_at?: number;
  limits: QuotaLimit[];
}

export interface QuotaReply {
  quota: Quota;
  fetched_at: string;
}

export const quotaApi = (host: Host) => ({
  quota: (id: string) => host.call<QuotaReply>("GET", `/accounts/${one(id)}/quota`),
});
