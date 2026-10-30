import { describe, expect, it } from 'bun:test';
import { normalizeAgentUsageReport } from '../src/services/agentUsage';

describe('Agent usage report normalization', () => {
  it('keeps the aggregate fields from the harness usage report', () => {
    const report = normalizeAgentUsageReport({
      schemaVersion: 1,
      generatedAt: '2026-10-29T12:00:00Z',
      sources: [{
        source: 'codex',
        status: 'ok',
        daily: [{
          date: '2026-10-29',
          inputTokens: 90,
          outputTokens: 50,
          cacheCreationTokens: 0,
          cacheReadTokens: 10,
          reasoningOutputTokens: 5,
          totalTokens: 155,
          costUSD: 0.0006,
          project: 'private-project',
          models: [{ model: 'gpt-5', inputTokens: 90, outputTokens: 50, totalTokens: 155, costUSD: 0.0006 }],
        }],
      }],
    });

    expect(report.sources[0].daily[0]).toEqual({
      date: '2026-10-29',
      inputTokens: 90,
      outputTokens: 50,
      cacheCreationTokens: 0,
      cacheReadTokens: 10,
      reasoningTokens: 5,
      totalTokens: 155,
      costUSD: 0.0006,
      models: [{
        model: 'gpt-5',
        inputTokens: 90,
        outputTokens: 50,
        cacheCreationTokens: 0,
        cacheReadTokens: 0,
        reasoningTokens: 0,
        totalTokens: 155,
        costUSD: 0.0006,
      }],
    });
    expect(JSON.stringify(report)).not.toContain('private-project');
  });

  it('rejects malformed source and daily rows', () => {
    expect(() => normalizeAgentUsageReport({ sources: [{ source: 'codex', status: 'unknown', daily: [] }] })).toThrow();
    expect(() => normalizeAgentUsageReport({ sources: [{ source: 'codex', status: 'ok', daily: [{ date: 'today' }] }] })).toThrow();
  });
});
