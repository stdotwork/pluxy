import { useEffect, useLayoutEffect, useId, useRef, useState, type ReactNode } from 'react';
import { Check, RefreshCw, Search, SlidersHorizontal, X } from 'lucide-react';
import { MessageNotice } from '../appNotice';
import { useDialogFocusTrap } from '../components/useDialogFocusTrap';
import { useI18n } from '../i18n';

const VISIBLE_CLIENTS_KEY = 'cpa-gui.agent-visible-clients.v1';

type Client<Id extends string> = {
  id: Id;
  name: string;
  icon: ReactNode;
  summary: string;
  installed: boolean;
  detected: boolean;
};

function readVisibleClients<Id extends string>(clients: Client<Id>[]): Id[] | null {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(VISIBLE_CLIENTS_KEY) ?? 'null');
    if (!Array.isArray(value)) return null;
    const known = clients.filter((client) => value.includes(client.id)).map((client) => client.id);
    return known.length ? known : null;
  } catch {
    return null;
  }
}

export function AgentClientList<Id extends string>({
  clients, selected, onSelect, onRefresh, loading, busy, error, onDismissError,
}: {
  clients: Client<Id>[];
  selected: Id;
  onSelect: (id: Id) => void;
  onRefresh: () => void;
  loading: boolean;
  busy: boolean;
  error: string;
  onDismissError: () => void;
}) {
  const { t } = useI18n();
  const [visibleIds, setVisibleIds] = useState(() => readVisibleClients(clients));
  const [managing, setManaging] = useState(false);
  const [picking, setPicking] = useState(false);
  const [capacity, setCapacity] = useState(1);
  const listRef = useRef<HTMLDivElement>(null);
  const close = () => { setManaging(false); setPicking(false); };
  const [draftIds, setDraftIds] = useState<Id[] | null>(null);
  const [query, setQuery] = useState('');
  // Keep the current client reachable even when detection fails or it was uninstalled.
  const automaticIds = clients.filter((client) => client.detected || client.id === selected)
    .map((client) => client.id);
  const visibleClients = clients.filter((client) => (visibleIds ?? automaticIds).includes(client.id));
  const shortcuts = visibleClients.slice(0, capacity);
  const current = visibleClients.find((client) => client.id === selected);
  if (current && !shortcuts.includes(current)) shortcuts[shortcuts.length - 1] = current;
  const draftSelection = draftIds ?? automaticIds;
  const searchRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  const dialogRef = useDialogFocusTrap<HTMLDialogElement>({
    active: managing || picking,
    onEscape: close,
    initialFocusRef: searchRef,
  });

  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const resize = () => setCapacity(Math.max(1, Math.min(6, Math.floor((list.clientHeight + 4) / 60))));
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(list);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (!busy && visibleIds && !visibleIds.includes(selected) && visibleIds.length) {
      onSelect(visibleIds[0]);
    }
  }, [busy, onSelect, selected, visibleIds]);

  useEffect(() => {
    if (managing || picking) dialogRef.current?.showModal();
  }, [dialogRef, managing, picking]);

  const pick = (id: Id) => {
    // A manually hidden client can be brought back directly from the switcher.
    if (visibleIds && !visibleIds.includes(id)) {
      const next = [...visibleIds, id];
      setVisibleIds(next);
      try { window.localStorage.setItem(VISIBLE_CLIENTS_KEY, JSON.stringify(next)); } catch { /* Session only. */ }
    }
    onSelect(id);
    close();
  };

  const save = () => {
    if (!draftSelection.length) return;
    try {
      if (draftIds === null) window.localStorage.removeItem(VISIBLE_CLIENTS_KEY);
      else window.localStorage.setItem(VISIBLE_CLIENTS_KEY, JSON.stringify(draftIds));
    } catch {
      // Still allow a session-only preference when browser storage is unavailable.
    }
    setVisibleIds(draftIds);
    if (!draftSelection.includes(selected)) {
      const first = clients.find((client) => draftSelection.includes(client.id));
      if (first) onSelect(first.id);
    }
    setManaging(false);
  };

  const matches = clients.filter((client) => client.name.toLowerCase().includes(query.trim().toLowerCase()));

  return <>
    <aside className="panel agent-client-list" aria-label={t('agents.localClients')}>
      <div className="agent-client-list-heading">
        <strong>{t('agents.localClients')}</strong>
        <button type="button" className="icon-button quiet" onClick={onRefresh}
          disabled={loading || busy} title={t('agents.redetect')} aria-label={t('agents.redetect')}>
          <RefreshCw size={15} className={loading ? 'spin' : ''} aria-hidden="true" />
        </button>
      </div>
      {error ? <MessageNotice message={error} onDismiss={onDismissError} /> : null}
      <div className="agent-list-items" ref={listRef}>
        {shortcuts.map((client) => <button type="button" key={client.id}
          className={selected === client.id ? 'active' : ''} aria-pressed={selected === client.id}
          onClick={() => onSelect(client.id)} disabled={busy}>
          <span className="agent-client-icon">{client.icon}</span>
          <span><strong title={client.name}>{client.name}</strong><small title={client.summary}>{client.summary}</small></span>
          {client.installed ? <i className="agent-installed-indicator" title={t('agents.clientDetected')} aria-hidden="true" /> : null}
        </button>)}
      </div>
      <div className="agent-client-list-footer">
        <button type="button" className="secondary-button agent-client-switch" disabled={busy || loading}
          onClick={() => { setQuery(''); setPicking(true); }}>
          <Search size={15} aria-hidden="true" />{t('agents.clients.switch')}
        </button>
        <small>{t('agents.clients.shown', { count: shortcuts.length, total: clients.length })}</small>
        <button type="button" className="secondary-button compact-button" disabled={busy || loading}
          onClick={() => { setDraftIds(visibleIds); setQuery(''); setManaging(true); }}>
          <SlidersHorizontal size={15} aria-hidden="true" />{t('agents.clients.manage')}
        </button>
      </div>
    </aside>
    {managing || picking ? <dialog className="agent-client-manager" ref={dialogRef} aria-labelledby={titleId}
      aria-describedby={descriptionId} onCancel={(event) => { event.preventDefault(); close(); }}>
      <header>
        <h2 id={titleId}>{t(picking ? 'agents.clients.switch' : 'agents.clients.manage')}</h2>
        <button type="button" className="icon-button quiet" aria-label={t('common.close')} onClick={close}>
          <X size={18} aria-hidden="true" />
        </button>
      </header>
      <p id={descriptionId}>{t(picking ? 'agents.clients.switchHint' : 'agents.clients.description')}</p>
      <div className="agent-client-search">
        <Search size={16} aria-hidden="true" />
        <input ref={searchRef} type="search" value={query} onChange={(event) => setQuery(event.currentTarget.value)}
          placeholder={t('agents.clients.search')} aria-label={t('agents.clients.search')} />
      </div>
      <div className="agent-client-catalog">
        {matches.map((client) => picking ? <button type="button" key={client.id} className="agent-client-option"
          aria-label={client.name} aria-pressed={selected === client.id} onClick={() => pick(client.id)}>
          <span className="agent-client-icon">{client.icon}</span>
          <span className="agent-client-option-copy"><strong>{client.name}</strong><small title={client.summary}>{client.summary}</small></span>
          {selected === client.id ? <Check size={16} aria-hidden="true" /> : null}
        </button> : <label key={client.id} className="agent-client-option">
          <input type="checkbox" aria-label={client.name} checked={draftSelection.includes(client.id)}
            onChange={(event) => setDraftIds(event.currentTarget.checked
              ? [...draftSelection, client.id] : draftSelection.filter((id) => id !== client.id))} />
          <span className="agent-client-icon">{client.icon}</span>
          <span className="agent-client-option-copy"><strong>{client.name}</strong><small title={client.summary}>{client.summary}</small></span>
        </label>)}
        {!matches.length ? <p role="status">{t('agents.clients.noResults')}</p> : null}
      </div>
      {managing ? <><div className="agent-client-manager-defaults">
        <button type="button" className="secondary-button compact-button" onClick={() => setDraftIds(null)}>
          {t('agents.clients.automatic')}
        </button>
        <small>{t('agents.clients.automaticHint')}</small>
      </div>
      <footer>
        <span role="status">{t(draftSelection.length ? 'agents.clients.selected' : 'agents.clients.minimum', { count: draftSelection.length })}</span>
        <div>
          <button type="button" className="secondary-button" onClick={() => setManaging(false)}>{t('common.cancel')}</button>
          <button type="button" className="primary-button" onClick={save} disabled={!draftSelection.length}>{t('common.save')}</button>
        </div>
      </footer></> : null}
    </dialog> : null}
  </>;
}
