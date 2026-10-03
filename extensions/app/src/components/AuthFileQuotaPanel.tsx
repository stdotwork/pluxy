import { LoaderCircle, RefreshCw } from 'lucide-react';
import { useI18n } from '../i18n';
import { MessageNotice } from '../appNotice';
import { fileName, formatQuotaTimestamp, type AuthFile, type QuotaState } from '../services/quotaService';
import { canResetCodexQuota } from '../services/quotaActions';
import { formatQuotaReset, useQuotaClock } from '../services/quotaTime';
import './AuthFileQuotaPanel.css';

export function AuthFileQuotaPanel({ quota, file, disabled, onRefresh, onReset, compact = false }: {
  quota: QuotaState;
  file: AuthFile;
  disabled: boolean;
  onRefresh: () => void;
  onReset?: () => void;
  compact?: boolean;
}) {
  const { locale, t } = useI18n();
  const now = useQuotaClock() + (quota.serverTimeOffsetMs ?? 0);
  const loading = quota.status === 'loading';
  const name = fileName(file);
  const showReset = Boolean(onReset && (quota.resetCredits ?? 0) > 0);
  const renderRow = (row: QuotaState['rows'][number], index: number) => {
    const percent = row.remainingPercent !== null && Number.isFinite(row.remainingPercent) ? Math.max(0, Math.min(100, row.remainingPercent)) : null;
    const reset = formatQuotaReset(row.resetAtMs, row.reset, locale, now);
    const tone = percent === null ? 'unknown' : percent <= 10 ? 'low' : percent <= 30 ? 'medium' : 'high';
    return <div className={`credential-quota-row ${tone}`} key={`${row.label}-${index}`}>
      <div className="credential-quota-label"><span title={row.label}>{row.label}</span><strong>{percent === null ? '—' : `${Math.round(percent)}%`}</strong></div>
      <div className="credential-quota-track" role={percent === null ? undefined : 'progressbar'} aria-label={`${row.label} · ${t('authFiles.settings.quotaRemaining')}`}
        aria-valuemin={percent === null ? undefined : 0} aria-valuemax={percent === null ? undefined : 100} aria-valuenow={percent ?? undefined}>
        {percent === null ? <span className="sr-only">{t('authFiles.settings.quotaUnknown')}</span> : <i style={{ width: `${percent}%` }} />}
      </div>
      {reset ? <small>{reset}</small> : null}
      {row.detail ? <small>{row.detail}</small> : null}
    </div>;
  };
  return (
    <section className={`credential-quota${compact ? ' credential-quota-compact' : ''}`} aria-label={t('authFiles.quota.aria')} aria-busy={loading}>
      {!compact || showReset ? <div className="credential-quota-heading">
        {!compact ? <strong>{t('authFiles.settings.quotaRemaining')}</strong> : null}
        {!compact && quota.plan ? <span className="credential-quota-plan">{quota.plan}</span> : null}
        {showReset ? <button type="button" className="secondary-button compact-button credential-quota-reset" onClick={onReset} disabled={disabled || !canResetCodexQuota(file, quota)} title={t('quota.reset')}>{t('quota.reset')}</button> : null}
        {!compact ? <button type="button" className="credential-quota-refresh" disabled={disabled || loading} onClick={onRefresh}
          title={disabled ? t('quota.fileDisabled') : t('authFiles.quota.refresh')}>
          {loading ? <LoaderCircle size={13} className="spin" /> : <RefreshCw size={13} />}
          {t(loading ? 'authFiles.quota.querying' : quota.status === 'idle' ? 'authFiles.quota.fetch' : 'common.refresh')}
        </button> : null}
      </div> : null}
      {loading ? <div className="credential-quota-loading" role="status"><span>{t(quota.pendingAction === 'reset' ? 'quota.resetting' : 'authFiles.quota.loading')}</span><div className="credential-quota-track indeterminate"><i /></div></div> : null}
      {quota.status === 'idle' ? <p className="credential-quota-empty">{t(disabled ? 'quota.fileDisabled' : 'authFiles.settings.quotaIdle')}</p> : null}
      {quota.status === 'error' ? <div className="credential-quota-error" role="status"><span>{t('authFiles.quota.failed')}</span>{quota.error ? <small>{quota.error}</small> : null}</div> : null}
      {quota.status === 'success' ? <>
        {quota.rows.length ? <>
          <div className="credential-quota-rows">{(compact ? quota.rows.slice(0, 2) : quota.rows).map(renderRow)}</div>
          {compact && quota.rows.length > 2 ? <details className="credential-quota-more">
            <summary>{t('authFiles.health.details')} (+{quota.rows.length - 2})</summary>
            <div className="credential-quota-rows">{quota.rows.slice(2).map((row, index) => renderRow(row, index + 2))}</div>
          </details> : null}
        </> : <p className="credential-quota-empty">{t('authFiles.quota.empty')}</p>}
        <div className="credential-quota-footnotes">
          {quota.resetCredits !== undefined ? <small>{t('authFiles.quota.resets', { count: quota.resetCredits })}</small> : null}
          {quota.resetCreditsApplicable !== undefined ? <small>{t('quota.resetApplicable', { count: quota.resetCreditsApplicable })}</small> : null}
          {quota.subscriptionActiveUntil ? <small>{t('quota.subscriptionExpiry', { time: formatQuotaTimestamp(quota.subscriptionActiveUntil, locale) })}</small> : null}
          {quota.resetCreditsEarliestExpiry ? <small>{t('authFiles.quota.expiry', { time: formatQuotaTimestamp(quota.resetCreditsEarliestExpiry, locale) })}</small> : null}
        </div>
        <MessageNotice message={quota.resetCreditsError ? name + ': ' + t('quota.resetCreditsWarning', { error: quota.resetCreditsError }) : null} />
      </> : null}
    </section>
  );
}
