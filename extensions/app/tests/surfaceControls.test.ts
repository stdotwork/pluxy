import { describe, expect, it } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import postcss from 'postcss';
import ts from 'typescript';

const root = join(import.meta.dir, '..', 'src');
const migrated: Record<string, string[]> = {
  'plugin-card': ['surface-card'],
  'plugin-settings-card': ['surface-card'],
  'oauth-page-toolbar': ['surface-card'],
  'structured-list-item': ['surface-card'],
  'structured-map-item': ['surface-card'],
  'template-config-custom': ['surface-section'],
  'config-search-result-list': ['surface-card'],
  'config-keys-panel': ['surface-card'],
  'config-management-panel': ['surface-card'],
  'config-network-section': ['surface-card'],
  'config-diagnostics-panel': ['surface-card'],
  'config-software-panel': ['surface-card'],
  'config-sensitive-words-panel': ['surface-bare'],
  'thinking-alias-list-panel': ['surface-bare'],
  'version-list': ['surface-bare'],
  'version-metric-tile': ['surface-section', 'surface-muted'],
};
const ids = new Set(['config-native-aliases', 'config-native-sensitive-words']);
const stateSelectors: Record<string, string[]> = {
  'styles/template-config.css': [
    '.template-config-field-dirty .config-network-input, .template-config-field-dirty .template-config-custom',
  ],
};
const appearance = /^(?:background(?:-|$)|border(?:-|$)|box-shadow$)/;
const walk = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
  const path = join(dir, entry.name);
  return entry.isDirectory() ? walk(path) : [path];
});
const normalize = (text: string) => text.replace(/\s+/g, ' ').trim();

