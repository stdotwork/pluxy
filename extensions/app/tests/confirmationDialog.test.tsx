import { describe, expect, it } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import type { ReactElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ConfirmationDialog } from '../src/components/ConfirmationDialog';
import { Dialog } from '../src/components/Dialog';
import { QuotaActionFeedback } from '../src/components/QuotaActionFeedback';
import { I18nProvider } from '../src/i18n';

const root = join(import.meta.dir, '..');
const files = (directory: string): string[] => readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
  const path = join(directory, entry.name);
  return entry.isDirectory() ? files(path) : /\.tsx?$/.test(path) ? [path] : [];
});
const walk = (node: ts.Node, visit: (node: ts.Node) => void) => {
  visit(node);
  ts.forEachChild(node, child => walk(child, visit));
};
const render = (element: ReactElement) => renderToStaticMarkup(<I18nProvider>{element}</I18nProvider>);

describe('application confirmation and quota feedback', () => {
  it('renders an accessible in-app confirmation with explicit reset and cancel actions', () => {
    const html = render(<ConfirmationDialog title="Reset Quota" message="Reset the test account quota?" confirmText="Confirm Reset" warning="This consumes one reset credit" onDecision={() => {}} />);
    expect(html).toContain('role="alertdialog"');
    expect(html).toContain('aria-modal="true"');
    expect(html).toContain('aria-describedby=');
    expect(html).toContain('Confirm Reset');
    expect(html).toContain('Cancel');
    expect(render(<ConfirmationDialog title="Unsaved" message="Discard?" cancelText="Keep Editing" onDecision={() => {}} />)).toContain('>Keep Editing</button>');
    expect(html).toContain('This consumes one reset credit');
  });

  it('builds the confirmation on the shared dialog root', () => {
    const html = render(<ConfirmationDialog title="Delete Backup" message="Delete this backup?" variant="danger" onDecision={() => {}} />);
    expect(html).toStartWith('<div class="config-dialog-backdrop app-confirm-backdrop"><section class="app-dialog config-dialog app-confirm-dialog config-dialog-sm" role="alertdialog" aria-modal="true"');
    expect(html).toContain('data-tone="danger"');
    const titleId = html.match(/aria-labelledby="([^"]+)"/)?.[1];
    expect(html).toContain(`<h2 id="${titleId}">Delete Backup</h2>`);
    expect(html).toContain('class="config-dialog-actions two-actions"');
    expect(html).toContain('class="danger-button"');
  });

  it('keeps reset feedback out of the card layout (message checked in browser)', () => {
    const html = render(<QuotaActionFeedback quota={{ status: 'success', rows: [], actionResult: { action: 'reset', status: 'refresh-error', error: 'query failed' } }} />);
    expect(html).toBe('');
  });
});

describe('shared dialog', () => {
  it('defaults to a modal dialog with the A surface and no close control', () => {
    const html = render(<Dialog labelledBy="custom-title"><h2 id="custom-title">Custom</h2></Dialog>);
    expect(html).toBe('<div class="config-dialog-backdrop"><section class="app-dialog config-dialog" role="dialog" aria-modal="true" aria-labelledby="custom-title"><h2 id="custom-title">Custom</h2></section></div>');
  });

  it('renders page surfaces, forms, headings, close controls and actions', () => {
    const html = render(<Dialog as="form" className="install-dialog" backdropClassName="install-dialog-backdrop" size="sm" busy title="Install" onClose={() => {}} closeDisabled actions={<button type="submit">Save</button>} />);
    expect(html).toStartWith('<div class="config-dialog-backdrop install-dialog-backdrop"><form class="app-dialog install-dialog config-dialog-sm" role="dialog" aria-modal="true"');
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('<button type="button" class="icon-button quiet" disabled="" title="Close" aria-label="Close">');
    expect(html).toContain('<div class="config-dialog-actions"><button type="submit">Save</button></div>');
  });

  it('builds every modal through the shared dialog', () => {
    const violations: string[] = [];
    for (const path of files(join(root, 'src'))) {
      const name = relative(join(root, 'src'), path);
      if (name.startsWith('mocks/') || name === 'components/Dialog.tsx') continue;
      walk(ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true), node => {
        if ((ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) && node.tagName.getText() === 'dialog') violations.push(`${name}: <dialog>`);
        if (ts.isPropertyAccessExpression(node) && ['showModal', 'alert'].includes(node.name.text)) violations.push(`${name}: ${node.getText()}`);
        if (ts.isJsxAttribute(node) && node.name.getText() === 'aria-modal') violations.push(`${name}: aria-modal`);
        if (ts.isIdentifier(node) && node.text === 'useDialogFocusTrap' && name !== 'components/useDialogFocusTrap.ts') violations.push(`${name}: useDialogFocusTrap`);
        if ((ts.isStringLiteralLike(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node))
          && node.text.includes('config-dialog-backdrop')) violations.push(`${name}: ${node.text}`);
        if (ts.isStringLiteral(node) && /\b(install-dialog|model-discovery|close-dialog)-backdrop\b/.test(node.text)
          && !(ts.isJsxAttribute(node.parent) && node.parent.name.getText() === 'backdropClassName')) violations.push(`${name}: ${node.text}`);
      });
    }
    expect(violations).toEqual([]);
  });
});
