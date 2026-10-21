import { useEffect, useId, useState } from 'react';
import { Check, Eye, EyeOff, Link2, LoaderCircle, PlugZap, X } from 'lucide-react';
import { Dialog } from '../components/Dialog';
import { Notice } from '../components/Notice';
import { SegmentedControl } from '../components/SegmentedControl';
import { useCoreRuntime } from '../coreRuntime';
import { useI18n } from '../i18n';
import {
  canSaveConnection, getCoreConnection, isCompleteRemoteDraft, isInsecureRemoteOrigin, isValidRemoteOrigin,
  saveCoreConnection, testCoreConnection, type ConnectionTestResult, type CoreConnection,
} from '../services/coreConnection';
import './CoreHealthPanel.css';
import './HomeDashboard.css';
import './CoreConnectionSettings.css';

// One implementation for both hosts: the Advanced Features card and the Home dialog. Both read the saved
// connection from the backend on mount, and every successful save reloads the window, so they cannot drift.
export function CoreConnectionSettings({ variant = 'card', hidden, onClose }: {
  variant?: 'card' | 'dialog';
  hidden?: boolean;
  onClose?: () => void;
}) {
  const { t } = useI18n();
  const dialogId = useId();
  const { status } = useCoreRuntime();
  const [saved, setSaved] = useState<CoreConnection | null>(null);
  const [draft, setDraft] = useState<CoreConnection | null>(null);
  const [showKey, setShowKey] = useState(false);
  const [busy, setBusy] = useState<'test' | 'save' | null>(null);
  const [error, setError] = useState('');
  const [result, setResult] = useState<ConnectionTestResult | null>(null);
  const localCoreRunning = Boolean(status?.managed && status.running);

  useEffect(() => {
    void getCoreConnection().then((value) => { setSaved(value); setDraft(value); }).catch((cause) => setError(String(cause)));
  }, []);

  const edit = (change: Partial<CoreConnection>) => {
    setDraft((value) => value && { ...value, ...change });
    setResult(null);
    setError('');
  };
  const test = async () => {
    if (!draft) return;
    setBusy('test'); setError(''); setResult(null);
    try { setResult(await testCoreConnection(draft)); }
    catch (cause) { setError(String(cause)); }
    finally { setBusy(null); }
  };
  const save = async () => {
    if (!draft) return;
    setBusy('save'); setError(''); setResult(null);
    try {
      await saveCoreConnection(draft);
      window.location.reload();
    } catch (cause) {
      setError(String(cause));
      setBusy(null);
    }
  };

  const remote = draft?.mode === 'remote';
  const originInvalid = remote && draft.managementOrigin.trim() !== '' && !isValidRemoteOrigin(draft.managementOrigin);
  const canSave = Boolean(draft && saved && busy === null && canSaveConnection(draft, saved, localCoreRunning));

  const form = draft ? <div className="core-connection-form">
    <div className="core-connection-mode-field">
      <SegmentedControl className="home-protocol-tabs core-connection-mode" aria-label={t('connection.mode')} value={draft.mode}
        disabled={busy !== null} onChange={(mode) => edit({ mode })}
        items={[{ value: 'local', label: t('connection.local') }, { value: 'remote', label: t('connection.remote') }]} />
      <p className="core-connection-hint">{t(remote ? 'connection.remoteHint' : 'connection.localHint')}</p>
    </div>
    {remote ? <div className="core-connection-fields">
      <label className="config-management-field">
        <span>{t('connection.managementOrigin')}</span>
        <input className="config-dialog-text-input" type="url" inputMode="url" autoComplete="off" spellCheck={false}
          placeholder={t('connection.originPlaceholder')} value={draft.managementOrigin} disabled={busy !== null}
          aria-invalid={originInvalid} onChange={(event) => edit({ managementOrigin: event.currentTarget.value })} />
      </label>
      <label className="config-management-field">
        <span>{t('connection.managementKey')}</span>
        <div className="config-secret-input">
          <input type={showKey ? 'text' : 'password'} autoComplete="off" spellCheck={false} value={draft.managementKey}
            disabled={busy !== null} onChange={(event) => edit({ managementKey: event.currentTarget.value })} />
          <button type="button" className="icon-button quiet" onClick={() => setShowKey((value) => !value)}
            title={t(showKey ? 'config.keys.hide' : 'config.keys.show')} aria-label={t(showKey ? 'config.keys.hide' : 'config.keys.show')}>
            {showKey ? <EyeOff size={16} aria-hidden="true" /> : <Eye size={16} aria-hidden="true" />}
          </button>
        </div>
      </label>
    </div> : null}
    {originInvalid ? <Notice tone="error" variant="text" live={false}>{t('connection.invalidOrigin')}</Notice>
      : isInsecureRemoteOrigin(draft) ? <Notice tone="warn" variant="text">{t('connection.httpWarning')}</Notice> : null}
    {remote && localCoreRunning && saved?.mode !== 'remote' ? <Notice tone="info">{t('connection.stopLocalHint')}</Notice> : null}
    {result ? <Notice tone={result.connected ? 'success' : 'error'} title={t(result.connected ? 'connection.connected' : 'connection.failed')}>
      {result.connected ? result.version || null : result.message}
    </Notice> : null}
    {error ? <Notice tone="error" onDismiss={() => setError('')}>{error}</Notice> : null}
  </div> : error ? <Notice tone="error">{error}</Notice> : null;

  const actions = <div className="core-connection-actions">
    {remote ? <button type="button" className="secondary-button compact-button" disabled={busy !== null || !isCompleteRemoteDraft(draft)} onClick={() => void test()}>
      {busy === 'test' ? <LoaderCircle size={15} className="spin" aria-hidden="true" /> : <PlugZap size={15} aria-hidden="true" />}{t('connection.test')}
    </button> : null}
    <button type="button" className="primary-button compact-button" disabled={!canSave} onClick={() => void save()}>
      <Check size={16} aria-hidden="true" />{busy === 'save' ? t('common.saving') : t(remote ? 'connection.save' : 'connection.useLocal')}
    </button>
  </div>;
  const footnote = <span className="core-connection-footnote">{t('connection.reloadHint')}</span>;

  if (variant === 'dialog') return <Dialog className="core-health-panel core-health-dialog core-connection-dialog" backdropClassName="core-health-backdrop"
    labelledBy={`${dialogId}-title`} describedBy={`${dialogId}-description`} tabIndex={-1} busy={busy !== null}
    onClose={onClose} closeDisabled={busy === 'save'} dismissOnBackdrop>
    <button type="button" className="icon-button quiet small-button core-health-close" onClick={onClose} disabled={busy === 'save'} aria-label={t('common.close')} title={t('common.close')}><X size={18} aria-hidden="true" /></button>
    <div className="core-health-heading">
      <div className="core-health-title">
        <span className="core-health-icon"><Link2 size={19} aria-hidden="true" /></span>
        <div><h2 id={`${dialogId}-title`}>{t('connection.title')}</h2><p id={`${dialogId}-description`}>{t('connection.description')}</p></div>
      </div>
    </div>
    <div className="core-connection-body">{form}</div>
    <div className="core-health-footer core-connection-footer">{footnote}{actions}</div>
  </Dialog>;

  return <section id="config-core-connection" tabIndex={-1} hidden={hidden} className="panel surface-card core-connection-card" aria-labelledby="core-connection-title">
    <div className="config-panel-heading">
      <div className="config-heading-title"><Link2 size={18} aria-hidden="true" /><h2 id="core-connection-title">{t('connection.title')}</h2></div>
    </div>
    {form}
    <div className="config-settings-save-row core-connection-footer">{footnote}{actions}</div>
  </section>;
}
