import { isRecord } from './managementApi';

const domainFromValue = (value: unknown): 'ai' | 'com' | null => {
  if (typeof value !== 'string') return null;
  const domain = value.trim().toLowerCase();
  if (['ai', 'kimi-ai', 'kimi.ai'].includes(domain) || domain.endsWith('.kimi.ai')) return 'ai';
  if (['com', 'kimi', 'kimi.com'].includes(domain) || domain.endsWith('.kimi.com')) return 'com';
  return null;
};

const domainFromUrl = (value: unknown): 'ai' | 'com' | null => {
  if (typeof value !== 'string') return null;
  try {
    const host = new URL(value.trim()).hostname.toLowerCase();
    if (host === 'kimi.ai' || host.endsWith('.kimi.ai')) return 'ai';
    if (host === 'kimi.com' || host.endsWith('.kimi.com')) return 'com';
  } catch { /* Unrecognized URLs do not override the credential type. */ }
  return null;
};

/** Match Management Center's credential precedence; only return trusted API hosts. */
export function parseKimiQuotaUrl(payload: unknown, file: Record<string, unknown>): string {
  if (typeof payload === 'string') {
    try { payload = JSON.parse(payload); } catch { throw new Error('Invalid Kimi credential'); }
  }
  if (!isRecord(payload)) throw new Error('Invalid Kimi credential');
  const explicit = typeof payload.domain === 'string' && payload.domain.trim()
    ? domainFromValue(payload.domain) ?? 'com' : null;
  const baseUrl = Object.prototype.hasOwnProperty.call(payload, 'base_url')
    ? payload.base_url : payload['base-url'];
  const provider = String(file.provider ?? file.type ?? '').trim().replace(/_/g, '-');
  const domain = explicit ?? domainFromUrl(baseUrl) ?? domainFromValue(payload.type)
    ?? domainFromValue(provider)
    ?? (/kimi-ai|kimi\.ai/i.test(`${file.id ?? ''} ${file.name ?? ''}`) ? 'ai' : 'com');
  return `https://api.kimi.${domain}/coding/v1/usages`;
}

export function parseKimiPlanName(payload: unknown): string | undefined {
  if (!isRecord(payload) || typeof payload.user_id !== 'string' || !payload.user_id.trim()) return undefined;
  return typeof payload.user_level_name === 'string' ? payload.user_level_name.trim() || undefined : undefined;
}
