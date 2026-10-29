import { useCallback, useEffect, useState } from 'react';
import { CircleAlert, LoaderCircle, RotateCw } from 'lucide-react';
import { EmptyState } from '../components/EmptyState';
import { useI18n } from '../i18n';
import { Dialog } from '../components/Dialog';
import { Switch } from '../components/Switch';
import { MessageNotice } from '../appNotice';
import { getAgentModels, getCodexSubagentConfig, saveCodexSubagentConfig, type CodexSubagentConfig } from '../services/agents';
import { AgentModelPicker } from '../components/AgentModelPicker';
import type { ModelOption } from '../services/modelService';

const reasoningEffortModels: ModelOption[] = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'].map((name) => ({ name }));

export function CodexSubagentConfigDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void | Promise<void> }) {
  const { t } = useI18n();
  const [config, setConfig] = useState<CodexSubagentConfig | null>(null);
  const [models, setModels] = useState<ModelOption[]>([]);
  const [modelsLoading, setModelsLoading] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [loadError, setLoadError] = useState('');
  const load = useCallback(() => {
    setLoading(true); setModelsLoading(true); setLoadError(''); setError('');
    // The model list is optional; only a config read failure blocks the form.
    void getAgentModels({ client: 'codex' }).then(setModels, () => setModels([])).finally(() => setModelsLoading(false));
    void getCodexSubagentConfig()
      .then(setConfig, (value) => setLoadError(String(value)))
      .finally(() => setLoading(false));
  }, []);
  useEffect(load, [load]);
  const update = <K extends keyof CodexSubagentConfig>(key: K, value: CodexSubagentConfig[K]) => setConfig((current) => current ? { ...current, [key]: value } : current);
  const save = async () => {
    if (!config || saving) return;
    if (config.maxConcurrentThreadsPerSession !== null && (!Number.isInteger(config.maxConcurrentThreadsPerSession) || config.maxConcurrentThreadsPerSession < 1)) {
      setError(t('agents.subagents.invalidConcurrency')); return;
    }
    setSaving(true); setError('');
    try { await saveCodexSubagentConfig(config); await onSaved(); onClose(); }
    catch (value) { setError(String(value)); }
    finally { setSaving(false); }
  };
  return <Dialog className="config-dialog codex-subagent-dialog" labelledBy="codex-subagent-title" onClose={saving ? undefined : onClose} closeDisabled={saving} dismissOnBackdrop>
    <div className="config-dialog-heading"><div><h2 id="codex-subagent-title">{t('agents.subagents.title')}</h2><p>{t('agents.subagents.description')}</p></div></div>
    {loading ? <EmptyState variant="box" role="status" className="codex-subagent-state" icon={<LoaderCircle size={18} className="spin" />}>{t('agents.subagents.loading')}</EmptyState>
      : loadError || !config ? <EmptyState variant="box" tone="error" role="alert" className="codex-subagent-state" icon={<CircleAlert size={18} />}
        title={t('agents.subagents.loadFailed')}
        action={<button type="button" className="secondary-button" onClick={load}><RotateCw size={15} />{t('common.retry')}</button>}>{loadError || null}</EmptyState>
      : <div className="codex-subagent-fields">
      <label className="codex-subagent-toggle"><span><strong>{t('agents.subagents.enabled')}</strong><small>{t('agents.subagents.enabledHint')}</small></span><Switch checked={config.enabled} onChange={(event) => update('enabled', event.currentTarget.checked)} /></label>
      <label><span>{t('agents.subagents.maxConcurrency')}</span><input type="number" min="1" step="1" value={config.maxConcurrentThreadsPerSession ?? ''} placeholder={t('agents.subagents.default')} onChange={(event) => update('maxConcurrentThreadsPerSession', event.currentTarget.value ? Number(event.currentTarget.value) : null)} /></label>
      <label><span>{t('agents.subagents.model')}</span><AgentModelPicker models={models} value={config.defaultSubagentModel ?? ''} loading={modelsLoading} preserveValue emptyOption={t('agents.subagents.inherit')} onChange={(value) => update('defaultSubagentModel', value || null)} /></label>
      <label><span>{t('agents.subagents.reasoning')}</span><AgentModelPicker models={reasoningEffortModels} value={config.defaultSubagentReasoningEffort ?? ''} emptyOption={t('agents.subagents.inherit')} onChange={(value) => update('defaultSubagentReasoningEffort', value || null)} /></label>
      <label className="codex-subagent-toggle"><span><strong>{t('agents.subagents.interrupt')}</strong><small>{t('agents.subagents.interruptHint')}</small></span><Switch checked={config.interruptMessage} onChange={(event) => update('interruptMessage', event.currentTarget.checked)} /></label>
    </div>}
    <MessageNotice tone="error" message={error} />
    <div className="config-dialog-actions"><button type="button" className="secondary-button" onClick={onClose} disabled={saving}>{t('common.cancel')}</button><button type="button" className="primary-button" onClick={() => void save()} disabled={loading || saving || !config}>{saving ? t('common.saving') : t('common.save')}</button></div>
  </Dialog>;
}
