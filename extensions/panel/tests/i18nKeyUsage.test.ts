import { describe, expect, test } from 'bun:test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';

const SRC_DIR = join(import.meta.dir, '..', 'src');
const EN_PATH = join(SRC_DIR, 'i18n', 'locales', 'en.json');

// Plural variants (key_one, key_other, ...) satisfy a call to the bare key.
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;

const flattenKeys = (value: unknown, prefix = ''): string[] => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [prefix];
  return Object.entries(value).flatMap(([key, child]) =>
    flattenKeys(child, prefix ? `${prefix}.${key}` : key)
  );
};

const sourceFiles = (dir: string): string[] =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx)$/.test(entry.name) ? [path] : [];
  });

/** The text of a call's arguments, from just after `(` to the matching `)`. */
const callArguments = (source: string, start: number): string => {
  let depth = 1;
  for (let i = start; i < source.length; i += 1) {
    if (source[i] === '(') depth += 1;
    if (source[i] === ')') depth -= 1;
    if (depth === 0) return source.slice(start, i);
  }
  return source.slice(start);
};

// `t('some.key'` or `t("some.key"` as a call, not as the tail of another name (`format(`).
const LITERAL_T_CALL = /(?<![\w$])t\(\s*(['"])([\w.-]+)\1/g;

const literalKeysWithoutDefault = (source: string): string[] => {
  const keys: string[] = [];
  for (const match of source.matchAll(LITERAL_T_CALL)) {
    const args = callArguments(source, match.index + match[0].indexOf('(') + 1);
    if (!args.includes('defaultValue')) keys.push(match[2]);
  }
  return keys;
};

describe('literal t() keys exist in en.json', () => {
  test('the scan finds t() and i18n.t() calls, skips defaultValue calls and other functions', () => {
    const sample = [
      "t('a.one')",
      't("a.two", { count: 2 })',
      "t('a.three', { defaultValue: 'x' })",
      "t(\n  'a.four',\n  { defaultValue: 'x' }\n)",
      "format('a.five')",
      "i18n.t('a.six')",
    ].join('\n');
    expect(literalKeysWithoutDefault(sample)).toEqual(['a.one', 'a.two', 'a.six']);
  });

  test('every literal key used without a defaultValue is defined', () => {
    const defined = new Set(
      flattenKeys(JSON.parse(readFileSync(EN_PATH, 'utf8'))).flatMap((key) => [
        key,
        key.replace(PLURAL_SUFFIX, ''),
      ])
    );
    const missing = sourceFiles(SRC_DIR).flatMap((file) =>
      literalKeysWithoutDefault(readFileSync(file, 'utf8'))
        .filter((key) => !defined.has(key))
        .map((key) => `${relative(SRC_DIR, file)}: ${key}`)
    );
    expect(missing.sort()).toEqual([]);
  });
});
