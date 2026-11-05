import { describe, expect, test } from 'bun:test';
import { readdirSync } from 'node:fs';
import { join } from 'node:path';

const LOCALE_DIR = join(import.meta.dir, '..', 'src', 'i18n', 'locales');
const REFERENCE = 'en';

// Plural forms differ by language (en: _one/_other, ru adds _few/_many, zh uses the bare key),
// so plural variants are compared as one key.
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;
const pluralBase = (key: string): string => key.replace(PLURAL_SUFFIX, '');

const flattenLeaves = (value: unknown, prefix = ''): Record<string, string> => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return { [prefix]: String(value ?? '') };
  }
  return Object.entries(value).reduce<Record<string, string>>(
    (leaves, [key, child]) => ({
      ...leaves,
      ...flattenLeaves(child, prefix ? `${prefix}.${key}` : key),
    }),
    {}
  );
};

const interpolationTokens = (value: string): string[] => value.match(/\{\{[^}]+\}\}/g) ?? [];

/** Per key (plural variants merged): the sorted, de-duplicated `{{...}}` tokens of its values. */
const tokensByKey = (leaves: Record<string, string>): Record<string, string[]> => {
  const byKey: Record<string, Set<string>> = {};
  for (const [key, value] of Object.entries(leaves)) {
    const base = pluralBase(key);
    byKey[base] ??= new Set();
    interpolationTokens(value).forEach((token) => byKey[base].add(token));
  }
  return Object.fromEntries(
    Object.entries(byKey).map(([key, tokens]) => [key, [...tokens].sort()])
  );
};

const loadLocale = async (name: string) =>
  flattenLeaves(await Bun.file(join(LOCALE_DIR, `${name}.json`)).json());

const localeNames = readdirSync(LOCALE_DIR)
  .filter((file) => file.endsWith('.json'))
  .map((file) => file.slice(0, -'.json'.length))
  .sort();
const reference = await loadLocale(REFERENCE);
const referenceKeys = new Set(Object.keys(reference).map(pluralBase));
const referenceTokens = tokensByKey(reference);

describe('locale key parity', () => {
  test('finds the reference locale and at least one other', () => {
    expect(localeNames).toContain(REFERENCE);
    expect(localeNames.length).toBeGreaterThan(1);
  });

  describe.each(localeNames.filter((name) => name !== REFERENCE))('%s', (name) => {
    test(`has exactly the keys of ${REFERENCE}`, async () => {
      const keys = new Set(Object.keys(await loadLocale(name)).map(pluralBase));
      expect({
        missing: [...referenceKeys].filter((key) => !keys.has(key)).sort(),
        extra: [...keys].filter((key) => !referenceKeys.has(key)).sort(),
      }).toEqual({ missing: [], extra: [] });
    });

    test(`uses the same {{placeholders}} per key as ${REFERENCE}`, async () => {
      const tokens = tokensByKey(await loadLocale(name));
      const mismatches = Object.keys(referenceTokens)
        .filter((key) => key in tokens)
        .filter((key) => referenceTokens[key].join('|') !== tokens[key].join('|'))
        .map((key) => `${key}: expected [${referenceTokens[key]}] got [${tokens[key]}]`);
      expect(mismatches).toEqual([]);
    });
  });

  test.each(localeNames)('%s defines every plural form its language needs', async (name) => {
    const keys = Object.keys(await loadLocale(name));
    const required = new Intl.PluralRules(name).resolvedOptions().pluralCategories;
    const problems = [...new Set(keys.filter((key) => PLURAL_SUFFIX.test(key)).map(pluralBase))]
      // A bare key is the single form i18next falls back to, so it is complete by itself.
      .filter((base) => !keys.includes(base))
      .flatMap((base) =>
        required
          .filter((category) => !keys.includes(`${base}_${category}`))
          .map((category) => `${base}_${category}`)
      );
    expect(problems).toEqual([]);
  });

  test.each(localeNames)('%s has no empty values', async (name) => {
    const leaves = await loadLocale(name);
    expect(Object.keys(leaves).filter((key) => !leaves[key].trim())).toEqual([]);
  });
});
