import { describe, expect, it } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import ts from 'typescript';
import { Select } from '../src/components/Select';
import { SelectMenu } from '../src/components/SelectMenu';

const popupRoles = ['listbox', 'combobox'];
const popupOwners = [
  'components/SelectMenu.tsx', 'components/AgentModelPicker.tsx', 'pages/ThinkingAliasesPage.tsx',
  'App.tsx', 'pages/EasyModePage.tsx', 'pages/CodexModelCatalogDialog.tsx', 'pages/DeepSeekHarnessCatalogDialog.tsx',
];
const tagName = (node: ts.JsxOpeningLikeElement) => node.tagName.getText();
const fileViolations = (path: string, text: string) => {
  const name = path.split(sep).join('/').replaceAll('\\', '/');
  if (name.startsWith('mocks/')) return [];
  return violations(text, name)
    .filter(item => !(name === 'components/Select.tsx' && item === 'select'))
    .filter(item => !(popupOwners.includes(name) && item.startsWith('role ')))
    .map(item => `${name}: ${item}`);
};
const violations = (text: string, name: string) => {
  const source = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const collect = (node: ts.Node) => {
    if ((ts.isStringLiteralLike(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node))
      && popupRoles.includes(node.text)) found.push(`role ${node.text}`);
    ts.forEachChild(node, collect);
  };
  const propertyName = (name: ts.PropertyName) => ts.isComputedPropertyName(name)
    ? ts.isStringLiteralLike(name.expression) ? name.expression.text : undefined
    : ts.isIdentifier(name) || ts.isStringLiteralLike(name) ? name.text : undefined;
  const localNames = (component: string) => new Set([component, ...source.statements.flatMap(statement => {
    const bindings = ts.isImportDeclaration(statement) ? statement.importClause?.namedBindings : undefined;
    return bindings && ts.isNamedImports(bindings)
      ? bindings.elements.filter(element => (element.propertyName ?? element.name).text === component).map(element => element.name.text)
      : [];
  })]);
  const dialogs = localNames('Dialog');
  const menus = localNames('SelectMenu');
  const insideDialog = (node: ts.Node) => {
    for (let parent = node.parent; parent; parent = parent.parent) {
      if (ts.isJsxElement(parent) && dialogs.has(tagName(parent.openingElement))
        || ts.isJsxSelfClosingElement(parent) && dialogs.has(tagName(parent))) return true;
    }
    return false;
  };
  const visit = (node: ts.Node) => {
    if (ts.isJsxAttribute(node) && node.name.getText(source) === 'role' && node.initializer) collect(node.initializer);
    if (ts.isPropertyAssignment(node) && propertyName(node.name) === 'role') collect(node.initializer);
    if ((ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) && tagName(node) === 'select') found.push('select');
    if ((ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) && menus.has(tagName(node)) && insideDialog(node)) found.push('SelectMenu in Dialog');
    if (ts.isCallExpression(node) && /(^|\.)createElement$/.test(node.expression.getText(source))
      && node.arguments[0] && ts.isStringLiteralLike(node.arguments[0]) && node.arguments[0].text === 'select') found.push('select');
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
};

describe('shared select controls', () => {
  it('renders a native select with the shared class and forwards attributes and options', () => {
    const html = renderToStaticMarkup(<Select id="mode" className="page-select" value="b" disabled aria-label="Mode" aria-invalid aria-describedby="hint" onChange={() => {}}>
      <option value="a">A</option><option value="b" disabled>B</option>
    </Select>);
    expect(html).toStartWith('<select');
    for (const fragment of ['class="select-control page-select"', 'id="mode"', 'disabled=""', 'aria-label="Mode"', 'aria-invalid="true"', 'aria-describedby="hint"', '<option value="b" disabled="" selected="">B</option>']) expect(html).toContain(fragment);
    expect(renderToStaticMarkup(<Select value="" onChange={() => {}} />)).toContain('class="select-control"');
  });
  it('exposes SelectMenu as a closed combobox without an active descendant', () => {
    const html = renderToStaticMarkup(<SelectMenu value="b" ariaLabel="Mode" onChange={() => {}} options={[{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]} />);
    for (const fragment of ['role="combobox"', 'aria-haspopup="listbox"', 'aria-expanded="false"', 'aria-label="Mode"']) expect(html).toContain(fragment);
    expect(html).not.toContain('aria-activedescendant');
  });
  it('detects native selects, popup roles and SelectMenu inside Dialog', () => {
    for (const source of [
      '<select value={value} />', '<select>{options}</select>', `createElement('select', {})`, `React.createElement('select')`,
    ]) expect(violations(source, 'example.tsx')).toEqual(['select']);
    for (const role of popupRoles) for (const source of [
      `<div role="${role}"/>`, `<div role={condition ? '${role}' : undefined}/>`, `<div {...{role: '${role}'}}/>`, `const props = { ['role']: '${role}' };`,
    ]) expect(violations(source, 'example.tsx')).toEqual([`role ${role}`]);
    for (const source of [
      '<Dialog><div><SelectMenu value="" options={[]} onChange={f} ariaLabel="x" /></div></Dialog>',
      `import { SelectMenu as FilterMenu } from '../components/SelectMenu';\n<Dialog><FilterMenu /></Dialog>`,
      `import { Dialog as Modal } from '../components/Dialog';\n<Modal><SelectMenu /></Modal>`,
      `import { Dialog as Modal } from './Dialog';\nimport { SelectMenu as FilterMenu } from './SelectMenu';\n<Modal><section><FilterMenu /></section></Modal>`,
    ]) expect(violations(source, 'example.tsx')).toEqual(['SelectMenu in Dialog']);
    for (const source of [
      '<Select value={value} />', '<SelectMenu value="" options={[]} onChange={f} ariaLabel="x" />', '<Dialog><Select /></Dialog>',
      `<div role="option"/>`, `<div role="dialog"/>`, `const props = { title: 'listbox' };`, '<input type="search" />',
      `import { Dialog as Modal } from './Dialog';\n<Modal><Select /></Modal>`, `import { SelectMenu as FilterMenu } from './SelectMenu';\n<section><FilterMenu /></section>`,
    ]) expect(violations(source, 'example.tsx')).toEqual([]);
  });
  it('checks self-closing Dialog attributes without treating sibling menus as dialog contents', () => {
    for (const source of [
      '<Dialog actions={<SelectMenu />} />',
      '<Dialog title={<section><SelectMenu /></section>} />',
      `import { Dialog as Modal } from './Dialog';\nimport { SelectMenu as FilterMenu } from './SelectMenu';\n<Modal actions={<FilterMenu />} />`,
      '<Dialog actions={<SelectMenu />}></Dialog>',
    ]) expect(violations(source, 'example.tsx')).toEqual(['SelectMenu in Dialog']);
    for (const source of [
      '<Dialog actions={<Select />} />',
      '<><Dialog /><SelectMenu /></>',
      '<section><SelectMenu /><Dialog /></section>',
      '<Panel actions={<SelectMenu />} />',
      `import { Dialog as Modal } from './Dialog';\nimport { SelectMenu as FilterMenu } from './SelectMenu';\n<><Modal actions={<Select />} /><FilterMenu /></>`,
    ]) expect(violations(source, 'example.tsx')).toEqual([]);
  });
  it('keeps native selects in Select and popup roles in the listed owners', () => {
    const root = join(import.meta.dir, '../src');
    const files = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap(entry => entry.isDirectory() ? files(join(dir, entry.name)) : /\.tsx?$/.test(entry.name) ? [join(dir, entry.name)] : []);
    expect(files(root).flatMap(file => fileViolations(relative(root, file), readFileSync(file, 'utf8')))).toEqual([]);
  });
  it('classifies Windows-style relative paths like POSIX paths', () => {
    expect(fileViolations('components\\Select.tsx', '<select />')).toEqual([]);
    expect(fileViolations('components\\SelectMenu.tsx', '<div role="listbox" />')).toEqual([]);
    expect(fileViolations('mocks\\browserMockRuntime.ts', `createElement('select')`)).toEqual([]);
    expect(fileViolations('pages\\Example.tsx', '<select />')).toEqual(['pages/Example.tsx: select']);
    expect(fileViolations('pages\\Example.tsx', '<div role="combobox" />')).toEqual(['pages/Example.tsx: role combobox']);
  });
});
