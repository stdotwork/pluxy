import { isRecord, managementApi, normalizeAuthIndex, readString } from './managementApi';
import type { AuthFile, QuotaRow, QuotaState } from './quotaService';

export const credentialQuotaPlugin = (file: AuthFile): string =>
  file.supports_quota === false || file.supportsQuota === false ? '' : readString(file, 'quota_plugin_id', 'quotaPluginId');

export function parsePluginCredentialQuota(payload: unknown): QuotaState {
  if (!isRecord(payload)) throw new Error('Invalid plugin quota response');
  if (payload.groups !== undefined && !Array.isArray(payload.groups)) throw new Error('Invalid plugin quota groups');
  const rows: QuotaRow[] = [];
  for (const group of (payload.groups ?? []) as unknown[]) {
    if (!isRecord(group) || !Array.isArray(group.buckets)) continue;
    const label = readString(group, 'displayName', 'display_name');
    for (const bucket of group.buckets) {
      if (!isRecord(bucket)) continue;
      const fraction = bucket.remainingFraction ?? bucket.remaining_fraction;
      const reset = readString(bucket, 'resetTime', 'reset_time');
      const resetAtMs = Date.parse(reset);
      rows.push({ label: [label, readString(bucket, 'window')].filter(Boolean).join(' · ') || 'Quota',
        remainingPercent: typeof fraction === 'number' && Number.isFinite(fraction) ? Math.min(100, Math.max(0, fraction * 100)) : null,
        ...(reset ? { reset } : {}), ...(Number.isFinite(resetAtMs) ? { resetAtMs } : {}),
        detail: readString(bucket, 'description') || undefined,
      });
    }
  }
  for (const metric of Array.isArray(payload.summary) ? payload.summary : []) {
    if (!isRecord(metric) || typeof metric.value !== 'number' || !Number.isFinite(metric.value)) continue;
    let detail = String(metric.value);
    if (metric.format === 'currency' && typeof metric.currency === 'string') {
      try { detail = new Intl.NumberFormat(undefined, { style: 'currency', currency: metric.currency }).format(metric.value); } catch { /* Retain the numeric amount for unknown currencies. */ }
    } else if (typeof metric.unit === 'string') detail += ` ${metric.unit}`;
    rows.push({ label: readString(metric, 'label', 'key'), remainingPercent: null, detail });
  }
  const offset = payload.serverTimeOffsetMs ?? payload.server_time_offset_ms;
  return { status: 'success', rows, plan: readString(payload.subscription, 'plan', 'tierName', 'tier_name') || undefined,
    ...(typeof offset === 'number' && Number.isFinite(offset) ? { serverTimeOffsetMs: offset } : {}), fetchedAt: Date.now() };
}

export async function loadPluginCredentialQuota(file: AuthFile): Promise<QuotaState> {
  try {
    const plugin = credentialQuotaPlugin(file);
    const authIndex = normalizeAuthIndex(file.auth_index ?? file.authIndex);
    if (!plugin || !authIndex || file.disabled === true) throw new Error('Plugin quota is unavailable for this credential');
    return parsePluginCredentialQuota(await managementApi.post(`/plugins/${encodeURIComponent(plugin)}/quota`, { auth_index: authIndex }));
  } catch (reason) { return { status: 'error', rows: [], error: reason instanceof Error ? reason.message : String(reason) }; }
}
