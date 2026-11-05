import { describe, expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

// The cases live in tests/fixtures/usePluginNavEntries.child.ts and run in their own `bun test`
// process: they mock `react` and the plugins service, and Bun keeps module mocks for the rest
// of the process, which broke every later test file that renders React or uses pluginsApi.
const fixture = './tests/fixtures/usePluginNavEntries.child.ts';
const cwd = fileURLToPath(new URL('..', import.meta.url));

function runCase(name: string) {
  const result = Bun.spawnSync([process.execPath, 'test', fixture, '--test-name-pattern', name], { cwd });
  const output = `${result.stdout.toString()}${result.stderr.toString()}`;
  return { exitCode: result.exitCode, output };
}

describe('usePluginNavEntries', () => {
  for (const name of [
    'target change drops the previous target entries and ignores its stale response',
    'list failure clears entries and the target cache',
    'core not ready hides entries without wiping that target cache',
  ]) {
    test(name, () => {
      const { exitCode, output } = runCase(name);
      // Exactly one case must have run, so a renamed case cannot pass by matching nothing.
      expect(output).toMatch(/^ 1 pass$/m);
      expect(output).toMatch(/^ 0 fail$/m);
      expect(exitCode).toBe(0);
    });
  }
});
