import { useState } from 'react';
import { createPortal } from 'react-dom';
import { invoke } from '@tauri-apps/api/core';
import { LoaderCircle } from 'lucide-react';
import { MessageNotice } from '../appNotice';
import { useDialogFocusTrap } from '../components/useDialogFocusTrap';
import { useI18n } from '../i18n';
import type { CodexSessionRepairResult } from '../services/codexSessionState';

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
  const dialogRef = useDialogFocusTrap<HTMLElement>({
    active: target !== null,
    onEscape: saving ? undefined : () => setTarget(null),
    preventEscape: saving,
  });

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
      await invoke('set_codex_openai_provider', { enabled: next });
      switched = true;
      setNotice(t('agents.provider.switched', { provider }));
      if (syncSessions) {
        try {
          const result = await invoke<CodexSessionRepairResult>('repair_codex_session_metadata', { expectedProvider: provider });
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
      <span className="switch-control">
        <input type="checkbox" checked={enabled} disabled={disabled || saving}
          onChange={(event) => { setTarget(event.currentTarget.checked); setSyncSessions(false); setError(''); setNotice(''); }} />
        <span className="switch-track" />
      </span>
    </label>
    <p className="agent-model-hint">{t('agents.provider.hint')}</p>
    <MessageNotice message={notice} tone="info" />
    {target === null ? <MessageNotice message={error} tone="error" /> : null}
    {target !== null ? createPortal(<div className="config-dialog-backdrop">
      <section ref={dialogRef} className="config-dialog agent-restore-dialog" role="dialog" aria-modal="true" aria-labelledby="codex-provider-title" aria-describedby="codex-provider-description">
        <div className="config-dialog-heading"><h2 id="codex-provider-title">{t('agents.provider.title', { provider: target ? 'openai' : 'cpa-gui' })}</h2></div>
        <p id="codex-provider-description">{t('agents.provider.description')}</p>
        <label className="agent-provider-sync-option">
          <input type="checkbox" checked={syncSessions} disabled={saving} onChange={(event) => setSyncSessions(event.currentTarget.checked)} />
          <span>{t('agents.provider.sync')}</span>
        </label>
        <p className="agent-model-hint">{t('agents.provider.syncHint')}</p>
        <MessageNotice message={error} tone="error" />
        <div className="config-dialog-actions two-actions">
          <button type="button" className="secondary-button" disabled={saving} onClick={() => setTarget(null)}>{t('common.cancel')}</button>
          <button type="button" className="primary-button" disabled={saving} onClick={() => void apply()}>
            {saving ? <LoaderCircle size={16} className="spin" /> : null}{t('common.confirm')}
          </button>
        </div>
      </section>
    </div>, document.body) : null}
  </div>;
}
