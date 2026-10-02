import { boolShape, nonemptyText, stringsShape, textShape, type ConfigShape } from './structuredConfig';

export const credentialAdvancedShape: ConfigShape = { type: 'object', fields: {
  cloak_mode: { type: 'select', optional: true, options: ['auto', 'always', 'never'], label: { zh: 'Claude 请求伪装', en: 'Claude request cloaking', ja: 'Claude リクエスト偽装' } },
  cloak_strict_mode: boolShape({ zh: '严格伪装模式', en: 'Strict cloaking', ja: '厳格な偽装' }),
  cloak_cache_user_id: boolShape({ zh: '复用用户标识', en: 'Cache user ID', ja: 'ユーザー ID を再利用' }),
  cloak_sensitive_words: stringsShape({ zh: '伪装时处理的敏感词', en: 'Words to obfuscate when cloaking', ja: '偽装時に難読化する単語' }),
  fingerprint_profile: { type: 'select', optional: true, options: ['', 'claude-code-cli', 'oauth-cli'], label: { zh: '请求指纹', en: 'Request fingerprint', ja: 'リクエストのフィンガープリント' } },
  timezone: { ...textShape({ zh: '凭据时区', en: 'Credential timezone', ja: '認証情報のタイムゾーン' }), validate: value => {
    if (!value) return null;
    try { new Intl.DateTimeFormat('en', { timeZone: value as string }); return null; } catch { return { zh: '请输入 IANA 时区，例如 Asia/Shanghai', en: 'Enter an IANA timezone such as Asia/Shanghai', ja: 'Asia/Shanghai などの IANA タイムゾーンを入力してください' }; }
  } },
  model_aliases: { type: 'array', optional: true, label: { zh: '仅此凭据的模型别名', en: 'Aliases for this credential', ja: 'この認証情報のモデル別名' }, item: { type: 'object', fields: {
    name: { ...nonemptyText, label: { zh: '上游模型', en: 'Upstream model', ja: '上流モデル' } },
    alias: { ...nonemptyText, label: { zh: '客户端别名', en: 'Client alias', ja: 'クライアント別名' } },
    'display-name': textShape({ zh: '展示名称', en: 'Display name', ja: '表示名' }),
    fork: boolShape({ zh: '保留原始模型', en: 'Keep original model', ja: '元のモデルを維持' }),
    'force-mapping': boolShape({ zh: '响应使用别名', en: 'Use alias in responses', ja: '応答で別名を使用' }),
  } } },
} };