const split = (text: string, comma = false) => {
  const parts: string[] = [];
  let depth = 0;
  let quote = '';
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (char === '\\') { i++; continue; }
    if (quote) { if (char === quote) quote = ''; continue; }
    if (char === '"' || char === "'") { quote = char; continue; }
    if (char === '(' || char === '[') depth++;
    if (char === ')' || char === ']') depth--;
    if (!depth && (comma ? char === ',' : /[\s>+~]/.test(char))) {
      if (text.slice(start, i).trim()) parts.push(text.slice(start, i).trim());
      start = i + 1;
    }
  }
  if (text.slice(start).trim()) parts.push(text.slice(start).trim());
  return parts;
};
const subjects = (selector: string): string[] => split(selector, true).flatMap(part => {
  const subject = split(part).at(-1) ?? '';
  const nested: string[] = [];
  let plain = '';
  for (let i = 0; i < subject.length; i++) {
    const match = subject.slice(i).match(/^:(is|where|not|has)\(/);
    if (!match) { plain += subject[i]; continue; }
    const start = i + match[0].length;
    let end = start;
    let depth = 1;
    for (; end < subject.length && depth; end++) {
      if (subject[end] === '(') depth++;
      if (subject[end] === ')') depth--;
    }
    if (match[1] === 'is' || match[1] === 'where') nested.push(...subjects(subject.slice(start, end - 1)));
    i = end - 1;
  }
  return [plain, ...nested];
});
const tokens = (subject: string) => [...subject.replace(/\[[^\]]*\]/g, '').matchAll(/[.#]([\w-]+)/g)].map(match => match[1]);
const containerSelector = (selector: string) => subjects(selector).some(subject => {
  if (tokens(subject).some(token => token in migrated || ids.has(token) || token.startsWith('surface-'))) return true;
  return /^section(?:$|[.:#[])/.test(subject) && selector.includes('.config-settings-cards');
});
const cssViolations = (text: string, file: string) => {
  const violations: string[] = [];
  postcss.parse(text).walkRules(rule => {
    if (/\.quiet-panel(?![\w-])/.test(rule.selector)) violations.push(`${file} uses retired quiet-panel`);
    if (!containerSelector(rule.selector)) return;
    if (file === 'styles/surfaces.css' && [
      '.panel, .surface-panel', '.panel:hover, .surface-panel:hover',
      ':where(.surface-card), .panel.surface-card', ':where(.surface-section)',
      ':where(.surface-muted)', ':where(.surface-bare), .panel.surface-bare',
    ].includes(normalize(rule.selector))) return;
    if (stateSelectors[file]?.includes(normalize(rule.selector))) return;
    for (const declaration of rule.nodes) {
      if (declaration.type === 'decl' && appearance.test(declaration.prop)) violations.push(`${file}:${rule.source?.start?.line} ${normalize(rule.selector)} ${declaration.prop}`);
    }
  });
  return violations;
};
const jsxViolations = (text: string, file: string) => {
  const source = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const violations: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const attrs = node.attributes.properties.filter(ts.isJsxAttribute);
      const literals = (name: string) => {
        const values: string[] = [];
        const visitValue = (item: ts.Node) => {
          if (ts.isStringLiteralLike(item) || ts.isTemplateHead(item) || ts.isTemplateMiddle(item) || ts.isTemplateTail(item)) values.push(...item.text.split(/\s+/));
          ts.forEachChild(item, visitValue);
        };
        const value = attrs.find(attr => attr.name.getText(source) === name)?.initializer;
        if (value) visitValue(value);
        return values;
      };
      const classes = literals('className');
      for (const hook of classes) for (const required of migrated[hook] ?? []) {
        if (!classes.includes(required)) violations.push(`${file} ${hook} requires ${required}`);
      }
      if (literals('id').some(id => ids.has(id)) && !classes.includes('surface-card')) violations.push(`${file} settings region requires surface-card`);
      if (classes.includes('quiet-panel')) violations.push(`${file} uses retired quiet-panel`);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return violations;
};

const orderedStyles = (file: string): postcss.Root[] => {
  const sheet = postcss.parse(readFileSync(file, 'utf8'));
  const imported: postcss.Root[] = [];
  sheet.walkAtRules('import', rule => {
    const target = rule.params.match(/['"]([^'"]+)['"]/)?.[1];
    if (target) imported.push(...orderedStyles(resolve(dirname(file), target)));
  });
  return [...imported, sheet];
};
const usageStatAppearance = (hover: boolean) => {
  const rules: { priority: number; declarations: Record<string, string> }[] = [];
  for (const sheet of orderedStyles(join(root, 'styles/index.css'))) sheet.walkRules(rule => {
    if (rule.parent?.type !== 'root') return;
    for (const selector of rule.selectors) {
      if (!['.panel', '.usage-stat-card', ...(hover ? ['.panel:hover', '.usage-stat-card:hover'] : [])].includes(selector)) continue;
      const declarations: Record<string, string> = {};
      for (const node of rule.nodes) if (node.type === 'decl') declarations[node.prop] = node.value;
      rules.push({ priority: selector.includes(':hover') ? 2 : 1, declarations });
    }
  });
  return Object.assign({}, ...rules.sort((a, b) => a.priority - b.priority).map(rule => rule.declarations)) as Record<string, string>;
};

describe('shared container surfaces', () => {
  it('keeps migrated container appearance in the shared stylesheet', () => {
    const violations = walk(root).filter(path => path.endsWith('.css')).flatMap(path => cssViolations(readFileSync(path, 'utf8'), relative(root, path).replaceAll('\\', '/')));
    expect(violations).toEqual([]);
  });
  it('requires shared classes at migrated markup and region IDs', () => {
    const violations = walk(root).filter(path => path.endsWith('.tsx')).flatMap(path => jsxViolations(readFileSync(path, 'utf8'), relative(root, path)));
    expect(violations).toEqual([]);
  });
  it('keeps surface definitions before theme overrides without padding or material rules', () => {
    const css = readFileSync(join(root, 'styles/surfaces.css'), 'utf8');
    const index = readFileSync(join(root, 'styles/index.css'), 'utf8');
    const imports: string[] = [];
    postcss.parse(index).walkAtRules('import', rule => { imports.push(rule.params.match(/['"]([^'"]+)['"]/)?.[1] ?? ''); });
    const surfaceIndex = imports.indexOf('./surfaces.css');
    expect(surfaceIndex).toBeGreaterThan(imports.indexOf('./feature-pages.css'));
    expect(surfaceIndex).toBeLessThan(imports.indexOf('./ui-refinements.css'));
    for (const theme of imports.filter(file => /\/theme-[^/]+\.css$/.test(file))) expect(surfaceIndex).toBeLessThan(imports.indexOf(theme));
    expect(css.indexOf('.panel:hover')).toBeLessThan(css.indexOf('.panel.surface-card'));
    for (const name of ['surface-panel', 'surface-card', 'surface-section', 'surface-muted', 'surface-bare']) expect(css).toContain('.' + name);
    expect(css).not.toMatch(/padding|!important|window-material/);
    expect(readFileSync(join(root, 'styles.css'), 'utf8')).not.toContain('.quiet-panel');
  });
  it('keeps usage stat cards flat and preserves their own hover after shared panel defaults', () => {
    const normal = usageStatAppearance(false);
    expect(normal.border).toBe('0');
    expect(normal['border-radius']).toBe('0');
    expect(normal['box-shadow']).toBe('none');
    const hover = usageStatAppearance(true);
    expect(hover.border).toBe('0');
    expect(hover['border-radius']).toBe('0');
    expect(hover['border-color']).toBe('transparent');
    expect(hover['box-shadow']).toContain('0 2px 6px');
  });
  it('rejects appearance mutations including logical properties and pseudo-elements', () => {
    for (const prop of ['border', 'background', 'border-radius', 'box-shadow', 'border-inline-start-color', 'border-start-start-radius', 'background-image']) {
      for (const selector of ['.plugin-card', ':is(.page .plugin-card, .other)', ':where(.plugin-card)', '.plugin-card:not(.selected)', '.plugin-card:has(input:checked)', '.plugin-card::before', '#config-native-aliases', '.config-settings-cards > section', '.page:has(.x) > :is(.plugin-card)']) {
        expect(cssViolations(`${selector} { ${prop}: initial }`, 'pages/Example.css')).toHaveLength(1);
      }
    }
  });
  it('permits layout, child controls and only the precise existing state rules', () => {
    for (const selector of ['.plugin-card', '.plugin-card:not(.selected)', '.plugin-card:has(input)']) expect(cssViolations(`${selector} { padding: 2px; gap: 4px; width: 100% }`, 'pages/Example.css')).toEqual([]);
    for (const selector of ['.page:has(.plugin-card)', '.page:not(.plugin-card)', '.plugin-card > button', '.plugin-card input', '.unmigrated-card']) expect(cssViolations(`${selector} { background: red }`, 'pages/Example.css')).toEqual([]);
    expect(cssViolations('.template-config-field-dirty .config-network-input, .template-config-field-dirty .template-config-custom { border-color: red }', 'styles/template-config.css')).toEqual([]);
    expect(cssViolations('.template-config-field-dirty .config-network-input, .template-config-field-dirty .template-config-custom { border-color: red }', 'pages/Example.css')).toHaveLength(1);
    expect(cssViolations('.surface-card.private-card { background: red }', 'styles/surfaces.css')).toHaveLength(1);
    expect(cssViolations('.quiet-panel { gap: 1px }', 'pages/Example.css')).toHaveLength(1);
    expect(cssViolations('.plugin-card { background: red }', 'styles/surfaces.css')).toHaveLength(1);
    expect(cssViolations('.template-config-field-dirty .template-config-custom:hover { background: red }', 'styles/template-config.css')).toHaveLength(1);
  });
  it('detects missing surface classes in literals, arrays, conditional and template classes', () => {
    for (const jsx of ['<article className="plugin-card" />', '<div className={["structured-map-item"].join(" ")} />', '<section className={`plugin-card ${dirty ? "selected" : ""}`} />', '<div id="config-native-aliases" />']) expect(jsxViolations(jsx, 'Example.tsx')).toHaveLength(1);
    expect(jsxViolations('<article className="surface-card plugin-card" />', 'Example.tsx')).toEqual([]);
    expect(jsxViolations('<div className={["surface-card", "structured-map-item"].join(" ")} />', 'Example.tsx')).toEqual([]);
  });
});
