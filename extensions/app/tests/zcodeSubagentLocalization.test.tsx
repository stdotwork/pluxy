import { describe, expect, it } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider, translate, type AppLocale } from '../src/i18n';
import { mcpSkillsText } from '../src/i18n/mcpSkills';
import type { MessageKey } from '../src/i18n/resources';
import { ZCodeSubagentConfigDialog, thoughtLevelLabel, thoughtLevelOptions } from '../src/pages/ZCodeSubagentConfigDialog';

const han = /\p{Script=Han}/u;
const labels = (locale: AppLocale) => thoughtLevelOptions.map(option => thoughtLevelLabel(option, key => translate(locale, key as MessageKey)));

describe('ZCode sub-agent dialog localization', () => {
  it('keeps the Chinese thinking-effort labels of zh-CN users unchanged', () => {
    // The labels the dialog hard-coded before they moved into the locale files.
    expect(labels('zh-CN')).toEqual(['继承默认 (inherit)', '关闭思考 (disabled)', '低思考 (low)', '中思考 (medium)', '高思考 (high)', '最高思考 (max)']);
    expect(translate('zh-CN', 'agents.zcode.subagents.thought.disabled')).toBe('关闭思考');
    expect(translate('zh-CN', 'agents.zcode.subagents.detected', { count: 3 })).toBe('已检测到的子智能体 (3)');
    expect(translate('zh-CN', 'agents.zcode.subagents.empty')).toBe('未发现匹配的子智能体文件');
  });

  it('shows English and Japanese users translated labels', () => {
    expect(labels('en')).toEqual(['Inherit', 'Thinking off (disabled)', 'Low', 'Medium', 'High', 'Max']);
    expect(labels('ja')).toEqual(['継承 (inherit)', '思考をオフ (disabled)', '低 (low)', '中 (medium)', '高 (high)', '最大 (max)']);
    expect(translate('en', 'agents.zcode.subagents.detected', { count: 3 })).toBe('Detected sub-agents (3)');
    expect(translate('ja', 'agents.zcode.subagents.detected', { count: 3 })).toBe('検出されたサブエージェント（3）');
    const html = renderToStaticMarkup(<I18nProvider><ZCodeSubagentConfigDialog onClose={() => {}} onSaved={() => {}} /></I18nProvider>);
    expect(html).not.toMatch(han);
  });
});

describe('MCP & Skills table and filter labels', () => {
  it('are translated in every language', () => {
    const expected = {
      serverFilter: ['服务器筛选', 'Server filter', 'サーバーのフィルター'],
      skillSourceFilter: ['技能来源', 'Skill source', 'スキルのソース'],
      columnKey: ['键名', 'Key', 'キー'],
      columnValue: ['值', 'Value', '値'],
      columnHeader: ['标头', 'Header', 'ヘッダー'],
      removeRow: ['移除', 'Remove', '削除'],
    } as const;
    for (const [key, [zh, en, ja]] of Object.entries(expected) as Array<[keyof typeof expected, readonly [string, string, string]]>) {
      expect(mcpSkillsText(key, 'zh-CN')).toBe(zh);
      expect(mcpSkillsText(key, 'en')).toBe(en);
      expect(mcpSkillsText(key, 'ja')).toBe(ja);
      expect(mcpSkillsText(key, 'zh-TW')).not.toBe(key);
    }
  });
});
