import { describe, expect, test } from 'bun:test';
import {
  combineModelAliasEntries,
  thinkingAliasSourceKindLabel,
} from '../src/pages/ThinkingAliasesPage';

describe('思考别名', () => {
  test('区分同名模型的接入来源', () => {
    expect(thinkingAliasSourceKindLabel('codex-oauth')).toBe('Codex OAuth');
    expect(thinkingAliasSourceKindLabel('codex-api')).toBe('Codex API');
    expect(thinkingAliasSourceKindLabel('openai-compatible')).toBe('OpenAI 兼容');
    expect(thinkingAliasSourceKindLabel('custom')).toBe('其他来源');
  });
});

describe('统一模型别名列表', () => {
  test('同时显示思考别名和 Fast 速度别名', () => {
    const entries = combineModelAliasEntries(
      [{
        sourceModel: 'gpt-thinking',
        alias: 'gpt-high',
        effort: 'high',
        provider: 'Provider B',
        kind: 'openai-compatible',
      }],
      [{
        sourceModel: 'gpt-fast-source',
        alias: 'gpt-fast',
        serviceTier: 'priority',
        provider: 'Provider A',
        kind: 'codex-api',
      }],
    );

    expect(entries.map((entry) => [entry.alias, entry.mode])).toEqual([
      ['gpt-fast', 'fast'],
      ['gpt-high', 'reasoning'],
    ]);
  });
});
