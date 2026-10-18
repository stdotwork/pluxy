import { expect, test } from 'bun:test';
import { createBrowserMockRuntime } from '../src/mocks/browserMockRuntime';

for (const [read, save, field, initial, value] of [
  ['get_usage_view_preferences', 'save_usage_view_preferences', 'values', {}, { 'cpa-gui.usage-tab': 'events' }],
  ['get_privacy_mode', 'save_privacy_mode', 'enabled', null, true],
  ['get_theme_selection', 'save_theme_selection', 'selection', null, { version: 1, selectedId: 'dark', lastLightId: 'light', lastDarkId: 'dark' }],
] as const) {
  test(`browser mock persists ${field} within one preview`, async () => {
    const runtime = createBrowserMockRuntime('running');
    expect(await runtime.invoke(read)).toEqual(initial);
    await runtime.invoke(save, { [field]: value });
    const saved = await runtime.invoke(read);
    expect(saved).toEqual(value);
    if (saved && typeof saved === 'object') Object.assign(saved, { modified: 'outside' });
    expect(await runtime.invoke(read)).toEqual(value);
    expect(await createBrowserMockRuntime('running').invoke(read)).toEqual(initial);
  });
}

test('browser mock rejects invalid preference writes', async () => {
  const runtime = createBrowserMockRuntime('running');
  for (const [command, payload] of [
    ['save_usage_view_preferences', { values: { invalid: 'value' } }],
    ['save_privacy_mode', { enabled: 'yes' }],
    ['save_theme_selection', { selection: { preference: 4 } }],
  ] as const) await expect(runtime.invoke(command, payload)).rejects.toThrow();
});
