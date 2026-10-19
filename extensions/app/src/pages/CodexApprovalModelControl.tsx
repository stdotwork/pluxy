import { useCallback, useEffect, useState } from 'react';
import { invoke } from '@tauri-apps/api/core';
import { MessageNotice } from '../appNotice';
import { AgentModelPicker } from '../components/AgentModelPicker';
import { useI18n } from '../i18n';
import type { CodexCatalogEditorSnapshot, CodexCatalogEditorSaveResult } from '../services/codexModelCatalog';

export function CodexApprovalModelControl({ disabled, onBusyChange }: {
  disabled: boolean; onBusyChange: (busy: boolean) => void;
}) {
  const { t } = useI18n();
  const [snapshot, setSnapshot] = useState<CodexCatalogEditorSnapshot | null>(null);
  const [value, setValue] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const load = useCallback(async () => {
    setLoading(true); setError(''); setNotice('');
    try {
      const next = await invoke<CodexCatalogEditorSnapshot>('get_codex_model_catalog_editor');
      setSnapshot(next); setValue(next.defaultAutoReviewModel ?? '');
    } catch (cause) { setError(String(cause)); }
    finally { setLoading(false); }
  }, []);
  useEffect(() => { void load(); }, [load]);
  const save = async () => {
    if (!snapshot || disabled || saving || loading) return;
    setSaving(true); onBusyChange(true); setError(''); setNotice('');
    try {
      const result = await invoke<CodexCatalogEditorSaveResult>('save_codex_model_catalog_editor', {
        request: {
          revision: snapshot.revision,
          models: snapshot.models.map(({ slug, configuration }) => ({ slug, configuration })),
          review_model: { model: value || null },
        },
      });
      setSnapshot(result.snapshot);
      setValue(result.snapshot.defaultAutoReviewModel ?? '');
      if (result.synchronizationError) setError(result.synchronizationError);
      else setNotice(t('agents.catalog.saved'));
    } catch (cause) {
      setError(String(cause).includes('CODEX_MODEL_CATALOG_CHANGED') ? t('agents.catalog.changed') : String(cause));
    } finally { setSaving(false); onBusyChange(false); }
  };
  const models = snapshot?.models.map(({ slug }) => ({ name: slug })) ?? [];
  return <div className="agent-approval-model">
    <h3>{t('agents.approvalModel.label')}</h3>
    <p>{t('agents.approvalModel.hint')}</p>
    <div className="agent-approval-model-actions">
      <AgentModelPicker models={models} value={value} loading={loading}
        disabled={disabled || saving || loading || !snapshot} preserveValue
        emptyOption={t('agents.approvalModel.default')}
        onChange={(next) => { setValue(next); setNotice(''); }}
        onRefresh={() => void load()} />
      <button type="button" className="secondary-button" onClick={() => void save()}
        disabled={disabled || saving || loading || !snapshot || value === (snapshot.defaultAutoReviewModel ?? '')}>
        {t('agents.approvalModel.save')}
      </button>
    </div>
    {snapshot && value && !models.some((model) => model.name === value)
      ? <p className="agent-model-hint">{t('agents.approvalModel.missing')}</p> : null}
    {!snapshot && !loading ? <button type="button" className="quiet-button" disabled={disabled || saving}
      onClick={() => void load()}>{t('common.refresh')}</button> : null}
    <MessageNotice message={error} />
    <MessageNotice message={notice} tone="success" />
  </div>;
}
