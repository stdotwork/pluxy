import { invoke } from '@tauri-apps/api/core';
import { isRecord, readNumber, readString } from './managementApi';

export type AgentUsageModel = {
  model: string;
  inputTokens: number;
  outputTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  reasoningTokens: number;
  totalTokens: number;
  costUSD: number | null;
};

export type AgentUsageDaily = {
  date: string;
  inputTokens: number;
  outputTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  reasoningTokens: number;
  totalTokens: number;
  costUSD: number;
  models: AgentUsageModel[];
};

export type AgentUsageSource = {
  source: string;
  status: 'ok' | 'empty' | 'error';
  error: string;
  daily: AgentUsageDaily[];
};

export type AgentUsageReport = {
  schemaVersion: number;
  generatedAt: string;
  sources: AgentUsageSource[];
};

const tokenValue = (value: unknown, ...keys: string[]): number => {
  const parsed = readNumber(value, ...keys);
  return parsed !== null && parsed >= 0 ? Math.floor(parsed) : 0;
};

const costValue = (value: unknown, ...keys: string[]): number => {
  const parsed = readNumber(value, ...keys);
  return parsed !== null && parsed >= 0 ? parsed : 0;
};

const optionalCostValue = (value: unknown): number | null => {
  const parsed = typeof value === 'number' ? value : readNumber(value);
  return parsed !== null && parsed >= 0 ? parsed : null;
};

const normalizeModel = (value: unknown): AgentUsageModel => {
  if (!isRecord(value)) throw new Error('Invalid Agent usage model row');
  const model = readString(value, 'model');
  if (!model) throw new Error('Agent usage model is missing');
  return {
    model,
    inputTokens: tokenValue(value, 'inputTokens'),
    outputTokens: tokenValue(value, 'outputTokens'),
    cacheCreationTokens: tokenValue(value, 'cacheCreationTokens'),
    cacheReadTokens: tokenValue(value, 'cacheReadTokens'),
    reasoningTokens: tokenValue(value, 'reasoningOutputTokens', 'reasoningTokens'),
    totalTokens: tokenValue(value, 'totalTokens'),
    costUSD: optionalCostValue(value.costUSD),
  };
};

const normalizeDaily = (value: unknown): AgentUsageDaily => {
  if (!isRecord(value)) throw new Error('Invalid Agent usage daily row');
  const date = readString(value, 'date');
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) throw new Error('Agent usage date is invalid');
  const models = Array.isArray(value.models) ? value.models.map(normalizeModel) : [];
  return {
    date,
    inputTokens: tokenValue(value, 'inputTokens'),
    outputTokens: tokenValue(value, 'outputTokens'),
    cacheCreationTokens: tokenValue(value, 'cacheCreationTokens'),
    cacheReadTokens: tokenValue(value, 'cacheReadTokens'),
    reasoningTokens: tokenValue(value, 'reasoningOutputTokens', 'reasoningTokens'),
    totalTokens: tokenValue(value, 'totalTokens'),
    costUSD: costValue(value, 'costUSD', 'totalCost'),
    models,
  };
};

const normalizeSource = (value: unknown): AgentUsageSource => {
  if (!isRecord(value)) throw new Error('Invalid Agent usage source');
  const source = readString(value, 'source');
  if (!source) throw new Error('Agent usage source is missing');
  const status = readString(value, 'status');
  if (status !== 'ok' && status !== 'empty' && status !== 'error') throw new Error('Agent usage status is invalid');
  return {
    source,
    status,
    error: readString(value, 'error'),
    daily: Array.isArray(value.daily) ? value.daily.map(normalizeDaily) : [],
  };
};

export function normalizeAgentUsageReport(value: unknown): AgentUsageReport {
  if (!isRecord(value) || !Array.isArray(value.sources)) throw new Error('Invalid Agent usage report');
  return {
    schemaVersion: tokenValue(value, 'schemaVersion'),
    generatedAt: readString(value, 'generatedAt'),
    sources: value.sources.map(normalizeSource),
  };
}

export const getAgentUsage = (_refresh = false): Promise<AgentUsageReport> =>
  invoke<unknown>('get_agent_usage').then(normalizeAgentUsageReport);
