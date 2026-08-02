import { apiCallErrorMessage, managementApi } from './managementApi';
import {
  fetchModels,
  normalizeBaseUrl,
  type ModelOption,
  type ModelProvider,
} from './modelService';

export const PROVIDER_HEALTH_TIMEOUT_MS = 15_000;

export type ProviderHealthStatus = 'healthy' | 'partial' | 'failed';

export type ProviderHealthProbe = {
  url: string;
  header: Record<string, string>;
  data: string;
};

export type ProviderHealthProbeResult = {
  success: boolean;
  latencyMs?: number;
  error?: string;
  timedOut?: boolean;
};

export type ProviderHealthResult = {
  status: ProviderHealthStatus;
  model: string;
  successCount: number;
  totalCount: number;
  latencyMs?: number;
  errors: string[];
  timedOut: boolean;
};

export type ProviderHealthCheckOptions = {
  provider: ModelProvider;
  baseUrl: string;
  apiKeys: string[];
  authIndex?: string;
  models: ModelOption[];
  testModel?: string;
  customHeaders?: Record<string, string>;
  timeoutMs?: number;
};

export class ProviderHealthCheckError extends Error {
  readonly code: 'no-model';

  constructor(code: 'no-model') {
    super(code);
    this.name = 'ProviderHealthCheckError';
    this.code = code;
  }
}

const defaultBaseUrl = (provider: ModelProvider) => {
  if (provider === 'claude') return 'https://api.anthropic.com';
  if (provider === 'gemini') return 'https://generativelanguage.googleapis.com';
  return '';
};

const endpointRoot = (provider: ModelProvider, baseUrl: string) => {
  const normalized = normalizeBaseUrl(baseUrl.trim() || defaultBaseUrl(provider));
  return normalized.replace(/\/(?:v1beta|v1)$/i, '');
};

const hasHeader = (headers: Record<string, string>, name: string) =>
  Object.keys(headers).some((key) => key.toLowerCase() === name.toLowerCase());

const headerValue = (headers: Record<string, string>, name: string) =>
  Object.entries(headers).find(([key]) => key.toLowerCase() === name.toLowerCase())?.[1] ?? '';

const setHeaderIfMissing = (
  headers: Record<string, string>,
  name: string,
  value: string,
) => {
  if (!hasHeader(headers, name)) headers[name] = value;
};

export function selectProviderHealthModel(
  testModel: string | undefined,
  models: ModelOption[],
): string {
  return testModel?.trim() || models.find((model) => model.name.trim())?.name.trim() || '';
}

