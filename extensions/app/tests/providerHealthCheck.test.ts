import { describe, expect, it } from 'bun:test';
import {
  buildProviderHealthProbe,
  mergeProviderHealthModels,
  primaryProviderHealthCredential,
  runProviderModelHealthChecks,
} from '../src/services/providerHealthCheck';

describe('API 接入健康检测', () => {
  it('合并发现模型、已配置模型和 test-model，并按名称去重', () => {
    const models = mergeProviderHealthModels(
      [{ name: 'model-b' }, { name: 'model-a' }],
      [{ name: 'MODEL-A', alias: 'model-a-alias' }, { name: 'model-c' }],
      'health-model',
    );

    expect(models).toEqual([
      { name: 'health-model' },
      { name: 'MODEL-A', alias: 'model-a-alias' },
      { name: 'model-b' },
      { name: 'model-c' },
    ]);
  });

  it('逐模型检测只使用当前接入的首个有效密钥', () => {
    expect(primaryProviderHealthCredential(['', ' first-key ', 'second-key'])).toBe('first-key');
    expect(primaryProviderHealthCredential([])).toBe('');
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
      stream: true,
    });
    expect(probe.protocol).toBe('openai-chat');
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
      stream: true,
    });
    expect(probe.protocol).toBe('openai-responses');
  });

  it('为 Gemini 使用默认地址并移除 models/ 前缀', () => {
    const probe = buildProviderHealthProbe(
      'gemini',
      '',
      'models/gemini-2.5-flash',
      'gemini-key',
    );

    expect(probe.url).toBe(
      'https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?alt=sse',
    );
    expect(probe.header['x-goog-api-key']).toBe('gemini-key');
    expect(probe.protocol).toBe('gemini');
  });

  it('检测全部时每个模型只请求一次并保持结果顺序', async () => {
    const requested: string[] = [];
    const results = await runProviderModelHealthChecks([
      { name: 'model-a' },
      { name: 'model-b' },
      { name: 'model-c' },
    ], async (model) => {
      requested.push(model.name);
      return {
        model: model.name,
        status: 'healthy',
        success: true,
        firstTokenLatencyMs: 120,
      };
    }, undefined, 2);

    expect(requested.sort()).toEqual(['model-a', 'model-b', 'model-c']);
    expect(results.map((result) => result.model)).toEqual(['model-a', 'model-b', 'model-c']);
  });

});
