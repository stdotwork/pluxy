import { builtinThemes, normalizeTheme } from './registry';
import type { ThemeDefinition } from './types';

export type CustomThemeFile = { fileName: string; content: string | null };
export type CustomThemeList = { files: CustomThemeFile[]; omitted: number };
export type CustomThemes = { themes: ThemeDefinition[]; skipped: string[] };

const allowedKeys = new Set(['$schema', 'id', 'name', 'description', 'appearance', 'windowBackground', 'windowMaterial', 'tokens', 'backdrop']);
const isRecord = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value);

function parseCustomTheme(content: string, order: number): ThemeDefinition {
  const record: unknown = JSON.parse(content);
  if (!isRecord(record) || Object.keys(record).some((key) => !allowedKeys.has(key))
    || record.name === undefined || typeof record.windowBackground !== 'string'
    || (record.$schema !== undefined && typeof record.$schema !== 'string')
    || !isRecord(record.tokens)) {
    throw new Error('Invalid custom theme');
  }
  const { $schema: _schema, ...definition } = record;
  return normalizeTheme({ ...definition, order } as ThemeDefinition);
}

export function parseCustomThemes(files: readonly CustomThemeFile[], builtins: readonly ThemeDefinition[] = builtinThemes): CustomThemes {
  const ids = new Set(builtins.map((theme) => theme.id));
  const first = Math.max(...builtins.map((theme) => theme.order)) + 1;
  const themes: ThemeDefinition[] = [];
  const skipped: string[] = [];
  for (const file of files) {
    try {
      if (file.content === null) throw new Error('Unreadable custom theme');
      const theme = parseCustomTheme(file.content, first + themes.length);
      if (ids.has(theme.id)) throw new Error('Duplicate custom theme');
      ids.add(theme.id);
      themes.push(theme);
    } catch {
      skipped.push(file.fileName);
    }
  }
  return { themes, skipped };
}
