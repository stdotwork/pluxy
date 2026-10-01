import { Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useI18n } from '../i18n';
import type { MessageKey } from '../i18n/resources';
import { isRecord, maskSecret, readString } from '../services/managementApi';
import { effectiveProviderKey, providerKeyDraft, providerKeyOverrides, type ProviderKeyDraft } from '../services/providerGroups';
import '../styles/provider-groups.css';

type Props = {
  keys: ProviderKeyDraft[];
  shared: Record<string, unknown>;
  section: string;
  disabled: boolean;
  onChange: (keys: ProviderKeyDraft[]) => void;
};

export function ProviderGroupKeysEditor({ keys, shared, section, disabled, onChange }: Props) {
  const { t } = useI18n();
  const [visible, setVisible] = useState<Record<string, boolean>>({});
  const update = (id: string, patch: (draft: ProviderKeyDraft) => ProviderKeyDraft) =>
    onChange(keys.map((draft) => draft.id === id ? patch(draft) : draft));
  const set = (id: string, field: string, value: unknown) => update(id, (draft) => {
    const next = { ...draft.value };
    if (value === undefined) delete next[field];
    else next[field] = value;
    const text = { ...draft.text };
    delete text[field];
    return { ...draft, value: next, text };
  });
  const text = (id: string, field: string, value: string) => update(id, (draft) => ({ ...draft, text: { ...draft.text, [field]: value } }));
  const fields: { field: string; label: MessageKey; kind: 'number' | 'text' | 'boolean' | 'lines'; initial: unknown }[] = [
    { field: 'priority', label: 'apiAccess.field.priority', kind: 'number', initial: 0 },
    { field: 'proxy-url', label: 'apiAccess.field.proxyUrl', kind: 'text', initial: '' },
    { field: 'prefix', label: 'apiAccess.field.prefix', kind: 'text', initial: '' },
    { field: 'request-retry', label: 'apiAccess.groups.retry', kind: 'number', initial: 0 },
    { field: 'disable-cooling', label: 'apiAccess.cooling.title', kind: 'boolean', initial: false },
    { field: 'headers', label: 'apiAccess.field.headers', kind: 'lines', initial: {} },
    { field: 'excluded-models', label: 'apiAccess.field.excludedModels', kind: 'lines', initial: [] },
  ];
  return <section className="provider-group-keys" aria-label={t('apiAccess.groups.keys')}>
    <div className="provider-group-heading"><div><strong>{t('apiAccess.groups.keys')}</strong><p>{t('apiAccess.groups.keysHint')}</p></div>
      <button type="button" className="secondary-button compact-button" disabled={disabled}
        onClick={() => onChange([...keys, providerKeyDraft({ 'api-key': '' })])}><Plus size={14} />{t('apiAccess.groups.addKey')}</button>
    </div>
    {!keys.length ? <p className="provider-group-hint">{t('apiAccess.groups.emptyKeys')}</p> : null}
    {keys.map((draft, index) => {
      const key = draft.value;
      const overrideCount = providerKeyOverrides(key).length;
      const effective = effectiveProviderKey(shared, key);
      return <fieldset className="provider-group-key" key={draft.id} disabled={disabled}>
        <legend>{t('apiAccess.groups.keyNumber', { number: index + 1 })}</legend>
        <div className="provider-group-key-main">
          <label><span>{t('apiAccess.field.key')}</span><input type={visible[draft.id] ? 'text' : 'password'} autoComplete="off" spellCheck={false}
            aria-label={t('apiAccess.groups.keyNumber', { number: index + 1 })} value={readString(key, 'api-key')}
            onChange={(event) => set(draft.id, 'api-key', event.currentTarget.value)} placeholder="sk-..." /></label>
          <button type="button" className="secondary-button compact-button" aria-pressed={Boolean(visible[draft.id])}
            onClick={() => setVisible((state) => ({ ...state, [draft.id]: !state[draft.id] }))}>{t('apiAccess.groups.showKey')}</button>
          <button type="button" className="icon-button quiet danger" aria-label={t('apiAccess.groups.removeKey', { number: index + 1 })}
            onClick={() => onChange(keys.filter((item) => item.id !== draft.id))}><Trash2 size={15} /></button>
        </div>
        <details className="provider-key-settings">
          <summary>{t('apiAccess.groups.keySettings')} · {overrideCount ? t('apiAccess.groups.overrides', { count: overrideCount }) : t('apiAccess.groups.inherits')}</summary>
          <label><span>{t('apiAccess.groups.weight')}</span><input type="number" min="0" max="1000000" step="1" value={key.weight == null ? '' : String(key.weight)}
            placeholder="1" onChange={(event) => set(draft.id, 'weight', event.currentTarget.value === '' ? undefined : Number(event.currentTarget.value))} /></label>
          <p className="provider-group-hint">{t('apiAccess.groups.overrideHint')}</p>
          {fields.map(({ field, label, kind, initial }) => {
            const overridden = key[field] != null;
            const content = draft.text?.[field] ?? (field === 'headers' && isRecord(key[field])
              ? Object.entries(key[field]).map(([name, value]) => `${name}: ${value}`).join('\n')
              : Array.isArray(key[field]) ? key[field].join('\n') : String(key[field] ?? ''));
            return <div className="provider-key-override" key={field}>
              <label className="provider-key-override-toggle"><input type="checkbox" checked={overridden}
                onChange={(event) => set(draft.id, field, event.currentTarget.checked ? structuredClone(effective[field] ?? initial) : undefined)} />
                <span>{t('apiAccess.groups.overrideField', { field: t(label) })}</span></label>
              {overridden ? kind === 'lines' ? <textarea aria-label={t(label)} rows={3} value={content} onChange={(event) => text(draft.id, field, event.currentTarget.value)} />
                : kind === 'boolean' ? <select aria-label={t(label)} value={String(key[field])} onChange={(event) => set(draft.id, field, event.currentTarget.value === 'true')}>
                  <option value="false">{t('apiAccess.cooling.enable')}</option><option value="true">{t('apiAccess.cooling.disable')}</option></select>
                : <input aria-label={t(label)} type={kind} step={kind === 'number' ? '1' : undefined} value={String(key[field] ?? '')}
                  onChange={(event) => set(draft.id, field, kind === 'number' ? Number(event.currentTarget.value) : event.currentTarget.value)} /> : null}
            </div>;
          })}
          <div className="provider-key-override">
            <label className="provider-key-override-toggle"><input type="checkbox" checked={key.models != null}
              onChange={(event) => set(draft.id, 'models', event.currentTarget.checked ? structuredClone(effective.models ?? []) : undefined)} />
              <span>{t('apiAccess.groups.overrideModels')}</span></label>
            {Array.isArray(key.models) ? <div className="model-config-list">
              {key.models.map((raw, modelIndex) => {
                const model = isRecord(raw) ? raw : { name: String(raw) };
                const change = (patch: Record<string, string>) => set(draft.id, 'models', (key.models as unknown[]).map((item, i) => i === modelIndex ? { ...model, ...patch } : item));
                return <div className="model-config-entry" key={modelIndex}>
                  <input aria-label={t('apiAccess.models.namePlaceholder')} value={readString(model, 'name')} onChange={(event) => change({ name: event.currentTarget.value })} />
                  <input aria-label={t('apiAccess.models.aliasPlaceholder')} value={readString(model, 'alias')} onChange={(event) => change({ alias: event.currentTarget.value })} />
                  <button type="button" className="icon-button quiet danger" aria-label={t('apiAccess.models.remove')} onClick={() => set(draft.id, 'models', (key.models as unknown[]).filter((_, i) => i !== modelIndex))}><Trash2 size={14} /></button>
                </div>;
              })}
              <button type="button" className="secondary-button compact-button" onClick={() => set(draft.id, 'models', [...key.models as unknown[], { name: '', alias: '' }])}><Plus size={14} />{t('apiAccess.models.add')}</button>
            </div> : null}
          </div>
          {section === 'codex-api-key' ? <label><span>WebSocket</span><select value={key.websockets == null ? '' : String(key.websockets)} onChange={(event) => set(draft.id, 'websockets', event.currentTarget.value === '' ? undefined : event.currentTarget.value === 'true')}>
            <option value="">{t('apiAccess.option.inherit')}</option><option value="true">{t('common.enabled')}</option><option value="false">{t('common.disabled')}</option>
          </select></label> : null}
          {section === 'claude-api-key' ? <div className="provider-cloak-settings">
            <label><span>{t('apiAccess.cloak.mode')}</span><select value={isRecord(key.cloak) ? readString(key.cloak, 'mode') : ''} onChange={(event) => {
              const cloak = isRecord(key.cloak) ? { ...key.cloak } : {};
              if (event.currentTarget.value) cloak.mode = event.currentTarget.value; else delete cloak.mode;
              set(draft.id, 'cloak', Object.keys(cloak).length ? cloak : undefined);
            }}><option value="">{t('apiAccess.cloak.default')}</option><option value="auto">{t('apiAccess.cloak.auto')}</option><option value="always">{t('apiAccess.cloak.always')}</option><option value="never">{t('apiAccess.cloak.never')}</option></select></label>
            <label className="multiline-field"><span>{t('apiAccess.cloak.words')}</span>
              <textarea rows={3} value={draft.text?.['cloak-words'] ?? (isRecord(key.cloak) && Array.isArray(key.cloak['sensitive-words']) ? key.cloak['sensitive-words'].join('\n') : '')}
                onChange={(event) => text(draft.id, 'cloak-words', event.currentTarget.value)} /></label>
            {(['strict-mode', 'cache-user-id'] as const).map((field) => <label key={field}>
              <span>{t(field === 'strict-mode' ? 'apiAccess.cloak.strict' : 'apiAccess.cloak.cacheUser')}</span>
              <select value={isRecord(key.cloak) && key.cloak[field] != null ? String(key.cloak[field]) : ''} onChange={(event) => {
                const cloak = isRecord(key.cloak) ? { ...key.cloak } : {};
                if (event.currentTarget.value === '') delete cloak[field]; else cloak[field] = event.currentTarget.value === 'true';
                set(draft.id, 'cloak', Object.keys(cloak).length ? cloak : undefined);
              }}><option value="">{t('apiAccess.option.inherit')}</option><option value="true">{t('common.enabled')}</option><option value="false">{t('common.disabled')}</option></select>
            </label>)}
          </div> : null}
        </details>
        <span className="provider-group-key-state">{maskSecret(readString(key, 'api-key'))}</span>
      </fieldset>;
    })}
  </section>;
}
