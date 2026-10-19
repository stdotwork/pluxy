import { useState } from 'react';
import { LoaderCircle } from 'lucide-react';
import { MessageNotice } from '../appNotice';
import { Checkbox } from '../components/Checkbox';
import { Dialog } from '../components/Dialog';
import { Switch } from '../components/Switch';
import { useI18n } from '../i18n';
import { setCodexOpenaiProvider } from '../services/agents';
import { repairCodexSessionMetadata } from '../services/codexSessions';

type Props = {
  enabled: boolean;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onChanged: (enabled: boolean) => Promise<void>;
};

export function CodexProviderSwitch({ enabled, disabled, onBusyChange, onChanged }: Props) {
  const { t } = useI18n();
  const [target, setTarget] = useState<boolean | null>(null);
  const [syncSessions, setSyncSessions] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');

  const apply = async () => {
    if (target === null || saving) return;
    const next = target;
    const provider = next ? 'openai' : 'cpa-gui';
    setSaving(true);
    onBusyChange(true);
    setNotice('');
    setError('');
    let switched = false;
    try {
      await setCodexOpenaiProvider({ enabled: next });
      switched = true;
      setNotice(t('agents.provider.switched', { provider }));
      if (syncSessions) {
        try {
          const result = await repairCodexSessionMetadata({ expectedProvider: provider });
          const summary = t('agents.sessions.repairResult', {
            provider: result.targetProvider, files: result.changedRolloutFiles,
            rows: result.sqliteRowsUpdated, skipped: result.skippedLockedFiles.length,
          });
          setNotice([
            t('agents.provider.switched', { provider }), summary,
            result.encryptedContentWarning, ...result.warnings,
            result.backupPath ? t('agents.sessions.backupAt', { path: result.backupPath }) : '',
          ].filter(Boolean).join(' '));
        } catch (cause) {
          setError(t('agents.provider.syncFailed', { error: String(cause) }));
        }
      }
    } catch (cause) {
      setError(String(cause).includes('CPA_CODEX_OAUTH_AUTH_UNSUPPORTED')
        ? t('agents.provider.oauthUnsupported') : String(cause));
    } finally {
      if (switched) {
        setTarget(null);
        try { await onChanged(next); } catch (cause) {
          setError((previous) => [previous, t('agents.provider.refreshFailed', { error: String(cause) })].filter(Boolean).join(' '));
        }
      }
      setSaving(false);
      onBusyChange(false);
    }
  };

  return <div className="agent-provider-identity">
    <label className="agent-provider-identity-label">
      <span>{t('agents.provider.label')}</span>
      <Switch checked={enabled} disabled={disabled || saving}
        onChange={(event) => { setTarget(event.currentTarget.checked); setSyncSessions(false); setError(''); setNotice(''); }} />
    </label>
    <p className="agent-model-hint">{t('agents.provider.hint')}</p>
    <MessageNotice message={notice} tone="info" />
    {target === null ? <MessageNotice message={error} tone="error" /> : null}
    {target !== null ? <Dialog className="config-dialog agent-restore-dialog" labelledBy="codex-provider-title" describedBy="codex-provider-description" onClose={() => setTarget(null)} closeDisabled={saving}>
      <div className="config-dialog-heading"><h2 id="codex-provider-title">{t('agents.provider.title', { provider: target ? 'openai' : 'cpa-gui' })}</h2></div>
      <p id="codex-provider-description">{t('agents.provider.description')}</p>
      <Checkbox checked={syncSessions} disabled={saving} onChange={(event) => setSyncSessions(event.currentTarget.checked)} label={t('agents.provider.sync')} />
      <p className="agent-model-hint">{t('agents.provider.syncHint')}</p>
      <MessageNotice message={error} tone="error" />
      <div className="config-dialog-actions two-actions">
        <button type="button" className="secondary-button" disabled={saving} onClick={() => setTarget(null)}>{t('common.cancel')}</button>
        <button type="button" className="primary-button" disabled={saving} onClick={() => void apply()}>
          {saving ? <LoaderCircle size={16} className="spin" /> : null}{t('common.confirm')}
        </button>
      </div>
    </Dialog> : null}
  </div>;
}
