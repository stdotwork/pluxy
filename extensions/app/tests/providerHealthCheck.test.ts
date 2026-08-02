import { describe, expect, it } from 'bun:test';
import {
  aggregateProviderHealthResults,
  buildProviderHealthProbe,
  selectProviderHealthModel,
} from '../src/services/providerHealthCheck';

describe('API 接入健康检测', () => {
  it('优先使用 test-model，其次使用第一个已配置模型', () => {
    const models = [{ name: 'configured-first' }, { name: 'configured-second' }];

    expect(selectProviderHealthModel('health-model', models)).toBe('health-model');
    expect(selectProviderHealthModel('', models)).toBe('configured-first');
    expect(selectProviderHealthModel('', [])).toBe('');
  });

  it('为 OpenAI 兼容接入构造最小 chat completions 请求并保留自定义头', () => {
    const probe = buildProviderHealthProbe(
      'openai',
      'https://openrouter.example/api/v1',
      'gpt-test',
      'secret-key',
      '',
      { 'X-Team': 'production' },
    );

    expect(probe.url).toBe('https://openrouter.example/api/v1/chat/completions');
    expect(probe.header).toMatchObject({
      Authorization: 'Bearer secret-key',
      'Content-Type': 'application/json',
      'X-Team': 'production',
    });
    expect(JSON.parse(probe.data)).toEqual({
      model: 'gpt-test',
      messages: [{ role: 'user', content: 'hi' }],
      max_tokens: 1,
      stream: false,
    });
  });

  it('不会覆盖供应商已有的自定义认证头', () => {
    const probe = buildProviderHealthProbe(
      'claude',
      '',
      'claude-test',
      'new-key',
      '',
      { 'X-Api-Key': 'custom-key', 'Anthropic-Version': 'custom-version' },
    );

    expect(probe.url).toBe('https://api.anthropic.com/v1/messages');
    expect(probe.header['X-Api-Key']).toBe('custom-key');
    expect(probe.header['Anthropic-Version']).toBe('custom-version');
    expect(probe.header['x-api-key']).toBeUndefined();
    expect(probe.header['anthropic-version']).toBeUndefined();
  });

  it('使用运行时凭据占位符构造 Codex responses 请求', () => {
    const probe = buildProviderHealthProbe(
      'codex',
      'https://codex.example/v1/responses',
      'gpt-5-codex',
      '',
      'runtime-auth-index',
    );

    expect(probe.url).toBe('https://codex.example/v1/responses');
    expect(probe.header.Authorization).toBe('Bearer $TOKEN$');
    expect(JSON.parse(probe.data)).toEqual({
      model: 'gpt-5-codex',
      input: 'hi',
      max_output_tokens: 1,
    });
  });

  it('为 Gemini 使用默认地址并移除 models/ 前缀', () => {
    const probe = buildProviderHealthProbe(
      'gemini',
      '',
      'models/gemini-2.5-flash',
      'gemini-key',
    );

    expect(probe.url).toBe(
      'https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent',
    );
    expect(probe.header['x-goog-api-key']).toBe('gemini-key');
  });

  it('多密钥检测部分成功时汇总最低延迟、去重错误和超时状态', () => {
    const result = aggregateProviderHealthResults('test-model', [
      { success: true, latencyMs: 240 },
      { success: false, error: 'invalid key' },
      { success: true, latencyMs: 180 },
      { success: false, error: 'invalid key', timedOut: true },
    ]);

    expect(result).toEqual({
      status: 'partial',
      model: 'test-model',
      successCount: 2,
      totalCount: 4,
      latencyMs: 180,
      errors: ['invalid key'],
      timedOut: true,
    });
  });
});
