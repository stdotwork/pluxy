import { describe, expect, test } from 'bun:test';
import { parseCustomThemes } from '../src/themes/customThemes';
import { builtinThemes, createThemeRegistry } from '../src/themes/registry';

const theme = (fields: Record<string, unknown> = {}) => JSON.stringify({
  $schema: './theme.schema.json', id: 'harbor-dark', name: { en: 'Harbor', 'zh-CN': '港湾' }, appearance: 'dark',
  windowBackground: '#0E1A1F', tokens: { workspaceBackground: '#0E1A1F', accent: '#6FD3C1' }, ...fields,
});
const file = (fileName: string, content: string | null) => ({ fileName, content });

describe('custom themes', () => {
  test('parses valid files in order after the built-in themes', () => {
    const { themes, skipped } = parseCustomThemes([
      file('a.json', theme()),
      file('b.json', theme({ id: 'paper-light', name: 'Paper', appearance: 'light', description: 'Plain', $schema: undefined })),
    ]);
    expect(skipped).toEqual([]);
    expect(themes.map(({ id }) => id)).toEqual(['harbor-dark', 'paper-light']);
    const last = Math.max(...builtinThemes.map(({ order }) => order));
    expect(themes.map(({ order }) => order)).toEqual([last + 1, last + 2]);
    expect(themes[0]).not.toHaveProperty('$schema');
    expect(Object.isFrozen(themes[0].tokens)).toBe(true);
    const registry = createThemeRegistry([...builtinThemes, ...themes]);
    expect(registry.themes.slice(-2).map(({ id }) => id)).toEqual(['harbor-dark', 'paper-light']);
  });

  test('accepts a window material request', () => {
    const { themes, skipped } = parseCustomThemes([
      file('glass.json', theme({ id: 'glass-dark', windowMaterial: 'translucent' })),
      file('solid.json', theme({ id: 'solid-dark', windowMaterial: 'solid' })),
    ]);
    expect(skipped).toEqual([]);
    expect(themes.map(({ windowMaterial }) => windowMaterial)).toEqual(['translucent', 'solid']);
  });

  test('skips only the invalid file and records its name', () => {
    const { themes, skipped } = parseCustomThemes([
      file('broken.json', '{'),
      file('unreadable.json', null),
      file('array.json', '[]'),
      file('nameless.json', theme({ name: undefined })),
      file('builtin.json', theme({ id: 'dark' })),
      file('extra.json', theme({ id: 'extra-dark', order: 1 })),
      file('key.json', theme({ id: 'key-dark', nameKey: 'app.theme.dark' })),
      file('material.json', theme({ id: 'glass-dark', windowMaterial: 'glass' })),
      file('token.json', theme({ id: 'token-dark', tokens: { unknownToken: '#ffffff' } })),
      file('token-type.json', theme({ id: 'token-type-dark', tokens: { accent: ['#ffffff'] } })),
      file('background.json', theme({ id: 'background-dark', windowBackground: ['#ffffff'] })),
      file('backdrop.json', theme({ id: 'backdrop-dark', backdrop: { layers: [] } })),
      file('good.json', theme()),
      file('duplicate.json', theme({ name: 'Copy' })),
    ]);
    expect(themes.map(({ id }) => id)).toEqual(['harbor-dark']);
    expect(skipped).toEqual([
      'broken.json', 'unreadable.json', 'array.json', 'nameless.json', 'builtin.json', 'extra.json', 'key.json',
      'material.json', 'token.json', 'token-type.json', 'background.json', 'backdrop.json', 'duplicate.json',
    ]);
  });
});
