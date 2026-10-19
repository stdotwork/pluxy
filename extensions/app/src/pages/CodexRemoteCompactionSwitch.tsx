import { useState } from 'react';
import { setCodexRemoteCompaction } from '../services/agents';
import { Switch } from '../components/Switch';
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
      await setCodexRemoteCompaction({ enabled: next });
      setSaved(true);
      await onChanged(next);
    } catch (cause) { setError(String(cause)); }
    finally { setSaving(false); onBusyChange(false); }
  };
  return <div className="agent-remote-compaction">
    <label className="agent-provider-identity-label">
      <span>{t('agents.remoteCompaction.label')}</span>
      <Switch checked={enabled || builtinProvider} disabled={disabled || saving || builtinProvider}
        aria-describedby="codex-remote-compaction-hint" onChange={(event) => void apply(event.currentTarget.checked)} />
    </label>
    <p id="codex-remote-compaction-hint" className="agent-model-hint">{t(builtinProvider ? 'agents.remoteCompaction.builtinHint' : 'agents.remoteCompaction.hint')}</p>
    <MessageNotice message={error} />
    <MessageNotice message={saved ? t('agents.remoteCompaction.saved') : ''} tone="success" />
  </div>;
}
