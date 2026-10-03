import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { invoke } from '@tauri-apps/api/core';
import { AlertTriangle, ExternalLink, X } from 'lucide-react';
import { useDialogFocusTrap } from '../components/useDialogFocusTrap';
import { useI18n } from '../i18n';
import { pluginText } from '../i18n/plugins';
import { pluginStoreApi, type PluginStoreEntry, type PluginStoreInstallResult } from '../services/plugins';
import { buildRepositoryURL, getPluginConfirmToken, isOfficialPlugin } from '../services/pluginResources';
import { isValidManualReleaseTag, supportsPluginVersionSelection } from '../services/pluginReleaseVersions';

export function PluginInstallDialog({ entry, onClose, onInstalled }: {
  entry: PluginStoreEntry;
  onClose: () => void;
  onInstalled: (result: PluginStoreInstallResult) => void;
}) {
  const { t, locale } = useI18n();
  const pt = (key: Parameters<typeof pluginText>[0]) => pluginText(key, locale);
  const [version, setVersion] = useState('');
  const [step, setStep] = useState(0);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const pending = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  // The dialog remains open throughout installation, including on errors.
  const ref = useDialogFocusTrap<HTMLElement>({ onEscape: busy ? undefined : onClose, preventEscape: busy });
  const thirdParty = !isOfficialPlugin(entry);
  const token = getPluginConfirmToken(entry);
  const canSelectVersion = supportsPluginVersionSelection(entry.installType);
  const validVersion = !version.trim() || isValidManualReleaseTag(version);
  const repository = buildRepositoryURL(entry.repository);
  const action = entry.installed ? pt('update') : pt('install');
  const install = async () => {
    if (pending.current || !validVersion || (thirdParty && (step < 2 || typed.trim() !== token))) return;
    pending.current = true; setBusy(true); setError('');
    try {
      const result = await pluginStoreApi.install(entry.id, { sourceId: entry.sourceId, version: canSelectVersion ? version.trim() : undefined });
      if (mounted.current) onInstalled(result);
    } catch (reason) {
      if (mounted.current) setError(reason instanceof Error ? reason.message : String(reason));
    } finally { pending.current = false; if (mounted.current) setBusy(false); }
  };
  return createPortal(<div className="config-dialog-backdrop">
    <section className="config-dialog plugin-install-dialog" role="dialog" aria-modal="true" aria-labelledby="plugin-install-title" ref={ref} tabIndex={-1}>
      <div className="config-dialog-heading"><h2 id="plugin-install-title">{action} · {entry.name || entry.id}</h2><button className="icon-button quiet" aria-label={t('common.close')} disabled={busy} onClick={onClose}><X size={18} /></button></div>
      <div className="plugin-install-body">
        <dl className="plugin-install-origin"><dt>{pt('source')}</dt><dd>{entry.sourceName || entry.sourceId}<small>{entry.sourceUrl}</small></dd><dt>{pt('repository')}</dt><dd>{entry.repository || '—'}{repository && <button className="icon-button quiet" aria-label={pt('repository')} onClick={() => void invoke('open_external_url', { url: repository }).catch(e => setError(String(e)))}><ExternalLink size={15} /></button>}</dd></dl>
        {step === 0 && <><p>{pt('installHint')}</p>{canSelectVersion && <label className="plugin-field">{pt('version')}<input value={version} onChange={e => setVersion(e.target.value)} disabled={busy} placeholder={entry.version || 'v1.0.0'} aria-invalid={!validVersion} />{!validVersion && <small role="alert">{pt('invalidVersion')}</small>}</label>}</>}
        {thirdParty && step >= 1 && <p className="plugin-warning"><AlertTriangle size={18} aria-hidden="true" />{pt('trustWarning')}</p>}
        {thirdParty && step === 2 && <label className="plugin-field">{pt('trustType')}<code>{token}</code><input value={typed} onChange={e => setTyped(e.target.value)} disabled={busy} autoComplete="off" spellCheck={false} /></label>}
        {error && <p className="plugin-error" role="alert">{error}</p>}
      </div>
      <div className="plugin-dialog-actions"><button className="secondary-button" disabled={busy} onClick={onClose}>{t('common.cancel')}</button>{thirdParty && step < 2 ? <button className="primary-button" disabled={!validVersion} onClick={() => setStep(step + 1)}>{step === 0 ? action : pt('trustNext')}</button> : <button className={thirdParty ? 'danger-button' : 'primary-button'} disabled={busy || !validVersion || (thirdParty && typed.trim() !== token)} onClick={() => void install()}>{busy ? t('common.loading') : action}</button>}</div>
    </section>
  </div>, document.body);
}
