import { AlertCircle, CheckCircle2, Info, TriangleAlert, X } from 'lucide-react';
import type { HTMLAttributes, ReactNode } from 'react';
import { useI18n } from '../i18n';

export type NoticeLevel = 'info' | 'success' | 'warn' | 'error';

export type NoticeProps = Omit<HTMLAttributes<HTMLDivElement>, 'role' | 'title'> & {
  tone: NoticeLevel;
  variant?: 'box' | 'text';
  title?: ReactNode;
  icon?: ReactNode | false;
  action?: ReactNode;
  onDismiss?: () => void;
  dismissLabel?: string;
  live?: boolean;
  bodyClassName?: string;
  bodyTabIndex?: number;
  dismissClassName?: string;
};

const icons = { info: Info, success: CheckCircle2, warn: TriangleAlert, error: AlertCircle };

export function Notice({
  tone, variant = 'box', title, icon, action, onDismiss, dismissLabel, live = true,
  bodyClassName, bodyTabIndex, dismissClassName, className, children, ...props
}: NoticeProps) {
  const { t } = useI18n();
  const Icon = icons[tone];
  const shownIcon = icon === false ? null : icon ?? (variant === 'box' ? <Icon size={16} /> : null);
  const label = dismissLabel ?? t('app.notice.dismiss');
  const liveProps = !live ? {} : tone === 'error'
    ? { role: 'alert', 'aria-live': 'assertive' as const, 'aria-atomic': true }
    : { role: 'status', 'aria-live': 'polite' as const, 'aria-atomic': true };
  return <div {...props} {...liveProps} className={['notice', 'notice-' + variant, 'notice-' + tone, className].filter(Boolean).join(' ')}>
    <div className="notice-main">
      {shownIcon ? <span className="notice-icon" aria-hidden="true">{shownIcon}</span> : null}
      <div className={['notice-body', bodyClassName].filter(Boolean).join(' ')} tabIndex={bodyTabIndex}>
        {title ? <strong className="notice-title">{title}</strong> : null}
        {children}
      </div>
    </div>
    {action}
    {onDismiss ? <button type="button" className={['icon-button quiet ghost tiny-button notice-dismiss', dismissClassName].filter(Boolean).join(' ')}
      onClick={onDismiss} aria-label={label} title={label}>
      <X size={14} aria-hidden="true" />
    </button> : null}
  </div>;
}
