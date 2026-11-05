import { Component, type ErrorInfo, type ReactNode } from 'react';
import { useI18n } from './i18n';
import { EmptyState } from './components/EmptyState';

type Props = { children: ReactNode };
type State = { error: Error | null };

export class AppErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('EasyCLIProxyAPI render failure', error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return <AppErrorFallback error={this.state.error} />;
  }
}

function AppErrorFallback({ error }: { error: Error }) {
  const { t } = useI18n();
  return (
    <main className="app-error-boundary">
      <EmptyState variant="box" title={t('error.render.title')} action={
        <button type="button" className="primary-button" onClick={() => window.location.reload()}>
          {t('error.reload')}
        </button>
      }>
        {error.message || t('error.unknown')}
      </EmptyState>
    </main>
  );
}
