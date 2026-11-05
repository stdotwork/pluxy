import { describe, expect, it } from 'bun:test';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import { renderToStaticMarkup } from 'react-dom/server';
import { CopilotPluginStatus } from '../src/components/copilot/CopilotPluginStatus';
import { I18nProvider, translate, type AppLocale } from '../src/i18n';
import type { MessageKey } from '../src/i18n/resources';
import { ZCodeModelCatalogDialog } from '../src/pages/ZCodeModelCatalogDialog';

const dialogPath = new URL('../src/pages/ZCodeModelCatalogDialog.tsx', import.meta.url);
const key = (name: string) => `agents.zcode.catalog.${name}` as MessageKey;

// The Chinese text the dialog showed before its labels moved into the locale files.
const chineseBefore: Record<string, string> = {
  vision: '视觉',
  enabledInZcode: '在 ZCode 中启用',
  hiddenInZcode: '在 ZCode 中隐藏',
  managed: '已由 CPA 代管下发规格（ZCode 软件端显示为手动自定义配置，关闭其内部默认配置）',
  contextWindow: '上下文窗口 (Tokens)',
  contextWindowPlaceholder: '例如 1048576',
  maxOutput: '最大输出 Token',
  maxOutputPlaceholder: '留空使用默认 (例如 128000)',
  reasoningLevels: '思考等级 (reasoningLevel)',
  inputTypes: '输入模态支持',
  text: '文本 (必需)',
  image: '图像 (Vision)',
  pdf: 'PDF 文档',
  video: '视频',
  advanced: '高级特性支持',
  jsonSchema: '结构化输出 (JSON Schema)',
  webSearch: '原生联网搜索',
  midSystem: '对话中系统消息',
  restore: '恢复推荐配置',
};

const usedKeys = (): string[] => {
  const source = ts.createSourceFile('dialog.tsx', readFileSync(dialogPath, 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const keys = new Set<string>();
  const visit = (node: ts.Node) => {
    if (ts.isStringLiteralLike(node) && node.text.startsWith('agents.zcode.catalog.')) keys.add(node.text);
    ts.forEachChild(node, visit);
  };
  visit(source);
  return [...keys];
};

const render = (element: React.ReactElement) => renderToStaticMarkup(<I18nProvider>{element}</I18nProvider>);

describe('ZCode model catalog dialog localization', () => {
  it('uses a locale key for every label the dialog used to hard-code in Chinese', () => {
    const used = usedKeys();
    for (const name of Object.keys(chineseBefore)) expect(used).toContain(key(name));
  });

  it('keeps the Chinese text of zh-CN users unchanged', () => {
    for (const [name, text] of Object.entries(chineseBefore)) expect(translate('zh-CN', key(name))).toBe(text);
  });

  it('shows English and Japanese users translated labels, with no Chinese-only text', () => {
    for (const used of usedKeys()) {
      const english = translate('en', used as MessageKey);
      const japanese = translate('ja', used as MessageKey);
      expect(english).not.toBe('');
      expect(english).not.toMatch(/\p{Script=Han}/u);
      expect(japanese).not.toBe('');
      expect(japanese).not.toBe(translate('zh-CN', used as MessageKey));
    }
  });

  it('converts the new labels to Traditional Chinese without leaving Simplified characters', () => {
    expect(translate('zh-TW', key('vision'))).toBe('視覺');
    expect(translate('zh-TW', key('video'))).toBe('影片');
    expect(translate('zh-TW', key('maxOutputPlaceholder'))).toBe('留空使用預設 (例如 128000)');
    expect(translate('zh-TW', key('managed'))).toStartWith('已由 CPA 代管下發規格');
    for (const name of Object.keys(chineseBefore)) {
      expect(translate('zh-TW', key(name))).not.toMatch(/[视频档级输换设态动应项选译]/);
    }
  });

  it('renders English labels in the English locale', () => {
    const html = render(<ZCodeModelCatalogDialog onClose={() => {}} onSaved={() => {}} />);
    expect(html).not.toMatch(/\p{Script=Han}/u);
    expect(html).toContain('ZCode Model List &amp; Rules');
    expect(html).toContain('Search model names or IDs');
    expect(html).toContain('Restore recommended settings');
  });

  it('translates the Copilot plugin version label', () => {
    const html = render(<CopilotPluginStatus installed version="1.2.3" />);
    expect(html).toContain('GitHub Copilot Chat · Plugin Version');
    expect(html).not.toMatch(/\p{Script=Han}/u);
    const labels: Record<AppLocale, string> = {
      en: 'GitHub Copilot Chat · Plugin Version',
      'zh-CN': 'GitHub Copilot Chat · 插件版本',
      'zh-TW': 'GitHub Copilot Chat · 插件版本',
      ja: 'GitHub Copilot Chat · プラグインバージョン',
    };
    for (const [locale, label] of Object.entries(labels)) expect(translate(locale as AppLocale, 'agents.copilot.chatPluginVersion')).toBe(label);
  });
});
