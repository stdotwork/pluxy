import { describe, expect, it } from 'bun:test';
import { readFileSync } from 'node:fs';

describe('usage records background refresh', () => {
  it('uses native visibility to pause hidden windows without depending on document visibility', () => {
    const source = readFileSync(
      new URL('../src/pages/UsageRecordsPage.tsx', import.meta.url),
      'utf8',
    );
    const refreshStart = source.indexOf('const refresh = () => {');
    const refreshEnd = source.indexOf('onUsageRecordsUpdated(', refreshStart);
    const refreshBlock = source.slice(refreshStart, refreshEnd);

    expect(refreshStart).toBeGreaterThanOrEqual(0);
    expect(refreshEnd).toBeGreaterThan(refreshStart);
    expect(refreshBlock).toContain('if (!disposed && windowActivity.active())');
    expect(refreshBlock).toContain('void loadData(!initial, false);');
    expect(refreshBlock).not.toContain('document.hidden');
  });

  it('keeps the trend tooltip while the pointer stays over the plot', () => {
    const source = readFileSync(
      new URL('../src/pages/UsageRecordsPage.tsx', import.meta.url),
      'utf8',
    );
    expect(source).toContain('hoveredRatio');
    expect(source).toContain('isClientPointInsideRect');
    expect(source).toContain('addEventListener(\'pointermove\', onWindowPointerMove)');
    expect(source).not.toContain('setHoveredIndex(null)');
  });

  it('allows default WebView background throttling', () => {
    const config = JSON.parse(
      readFileSync(new URL('../src-tauri/tauri.conf.json', import.meta.url), 'utf8'),
    );
    const mainWindow = config.app.windows.find((window: { label?: string }) => window.label === 'main');
    const browserArgs = String(mainWindow?.additionalBrowserArgs ?? '').split(/\s+/);

    expect(mainWindow?.backgroundThrottling).not.toBe('disabled');
    expect(browserArgs).not.toContain('--disable-background-timer-throttling');
    expect(browserArgs).not.toContain('--disable-renderer-backgrounding');
    expect(browserArgs).not.toContain('--disable-backgrounding-occluded-windows');
  });
});
