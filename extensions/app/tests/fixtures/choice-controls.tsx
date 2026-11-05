import { StrictMode, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Dialog } from '../../src/components/Dialog';
import { Tabs } from '../../src/components/Tabs';
import { SegmentedControl } from '../../src/components/SegmentedControl';
import { HomeAccessPanel } from '../../src/pages/HomeAccessPanel';
import { I18nProvider } from '../../src/i18n';
import { requestAppNavigation, APP_NAVIGATION_REQUEST_EVENT, type AppNavigationRequest } from '../../src/navigation';
import '../../src/styles/index.css';

let accept: (() => void) | undefined;
window.addEventListener(APP_NAVIGATION_REQUEST_EVENT, event => {
  event.preventDefault();
  accept = (event as AppNavigationRequest).detail;
});
function Fixture() {
  const [tab, setTab] = useState('one');
  const [segment, setSegment] = useState('one');
  const [waiting, setWaiting] = useState(false);
  const [dialogTab, setDialogTab] = useState('one');
  const [requested, setRequested] = useState<string | null>(null);
  const [manualTab, setManualTab] = useState('one');
  const [plainSegment, setPlainSegment] = useState('one');
  const [fallback, setFallback] = useState(false);
  const items = ['one', 'disabled', 'two', 'three'].map(value => ({ value, label: value, disabled: value === 'disabled', id: `fixture-${value}`, panelId: 'fixture-panel' }));
  return <I18nProvider><Tabs aria-label="Dialog guarded" items={items.map(item => ({ ...item, id: `dialog-${item.value}` }))} value={dialogTab} onChange={setRequested} />
    {requested ? <Dialog label="Navigation guard" onClose={() => setRequested(null)}><button onClick={() => { setDialogTab(requested); setRequested(null); }}>Confirm navigation</button></Dialog> : null}
    <Tabs aria-label="Guarded" items={items} value={tab} onChange={value => requestAppNavigation(() => setTab(value))} />
    <Tabs aria-label="Manual" activation="manual" items={items.map(item => ({ ...item, id: `manual-${item.value}` }))} value={manualTab} onChange={setManualTab} />
    <SegmentedControl aria-label="Plain guarded" items={items} value={plainSegment} onChange={value => { accept = () => setPlainSegment(value); }} />
    <button onClick={() => setTab('two')}>Programmatic tab</button>
    <button onClick={() => setPlainSegment('two')}>Programmatic segment</button>
    <div id="fixture-panel" role="tabpanel" aria-labelledby={`fixture-${tab}`}>{tab}</div>
    <button onClick={() => accept?.()}>Accept</button><button onClick={() => { accept = undefined; setWaiting(false); }}>Reject</button>
    <SegmentedControl aria-label="Delayed" items={items} value={segment} disabled={waiting} onChange={value => { setWaiting(true); accept = () => { setSegment(value); setWaiting(false); }; }} />
    <button onClick={() => setFallback(true)}>Remove OpenAI</button>
    <HomeAccessPanel profiles={(fallback ? ['claude', 'gemini'] as const : ['openai', 'claude', 'gemini'] as const).map(id => ({ id, name: id, description: id, baseUrl: '/mock' }))}
      apiKey={null} keyError={false} ready copiedField="" onCopy={async () => {}} />
  </I18nProvider>;
}
createRoot(document.getElementById('root')!).render(<StrictMode><Fixture /></StrictMode>);