export function buildProviderHealthProbe(
  provider: ModelProvider,
  baseUrl: string,
  model: string,
  apiKey: string,
  authIndex = '',
  customHeaders: Record<string, string> = {},
): ProviderHealthProbe {
  const root = endpointRoot(provider, baseUrl);
  const headers = { ...customHeaders };
  const key = apiKey.trim();
  setHeaderIfMissing(headers, 'Content-Type', 'application/json');

  if (provider === 'gemini') {
    if (key) setHeaderIfMissing(headers, 'x-goog-api-key', key);
    else if (authIndex) setHeaderIfMissing(headers, 'x-goog-api-key', '$TOKEN$');
    const normalizedModel = model.trim().replace(/^models\//i, '');
    return {
      url: `${root}/v1beta/models/${encodeURIComponent(normalizedModel)}:generateContent`,
      header: headers,
      data: JSON.stringify({
        contents: [{ parts: [{ text: 'hi' }] }],
        generationConfig: { maxOutputTokens: 1 },
      }),
    };
  }

  if (provider === 'claude') {
    const bearerToken = headerValue(headers, 'authorization').match(/^Bearer\s+(.+)$/i)?.[1]?.trim() ?? '';
    if (key) setHeaderIfMissing(headers, 'x-api-key', key);
    else if (bearerToken) setHeaderIfMissing(headers, 'x-api-key', bearerToken);
    else if (authIndex) setHeaderIfMissing(headers, 'x-api-key', '$TOKEN$');
    setHeaderIfMissing(headers, 'anthropic-version', '2023-06-01');
    return {
      url: `${root}/v1/messages`,
      header: headers,
      data: JSON.stringify({
        model: model.trim(),
        max_tokens: 1,
        messages: [{ role: 'user', content: 'hi' }],
      }),
    };
  }

  if (key) setHeaderIfMissing(headers, 'Authorization', `Bearer ${key}`);
  else if (authIndex) setHeaderIfMissing(headers, 'Authorization', 'Bearer $TOKEN$');

  if (provider === 'codex') {
    return {
      url: `${root}/v1/responses`,
      header: headers,
      data: JSON.stringify({
        model: model.trim(),
        input: 'hi',
        max_output_tokens: 1,
      }),
    };
  }

  return {
    url: `${root}/v1/chat/completions`,
    header: headers,
    data: JSON.stringify({
      model: model.trim(),
      messages: [{ role: 'user', content: 'hi' }],
      max_tokens: 1,
      stream: false,
    }),
  };
}

const isTimeoutError = (message: string) =>
  /timed?\s*out|timeout|deadline has elapsed|超时/i.test(message);

const errorMessage = (error: unknown) => {
  if (error instanceof Error) return error.message.trim() || error.name;
  return String(error).replace(/^Error:\s*/i, '').trim();
};

export async function checkProviderHealthProbe(
  provider: ModelProvider,
  baseUrl: string,
  model: string,
  apiKey: string,
  authIndex = '',
  customHeaders: Record<string, string> = {},
  timeoutMs = PROVIDER_HEALTH_TIMEOUT_MS,
): Promise<ProviderHealthProbeResult> {
  const probe = buildProviderHealthProbe(
    provider,
    baseUrl,
    model,
    apiKey,
    authIndex,
    customHeaders,
  );
  const startedAt = performance.now();
  try {
    const response = await managementApi.post<Record<string, unknown>>('/api-call', {
      authIndex: authIndex.trim() || undefined,
      method: 'POST',
      url: probe.url,
      header: probe.header,
      data: probe.data,
    }, { timeoutMs });
    const status = Number(response.status_code ?? response.statusCode ?? 0);
    if (status < 200 || status >= 300) {
      return {
        success: false,
        error: apiCallErrorMessage(response),
      };
    }
    return {
      success: true,
      latencyMs: Math.max(1, Math.round(performance.now() - startedAt)),
    };
  } catch (error) {
    const message = errorMessage(error);
    return {
      success: false,
      error: message,
      timedOut: isTimeoutError(message),
    };
  }
}

export function aggregateProviderHealthResults(
  model: string,
  results: ProviderHealthProbeResult[],
): ProviderHealthResult {
  const successful = results.filter((result) => result.success);
  const status: ProviderHealthStatus = successful.length === results.length
    ? 'healthy'
    : successful.length > 0
      ? 'partial'
      : 'failed';
  const latencies = successful
    .map((result) => result.latencyMs)
    .filter((latency): latency is number => latency !== undefined);
  const errors = results
    .map((result) => result.error?.trim() ?? '')
    .filter((message, index, messages) => message && messages.indexOf(message) === index);
  return {
    status,
    model,
    successCount: successful.length,
    totalCount: results.length,
    ...(latencies.length > 0 ? { latencyMs: Math.min(...latencies) } : {}),
    errors,
    timedOut: results.some((result) => result.timedOut),
  };
}

const healthCredentials = (apiKeys: string[]) => {
  const keys = apiKeys
    .map((key) => key.trim())
    .filter((key, index, values) => key && values.indexOf(key) === index);
  return keys.length > 0 ? keys : [''];
};

export async function checkProviderHealth(
  options: ProviderHealthCheckOptions,
): Promise<ProviderHealthResult> {
  const timeoutMs = options.timeoutMs ?? PROVIDER_HEALTH_TIMEOUT_MS;
  const credentials = healthCredentials(options.apiKeys);
  const customHeaders = options.customHeaders ?? {};
  let model = selectProviderHealthModel(options.testModel, options.models);

  if (!model) {
    let discoveryError: unknown;
    for (const apiKey of credentials) {
      try {
        const discovered = await fetchModels(
          options.provider,
          options.baseUrl,
          apiKey,
          options.authIndex,
          customHeaders,
          timeoutMs,
        );
        model = selectProviderHealthModel(undefined, discovered);
        if (model) break;
      } catch (error) {
        discoveryError = error;
      }
    }
    if (!model && discoveryError) throw discoveryError;
    if (!model) throw new ProviderHealthCheckError('no-model');
  }

  const results = await Promise.all(credentials.map((apiKey) => checkProviderHealthProbe(
    options.provider,
    options.baseUrl,
    model,
    apiKey,
    options.authIndex,
    customHeaders,
    timeoutMs,
  )));
  return aggregateProviderHealthResults(model, results);
}
