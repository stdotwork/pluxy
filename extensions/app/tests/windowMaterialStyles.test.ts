import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const read = (path: string) => readFileSync(new URL(`../src/styles/${path}`, import.meta.url), 'utf8');

describe('window material styles', () => {
  test('every see-through rule is scoped to a native window material', () => {
    const selectors = [...read('theme-window-material.css').matchAll(/([^{}]+)\{[^}]*\}/g)]
      .flatMap(([, selector]) => selector.split(/,(?![^(]*\))/).map((part) => part.trim()));
    expect(selectors.length).toBeGreaterThan(0);
    for (const selector of selectors) expect(selector.startsWith(':root[data-window-material]')).toBe(true);
  });

  test('material rules load after the backdrop rules they override', () => {
    const imports = [...read('index.css').matchAll(/@import '\.\/(theme-[a-z-]+\.css)'/g)].map(([, file]) => file);
    expect(imports.indexOf('theme-window-material.css')).toBeGreaterThan(imports.indexOf('theme-backdrop.css'));
  });

  test('main window paints opaque in setup when transparent is enabled', () => {
    const config = JSON.parse(readFileSync(new URL('../src-tauri/tauri.conf.json', import.meta.url), 'utf8'));
    const mainWindow = config.app?.windows?.find((window: { label?: string }) => window.label === 'main');
    if (mainWindow?.transparent) {
      const mainRs = readFileSync(new URL('../src-tauri/src/main.rs', import.meta.url), 'utf8');
      const setupBlock = mainRs.match(/\.setup\(move \|app\| \{([\s\S]*?)\}\)\s*\.invoke_handler/)?.[1] ?? '';
      const uncommented = setupBlock.split('\n').filter(line => !line.trim().startsWith('//')).join('\n');
      expect(uncommented).toContain('window.set_background_color(Some(');
    }
  });
});
