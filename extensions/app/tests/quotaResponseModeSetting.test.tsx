import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { TemplateConfigSection } from '../src/components/TemplateConfigSection';
import { CoreRuntimeProvider } from '../src/coreRuntime';
import { I18nProvider } from '../src/i18n';
import { templateText } from '../src/i18n/templateConfig';
import { createBrowserMockRuntime } from '../src/mocks/browserMockRuntime';
import { routingTemplateGroups } from '../src/services/generalTemplateFields';
import { applyTemplateChanges, draftTemplateField, readTemplatePath, templateSwitchChange, templateSwitchChecked } from '../src/services/templateConfig';

const group = routingTemplateGroups.find(item => item.id === 'routing-advanced')!;
const index = group.fields.findIndex(item => item.path.join('.') === 'routing.cooldown.quota-response-mode');
const field = group.fields[index];
const neighbour = group.fields.find(item => item.path.join('.') === 'routing.cooldown.save-cooldown-status')!;
const path = ['routing', 'cooldown', 'quota-response-mode'];
const stored = (value: string) => ({ routing: { cooldown: { 'disable-cooling': false, 'quota-response-mode': value } } });
const toggle = (config: Record<string, unknown>, checked: boolean) => {
  const next = templateSwitchChange(field, checked);
  return draftTemplateField(field, config, undefined, next.value, next.remove);
};

describe('quota response mode toggle', () => {
  test('is an ordinary switch that is off unless usage-limit is stored', () => {
    expect(field).toMatchObject({ type: 'boolean', defaultValue: false, checkedValue: 'usage-limit' });
    expect(templateSwitchChecked(field, false)).toBe(false);
    expect(templateSwitchChecked(field, 'retryable')).toBe(false);
    expect(templateSwitchChecked(field, 'usage-limit')).toBe(true);
    // Plain boolean neighbours keep their true/false behaviour.
    expect(templateSwitchChecked(neighbour, true)).toBe(true);
    expect(templateSwitchChange(neighbour, false)).toEqual({ value: false, remove: false });
  });

  test('on writes usage-limit and off removes the key', () => {
    expect(toggle({}, true)).toEqual({ value: 'usage-limit', remove: false, expectedExists: false, expected: null });
    expect(toggle({}, false)).toBeUndefined();
    const removal = toggle(stored('usage-limit'), false);
    expect(removal).toEqual({ value: null, remove: true, expectedExists: true, expected: 'usage-limit' });
    const saved = applyTemplateChanges(stored('usage-limit'), [{ ...removal!, path }]);
    expect(readTemplatePath(saved, path).exists).toBe(false);
    expect(readTemplatePath(saved, ['routing', 'cooldown', 'disable-cooling']).value).toBe(false);
    expect(toggle(stored('retryable'), false)).toMatchObject({ remove: true, expectedExists: true });
    expect(toggle(stored('usage-limit'), true)).toBeUndefined();
  });

  test('round-trips through the browser mock configuration', async () => {
    const runtime = createBrowserMockRuntime('running');
    const on = toggle(await runtime.invoke('get_extended_core_config') as Record<string, unknown>, true)!;
    const result = await runtime.invoke('save_extended_core_config', { changes: [{ ...on, path }] }) as { config: Record<string, unknown>; restartRequired: boolean };
    expect(result.restartRequired).toBe(false);
    expect(readTemplatePath(result.config, path).value).toBe('usage-limit');
    const off = toggle(result.config, false)!;
    await runtime.invoke('save_extended_core_config', { changes: [{ ...off, path }] });
    expect(readTemplatePath(await runtime.invoke('get_extended_core_config'), path).exists).toBe(false);
  });

  test('renders like the neighbouring toggles', () => {
    const html = renderToStaticMarkup(<I18nProvider><CoreRuntimeProvider><TemplateConfigSection groups={[group]} /></CoreRuntimeProvider></I18nProvider>);
    const id = `template-${group.id}-${index}`;
    expect(html).toContain(`id="template-field-${group.id}-${index}" data-field-type="boolean" tabindex="-1" class="template-config-field template-config-toggle"`);
    expect(html).toContain(`<label for="${id}">Report usage limit on quota exhaustion</label>`);
    const input = (fieldId: string) => html.match(new RegExp(`<input[^>]*id="${fieldId}"[^>]*>`))?.[0];
    const neighbourId = `template-${group.id}-${group.fields.indexOf(neighbour)}`;
    expect(input(id)).toBe(input(neighbourId)!.replace(neighbourId, id));
    expect(input(id)).toContain('type="checkbox" role="switch"');
    expect(input(id)).not.toContain('checked');
  });

  test('has short text in every shipped language', () => {
    for (const locale of ['zh-CN', 'zh-TW', 'ja', 'en'] as const) {
      for (const text of [field.label, field.description]) expect(templateText(text, locale).length).toBeGreaterThan(0);
    }
    expect(templateText(field.description, 'en')).toBe('When every credential is out of quota, tell clients the reset time so they pause and resume instead of retrying.');
  });
});
