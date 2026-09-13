import { describe, expect, it } from 'bun:test';
import {
  mergeModelOptions,
  reconcileModelSelection,
} from '../src/services/modelService';

describe('model discovery selection', () => {
  const discovered = [
    { name: 'deepseek-chat' },
    { name: 'deepseek-reasoner' },
    { name: 'deepseek-new-model' },
  ];

  it('selects every discovered model on the first fetch for a new connection', () => {
    expect(Array.from(reconcileModelSelection(discovered, [], [], 'initial'))).toEqual([
      'deepseek-chat',
      'deepseek-reasoner',
      'deepseek-new-model',
    ]);
  });

  it('keeps saved selections and leaves newly discovered models unchecked', () => {
    expect(Array.from(reconcileModelSelection(
      discovered,
      [{ name: 'deepseek-chat' }],
      ['deepseek-chat'],
      'initial',
    ))).toEqual(['deepseek-chat']);
  });

  it('preserves refresh selections, drops unavailable discoveries, and keeps configured models', () => {
    expect(Array.from(reconcileModelSelection(
      [{ name: 'deepseek-reasoner' }, { name: 'deepseek-new-model' }],
      [{ name: 'custom-model' }],
      ['DEEPSEEK-REASONER', 'removed-model', 'custom-model'],
      'refresh',
    ))).toEqual(['deepseek-reasoner', 'custom-model']);
  });

  it('deduplicates model names case-insensitively and keeps configured metadata', () => {
    expect(mergeModelOptions(
      [{ name: 'deepseek-chat' }],
      [{ name: ' DEEPSEEK-CHAT ', alias: 'Chat' }],
    )).toEqual([{ name: 'DEEPSEEK-CHAT', alias: 'Chat' }]);
  });
});
