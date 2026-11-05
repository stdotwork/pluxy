import { expect, test } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';

const root = join(import.meta.dir, '..');
const files = (directory: string): string[] => readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
  const path = join(directory, entry.name);
  return entry.isDirectory() ? files(path) : /\.tsx?$/.test(path) ? [path] : [];
});
const source = (path: string) => ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true);
const walk = (node: ts.Node, visit: (node: ts.Node) => void) => {
  visit(node);
  ts.forEachChild(node, child => walk(child, visit));
};

const isPlatformOwner = (name: string) => {
  name = name.replaceAll('\\', '/');
  return name.startsWith('services/') || name.startsWith('mocks/');
};

test('classifies Windows platform owner paths like POSIX paths', () => {
  expect(isPlatformOwner('services\\platform.ts')).toBe(true);
  expect(isPlatformOwner('mocks\\browserMockRuntime.ts')).toBe(true);
  expect(isPlatformOwner('pages\\HomePage.tsx')).toBe(false);
});

test('Tauri APIs stay inside services and browser mocks', () => {
  const violations: string[] = [];
  for (const path of files(join(root, 'src'))) {
    const name = relative(join(root, 'src'), path).replaceAll('\\', '/');
    if (isPlatformOwner(name)) continue;
    walk(source(path), node => {
      if (ts.isStringLiteral(node) && node.text.startsWith('@tauri-apps/')) violations.push(name);
    });
  }
  expect([...new Set(violations)]).toEqual([]);
});

// Modules whose export reaches Tauri's `invoke`; the command stays a literal at each call site.
const AGENT_WRAPPER = 'invokeAgentCommand';
const INVOKERS: Record<string, string> = {
  '@tauri-apps/api/core': 'invoke',
  './platform': 'invoke',
  './agents': AGENT_WRAPPER,
};

test('service IPC commands are registered and have browser mock contracts', () => {
  const commands = new Set<string>();
  const dynamic: string[] = [];
  for (const path of files(join(root, 'src/services'))) {
    const module = source(path);
    const invokers = new Set<string>();
    const namespaces = new Set<string>();
    for (const node of module.statements) {
      if (!ts.isImportDeclaration(node) || !ts.isStringLiteral(node.moduleSpecifier)) continue;
      const exported = INVOKERS[node.moduleSpecifier.text];
      if (!exported) continue;
      const bindings = node.importClause?.namedBindings;
      if (bindings && ts.isNamespaceImport(bindings) && exported === 'invoke'
        && node.moduleSpecifier.text === '@tauri-apps/api/core') namespaces.add(bindings.name.text);
      if (bindings && ts.isNamedImports(bindings)) for (const binding of bindings.elements) {
        if ((binding.propertyName ?? binding.name).text === exported) invokers.add(binding.name.text);
      }
    }
    // `agents.ts` defines the wrapper itself and aliases it as `invoke`.
    const isAgents = relative(join(root, 'src/services'), path) === 'agents.ts';
    if (isAgents) invokers.add(AGENT_WRAPPER);
    for (const node of module.statements) {
      if (!isAgents || !ts.isVariableStatement(node)) continue;
      for (const declaration of node.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name) && declaration.initializer && ts.isIdentifier(declaration.initializer)
          && invokers.has(declaration.initializer.text)) invokers.add(declaration.name.text);
      }
    }
    // The only call allowed to forward a non-literal command is the wrapper's own call to Tauri.
    const forwardsWrapperCommand = (call: ts.CallExpression) => {
      const command = call.arguments[0];
      if (!isAgents || !command || !ts.isIdentifier(command)) return false;
      for (let parent: ts.Node | undefined = call.parent; parent; parent = parent.parent) {
        if (!ts.isVariableDeclaration(parent) || parent.name.getText() !== AGENT_WRAPPER) continue;
        const wrapper = parent.initializer;
        return !!wrapper && ts.isArrowFunction(wrapper) && wrapper.parameters[0]?.name.getText() === command.text;
      }
      return false;
    };
    walk(module, node => {
      if (!ts.isCallExpression(node)) return;
      const expression = node.expression;
      const invokes = ts.isIdentifier(expression) && invokers.has(expression.text)
        || ts.isPropertyAccessExpression(expression) && ts.isIdentifier(expression.expression)
          && namespaces.has(expression.expression.text) && expression.name.text === 'invoke';
      if (!invokes) return;
      const command = node.arguments[0];
      if (command && ts.isStringLiteral(command)) commands.add(command.text);
      else if (!forwardsWrapperCommand(node)) dynamic.push(relative(root, path));
    });
  }
  const backend = readFileSync(join(root, 'src-tauri/src/main.rs'), 'utf8');
  const registered = new Set(backend.match(/generate_handler!\[([\s\S]*?)\]/)?.[1].match(/\b\w+\b/g));
  const mocked = new Set<string>();
  for (const path of files(join(root, 'src/mocks'))) {
    walk(source(path), node => {
      if (ts.isVariableDeclaration(node) && node.name.getText() === 'WEBVIEW_COMMANDS'
        && node.initializer && ts.isNewExpression(node.initializer)) {
        for (const argument of node.initializer.arguments ?? []) walk(argument, value => {
          if (ts.isStringLiteral(value)) mocked.add(value.text);
        });
      }
      if (ts.isCaseClause(node) && ts.isStringLiteral(node.expression)) mocked.add(node.expression.text);
      if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken
        && node.left.getText() === 'command' && ts.isStringLiteral(node.right)) mocked.add(node.right.text);
    });
  }
  expect(dynamic).toEqual([]);
  expect([...commands].filter(command => !registered.has(command))).toEqual([]);
  expect([...commands].filter(command => !mocked.has(command))).toEqual([]);
});
