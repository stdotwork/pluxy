import { describe, expect, it } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { renderToStaticMarkup } from 'react-dom/server';
import { Switch } from '../src/components/Switch';
import { Checkbox } from '../src/components/Checkbox';

const noop = () => {};

const nativeCheckboxExceptions: Record<string, string> = {};
const switchClassExceptions: Record<string, string> = {
  'thinking-fast-switch': 'Preserve the existing Fast accent while Switch owns the track and size.',
};
const switchRoleExceptions = [{
  file: 'pages/AuthFileManagementPage.tsx',
  tag: 'button',
  classToken: 'auth-card-toggle',
  callback: 'toggleStatus(file)',
  reason: '这是操作按钮，不是开关；保留 HEAD 上的行为、外观与 role 标记。',
}];
const staticAttributeValue = (attribute: ts.JsxAttribute) => {
  const initializer = attribute.initializer;
  const value = initializer && ts.isJsxExpression(initializer) ? initializer.expression : initializer;
  return value && ts.isStringLiteralLike(value) ? value.text : undefined;
};
const binaryControlViolations = (text: string, name: string) => {
  name = name.replaceAll('\\', '/');
  const source = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const violations: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const attributes = node.attributes.properties.filter(ts.isJsxAttribute);
      const tag = node.tagName.getText(source);
      const roleException = switchRoleExceptions.some(exception => exception.file === name && exception.tag === tag
        && exception.reason.trim().length > 0
        && attributes.some(attribute => attribute.name.getText(source) === 'className' && attribute.initializer?.getText(source).includes(exception.classToken))
        && attributes.some(attribute => attribute.name.getText(source) === 'onClick' && attribute.initializer?.getText(source).includes(exception.callback)));
      for (const attribute of attributes) {
        const key = attribute.name.getText(source);
        const value = staticAttributeValue(attribute);
        if (key === 'role' && value === 'switch' && !roleException) violations.push(`${name}: handwritten switch role`);
        if (key === 'className') for (const token of attribute.initializer?.getText(source).match(/\b[\w-]*switch[\w-]*\b/g) ?? []) {
          if (tag !== 'Switch' || !switchClassExceptions[token]) violations.push(`${name}: handwritten switch class ${token}`);
        }
        if (tag === 'input' && key === 'type' && value === 'checkbox' && !nativeCheckboxExceptions[name]) violations.push(`${name}: native checkbox`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return violations;
};


describe('shared binary controls', () => {
  it('associates switch labels and forwards accessibility and form attributes', () => {
    const html = renderToStaticMarkup(<Switch id="setting" label="Setting" checked onChange={noop} disabled invalid aria-label="Override" aria-describedby="hint" name="setting" size="sm" />);
    for (const fragment of ['for="setting"', 'id="setting"', 'role="switch"', 'checked=""', 'disabled=""', 'aria-invalid="true"', 'aria-label="Override"', 'aria-describedby="hint"', 'name="setting"', 'switch-control-sm', 'aria-hidden="true"']) expect(html).toContain(fragment);
  });

  it('keeps the native checkbox immediately before the track', () => {
    const html = renderToStaticMarkup(<Switch checked={false} onChange={noop} />);
    expect(html).toMatch(/<input[^>]+role="switch"[^>]*\/><span class="switch-track" aria-hidden="true"><\/span>/);
    expect(html).not.toContain('checked=');
    expect(html).not.toContain('aria-checked=');
  });

  it('uses native checked state without a second accessibility state', () => {
    for (const Component of [Switch, Checkbox]) {
      expect(renderToStaticMarkup(<Component checked onChange={noop} />)).toContain('checked=""');
      const html = renderToStaticMarkup(<Component checked={false} onChange={noop} />);
      expect(html).not.toContain('checked=');
      expect(html).not.toContain('aria-checked=');
    }
  });

  it('renders checkbox mixed state, explicit labels, errors and native attributes', () => {
    const html = renderToStaticMarkup(<Checkbox id="selection" label="Selection" checked={false} indeterminate onChange={noop} disabled invalid aria-describedby="error" aria-label="Select" />);
    for (const fragment of ['for="selection"', 'id="selection"', 'type="checkbox"', 'aria-describedby="error"', 'aria-label="Select"', 'aria-invalid="true"', 'disabled=""']) expect(html).toContain(fragment);
    expect(html).not.toContain('role="switch"');
    expect(html).not.toContain('aria-checked=');
    expect(html).not.toContain('checked=');
  });

  it('generates matching label ids for both controls', () => {
    for (const Component of [Switch, Checkbox]) {
      const html = renderToStaticMarkup(<Component label="Generated" checked onChange={noop} />);
      const id = html.match(/id="([^"]+)"/)?.[1];
      expect(id).toBeDefined();
      expect(html).toContain(`for="${id}"`);
    }
  });

  it('classifies Windows role exceptions like POSIX paths', () => {
    expect(binaryControlViolations('<button className="auth-card-toggle" role="switch" onClick={() => toggleStatus(file)} />', 'pages\\AuthFileManagementPage.tsx')).toEqual([]);
    expect(binaryControlViolations('<button role="switch" />', 'pages\\Example.tsx')).toEqual(['pages/Example.tsx: handwritten switch role']);
  });

  it('recognizes all static JSX role values', () => {
    for (const role of ['"switch"', '{"switch"}', '{`switch`}']) {
      expect(binaryControlViolations(`<button role=${role} />`, 'pages/Example.tsx')).toEqual(['pages/Example.tsx: handwritten switch role']);
    }
  });

  it('rejects handwritten switch classes, roles and unlisted native checkboxes', () => {
    const directory = join(import.meta.dir, '../src');
    const files = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap(entry => entry.isDirectory() ? files(join(dir, entry.name)) : /\.tsx?$/.test(entry.name) ? [join(dir, entry.name)] : []);
    const violations = files(directory).flatMap(file => {
      const name = relative(directory, file).replaceAll('\\', '/');
      return name.startsWith('mocks/') || ['components/Switch.tsx', 'components/Checkbox.tsx'].includes(name)
        ? [] : binaryControlViolations(readFileSync(file, 'utf8'), name);
    });
    expect(violations).toEqual([]);
  });
});
