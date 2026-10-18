import {
  listAgentConfigBackups,
  previewAgentConfigBackup,
  restoreAgentConfigBackup,
  deleteAgentConfigBackup,
} from '../services/agents';
import type { AgentConfigBackupListing, AgentConfigBackupVersion, AgentConfigBackupPreview } from '../services/agents';
import { MessageNotice } from '../appNotice';
import { useEffect, useState } from 'react';
import { ArchiveRestore, LoaderCircle, X } from 'lucide-react';
import { useI18n } from '../i18n';
import { Dialog } from '../components/Dialog';
import { useConfirmation } from '../components/ConfirmationDialog';
import './AgentConfigBackupDialog.css';

export function AgentConfigBackupDialog({ client, onClose, onRestored }: {
  client: string; onClose: () => void; onRestored: () => Promise<void>;
}) {
  const { t, formatDate } = useI18n();
  const [listing, setListing] = useState<AgentConfigBackupListing>({ versions: [] });
  const [selected, setSelected] = useState<AgentConfigBackupVersion | null>(null);
  const [preview, setPreview] = useState<AgentConfigBackupPreview | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const { askConfirmation, confirmationDialog } = useConfirmation();
  useEffect(() => {
    let disposed = false;
    void listAgentConfigBackups({ client })
      .then((value) => { if (!disposed) setListing(value); })
      .catch((cause) => { if (!disposed) setError(String(cause)); })
      .finally(() => { if (!disposed) setBusy(false); });
    return () => { disposed = true; };
  }, [client]);
  const choose = async (version: AgentConfigBackupVersion) => {
    setBusy(true); setError(''); setSelected(version); setPreview(null);
    try {
      if (version.restorable) setPreview(await previewAgentConfigBackup({ client, id: version.id }));
    } catch (cause) { setError(String(cause)); }
    finally { setBusy(false); }
  };
  const confirm = async (confirmation: 'restore' | 'delete') => {
    if (!selected || (confirmation === 'restore' && !preview)) return;
    const confirmed = await askConfirmation({
      title: t(confirmation === 'restore' ? 'agents.backup.restoreConfirm' : 'agents.backup.deleteConfirm', { version: date(selected) }),
      message: t(confirmation === 'restore' ? 'agents.backup.restoreWarning' : 'agents.backup.deleteWarning'),
      confirmText: t(confirmation === 'restore' ? 'agents.backup.confirmRestore' : 'agents.backup.confirmDelete'),
      variant: 'danger',
    });
    if (!confirmed) return;
    setBusy(true); setError('');
    try {
      if (confirmation === 'restore') {
        await restoreAgentConfigBackup({ client, id: selected.id, revision: preview!.revision });
        await onRestored(); onClose();
      } else {
        await deleteAgentConfigBackup({ client, id: selected.id });
        setSelected(null); setPreview(null);
        setListing(await listAgentConfigBackups({ client }));
      }
    } catch (cause) { setError(String(cause)); setPreview(null); }
    finally { setBusy(false); }
  };
  const date = (version: AgentConfigBackupVersion) => version.createdAt
    ? formatDate(version.createdAt, { dateStyle: 'short', timeStyle: 'medium' }) : version.id;
  return <Dialog className="agent-backup-modal" labelledBy="agent-backup-title" onClose={onClose} closeDisabled={busy}>
    <header><h2 id="agent-backup-title"><ArchiveRestore size={20} />{t('agents.backup.button')}</h2>
      <button type="button" className="secondary-button" onClick={onClose} disabled={busy} aria-label={t('common.cancel')}><X size={18} /></button></header>
    <p>{t('agents.backup.description')}</p>
    <div className="agent-backup-columns">
      <nav aria-label={t('agents.backup.versions')}>
        {!listing.versions.length && !busy && <p>{t('agents.backup.empty')}</p>}
        {listing.versions.map((version) => <button type="button" key={version.id} disabled={busy} className={selected?.id === version.id ? 'active' : ''} onClick={() => void choose(version)}>
          <strong>{date(version)}</strong>
          <small>{t('agents.backup.files', { count: version.fileCount })}</small>
          {!version.restorable && <span>{t('agents.backup.invalid')}</span>}
        </button>)}
      </nav>
      <section className="agent-backup-changes" aria-label={t('agents.backup.preview')}>
        {busy && <p role="status"><LoaderCircle size={16} className="spin" /> {t('agents.backup.loading')}</p>}
        {!selected && !busy && <p>{t('agents.backup.select')}</p>}
        {selected && <>
          <strong>{t('agents.backup.location')}</strong><p className="agent-backup-path"><code>{selected.location}</code></p>
          {selected.error && <p className="agent-inline-message warning">{selected.error}</p>}
          <ul className="agent-backup-files">{selected.files.map((file) => <li key={file.path}>
            <code>{file.path}</code><small>{file.exists === null ? t('agents.backup.invalid') : file.exists ? `${t('agents.backup.present')} · ${file.size ?? 0} B` : t('agents.backup.missing')}</small>
          </li>)}</ul>
          {preview && <p>{t(preview.differences.length ? 'agents.backup.overwrite' : 'agents.backup.unchanged')}</p>}
          {preview?.differences.filter((diff) => diff.field.startsWith('modelMappings')).map((diff) => <p key={`${diff.file}:${diff.field}`}>{t('agents.backup.mappingRestore')} <code>{diff.field}</code></p>)}
        </>}
      </section>
    </div>
    {error && <MessageNotice message={error} onDismiss={() => setError('')} />}
    <footer>
      <button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{t('common.cancel')}</button>
      <button type="button" className="danger-button" disabled={busy || !selected} onClick={() => void confirm('delete')}>{t('agents.backup.delete')}</button>
      <button type="button" className="primary-button" disabled={busy || !preview} onClick={() => void confirm('restore')}>{t('agents.backup.restore')}</button>
    </footer>
    {confirmationDialog}
  </Dialog>;
}
