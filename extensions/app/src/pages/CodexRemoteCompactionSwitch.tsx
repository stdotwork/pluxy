import { useState } from 'react';
import { invoke } from '@tauri-apps/api/core';
import { MessageNotice } from '../appNotice';
import { useI18n } from '../i18n';

export function CodexRemoteCompactionSwitch({ enabled, builtinProvider, disabled, onBusyChange, onChanged }: {
  enabled: boolean; builtinProvider: boolean; disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onChanged: (enabled: boolean) => Promise<void>;
}) {
  const { t } = useI18n();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);
  const apply = async (next: boolean) => {
    if (saving || disabled || builtinProvider) return;
    setSaving(true); onBusyChange(true); setError(''); setSaved(false);
    try {
      await invoke('set_codex_remote_compaction', { enabled: next });
      setSaved(true);
      await onChanged(next);
    } catch (cause) { setError(String(cause)); }
    finally { setSaving(false); onBusyChange(false); }
  };
  return <div className="agent-remote-compaction">
    <label className="agent-provider-identity-label">
      <span>{t('agents.remoteCompaction.label')}</span>
      <span className="switch-control">
        <input type="checkbox" checked={enabled || builtinProvider} disabled={disabled || saving || builtinProvider}
          aria-describedby="codex-remote-compaction-hint" onChange={(event) => void apply(event.currentTarget.checked)} />
        <span className="switch-track" />
      </span>
    </label>
    <p id="codex-remote-compaction-hint" className="agent-model-hint">{t(builtinProvider ? 'agents.remoteCompaction.builtinHint' : 'agents.remoteCompaction.hint')}</p>
    <MessageNotice message={error} />
    <MessageNotice message={saved ? t('agents.remoteCompaction.saved') : ''} tone="success" />
  </div>;
}
