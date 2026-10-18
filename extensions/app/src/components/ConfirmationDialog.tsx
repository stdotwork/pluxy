import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { AlertTriangle } from 'lucide-react';
import { useI18n } from '../i18n';
import { usePrivacyMask } from '../privacy';
import { Dialog } from './Dialog';

export type ConfirmationOptions = {
  title: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  warning?: string;
  details?: { label: string; value: string }[];
  variant?: 'primary' | 'danger';
};

type ConfirmationDialogProps = ConfirmationOptions & { onDecision: (confirmed: boolean) => void };

export function ConfirmationDialog({ title, message, confirmText, cancelText, warning, details, variant = 'primary', onDecision }: ConfirmationDialogProps) {
  const { t } = useI18n();
  const privacy = usePrivacyMask();
  const descriptionId = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const cancel = () => onDecision(false);
  return (
    <Dialog
      alert
      size="sm"
      className="config-dialog app-confirm-dialog"
      backdropClassName="app-confirm-backdrop"
      tone={variant === 'danger' ? 'danger' : undefined}
      title={title}
      icon={<AlertTriangle size={20} aria-hidden="true" />}
      describedBy={descriptionId}
      initialFocusRef={cancelRef}
      onClose={cancel}
      dismissOnBackdrop
      actionsClassName="two-actions"
      actions={<>
        <button ref={cancelRef} type="button" className="secondary-button" onClick={cancel}>{cancelText ?? t('common.cancel')}</button>
        <button type="button" className={variant === 'danger' ? 'danger-button' : 'primary-button'} onClick={() => onDecision(true)}>{confirmText ?? t('common.confirm')}</button>
      </>}
    >
      <p id={descriptionId}>{privacy.text(message)}</p>
      {details?.length ? <dl className="app-confirm-details">{details.map((detail) => <div key={detail.label}><dt>{detail.label}</dt><dd>{privacy.text(detail.value)}</dd></div>)}</dl> : null}
      {warning ? <p className="app-confirm-warning">{privacy.text(warning)}</p> : null}
    </Dialog>
  );
}

type PendingConfirmation = { options: ConfirmationOptions; resolve: (confirmed: boolean) => void };

export function useConfirmation() {
  const pendingRef = useRef<PendingConfirmation | null>(null);
  const mountedRef = useRef(true);
  const [pending, setPending] = useState<PendingConfirmation | null>(null);
  const askConfirmation = useCallback((options: ConfirmationOptions): Promise<boolean> => {
    if (!mountedRef.current) return Promise.resolve(false);
    pendingRef.current?.resolve(false);
    return new Promise((resolve) => {
      const request = { options, resolve };
      pendingRef.current = request;
      setPending(request);
    });
  }, []);
  const decide = useCallback((confirmed: boolean) => {
    if (!pending || pendingRef.current !== pending) return;
    pendingRef.current = null;
    setPending(null);
    pending.resolve(confirmed);
  }, [pending]);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      pendingRef.current?.resolve(false);
      pendingRef.current = null;
    };
  }, []);
  return {
    askConfirmation,
    confirmationDialog: pending ? <ConfirmationDialog {...pending.options} onDecision={decide} /> : null,
  };
}
