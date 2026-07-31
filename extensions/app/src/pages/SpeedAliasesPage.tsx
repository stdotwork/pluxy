import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { invoke } from '@tauri-apps/api/core';
import {
  ArrowRight,
  Check,
  GitFork,
  LoaderCircle,
  Search,
  Trash2,
  Zap,
} from 'lucide-react';
import { useI18n } from '../i18n';
import { thinkingAliasSourceKindLabel } from './ThinkingAliasesPage';

type SpeedAliasEntry = {
  sourceModel: string;
  alias: string;
  serviceTier: string;
  provider: string;
  kind: string;
};

type SpeedAliasSource = {
  id: string;
  model: string;
  displayName: string | null;
  provider: string;
  kind: string;
  protocol: string;
};

export function SpeedAliasesPage() {
  const { t } = useI18n();
  const [entries, setEntries] = useState<SpeedAliasEntry[]>([]);
  const [sources, setSources] = useState<SpeedAliasSource[]>([]);
  const [selectedSourceId, setSelectedSourceId] = useState('');
  const [alias, setAlias] = useState('');
  const [search, setSearch] = useState('');
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  const [activeSourceIndex, setActiveSourceIndex] = useState(0);
  const modelPickerRef = useRef<HTMLDivElement>(null);
  const [loading, setLoading] = useState(true);
  const [busyAlias, setBusyAlias] = useState('');
  const [busyAction, setBusyAction] = useState<'create' | 'delete' | ''>('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [nextEntries, nextSources] = await Promise.all([
        invoke<SpeedAliasEntry[]>('get_speed_aliases'),
        invoke<SpeedAliasSource[]>('get_speed_alias_sources'),
      ]);
      setEntries(nextEntries);
      setSources(nextSources);
      setSelectedSourceId((current) => (
        nextSources.some((source) => source.id === current) ? current : ''
      ));
    } catch (requestError) {
      setError(String(requestError));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!modelPickerOpen) return undefined;
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (!modelPickerRef.current?.contains(event.target as Node)) {
        setModelPickerOpen(false);
        setSearch('');
      }
    };
    document.addEventListener('pointerdown', closeOnOutsideClick);
    return () => document.removeEventListener('pointerdown', closeOnOutsideClick);
  }, [modelPickerOpen]);

  const selectedSource = useMemo(
    () => sources.find((source) => source.id === selectedSourceId) ?? null,
    [selectedSourceId, sources],
  );

  const filteredSources = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return sources;
    return sources.filter((source) => {
      const haystack = [
        source.model,
        source.displayName ?? '',
        source.provider,
        thinkingAliasSourceKindLabel(source.kind),
      ].join(' ').toLowerCase();
      return haystack.includes(query);
    });
  }, [search, sources]);

  useEffect(() => {
    setActiveSourceIndex(0);
  }, [search, sources]);

  const chooseSource = (source: SpeedAliasSource) => {
    setSelectedSourceId(source.id);
    setModelPickerOpen(false);
    setSearch('');
  };

  const handleModelSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape') {
      setModelPickerOpen(false);
      setSearch('');
      event.currentTarget.blur();
      return;
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (!modelPickerOpen) {
        setModelPickerOpen(true);
        return;
      }
      if (!filteredSources.length) return;
      const direction = event.key === 'ArrowDown' ? 1 : -1;
      setActiveSourceIndex((current) => (
        (current + direction + filteredSources.length) % filteredSources.length
      ));
      return;
    }
    if (event.key === 'Enter' && modelPickerOpen && filteredSources[activeSourceIndex]) {
      event.preventDefault();
      chooseSource(filteredSources[activeSourceIndex]);
    }
  };

  const createAlias = async () => {
    if (!selectedSource) {
      setError(t('aliases.error.selectModel'));
      return;
    }
    const normalizedAlias = alias.trim();
    if (!normalizedAlias) {
      setError(t('aliases.error.emptyAlias'));
      return;
    }
    setBusyAlias(normalizedAlias);
    setBusyAction('create');
    setError('');
    setNotice('');
    try {
      const nextEntries = await invoke<SpeedAliasEntry[]>('create_speed_alias', {
        sourceId: selectedSource.id,
        alias: normalizedAlias,
      });
      setEntries(nextEntries);
      setNotice(t('speedAliases.created', { alias: normalizedAlias }));
      setAlias('');
    } catch (requestError) {
      setError(String(requestError));
    } finally {
      setBusyAlias('');
      setBusyAction('');
    }
  };

  const deleteAlias = async (entry: SpeedAliasEntry) => {
    if (!window.confirm(t('speedAliases.deleteConfirm', { alias: entry.alias }))) return;
    setBusyAlias(entry.alias);
    setBusyAction('delete');
    setError('');
    setNotice('');
    try {
      const nextEntries = await invoke<SpeedAliasEntry[]>('delete_speed_alias', {
        alias: entry.alias,
      });
      setEntries(nextEntries);
      setNotice(t('speedAliases.deleted', { alias: entry.alias }));
    } catch (requestError) {
      setError(String(requestError));
    } finally {
      setBusyAlias('');
      setBusyAction('');
    }
  };

  return (
    <section className="page management-page thinking-alias-page">
      <div className="thinking-alias-feedback" aria-live="polite">
        {error ? <div className="management-alert error">{error}</div> : null}
        {!error && notice ? <div className="management-alert success">{notice}</div> : null}
      </div>

      <div className="thinking-alias-workbench">
        <section className="panel thinking-alias-editor-panel">
          <div className="thinking-alias-panel-heading">
            <span><Zap size={18} /></span>
            <div>
              <h2>{t('speedAliases.create.title')}</h2>
              <p>{t('speedAliases.create.description')}</p>
            </div>
          </div>

          <div className="thinking-alias-field thinking-model-field">
            <label htmlFor="speed-model-search">{t('aliases.originalModel')}</label>
            <div className="thinking-model-picker" ref={modelPickerRef}>
              <div className="thinking-model-search">
                {loading ? <LoaderCircle size={15} className="spin" /> : <Search size={15} />}
                <input
                  id="speed-model-search"
                  role="combobox"
                  aria-autocomplete="list"
                  aria-expanded={modelPickerOpen}
                  aria-controls="speed-model-options"
                  aria-activedescendant={modelPickerOpen && filteredSources[activeSourceIndex]
                    ? `speed-model-option-${activeSourceIndex}`
                    : undefined}
                  value={modelPickerOpen ? search : selectedSource?.model ?? ''}
                  onFocus={(event) => {
                    setSearch(selectedSource?.model ?? '');
                    setModelPickerOpen(true);
                    event.currentTarget.select();
                  }}
                  onChange={(event) => {
                    setSearch(event.currentTarget.value);
                    setModelPickerOpen(true);
                  }}
                  onKeyDown={handleModelSearchKeyDown}
                  placeholder={loading ? t('aliases.loadingModels') : t('aliases.searchModel')}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={loading}
                />
                {!modelPickerOpen && selectedSource ? (
                  <span className="thinking-source-kind">
                    {thinkingAliasSourceKindLabel(selectedSource.kind)}
                  </span>
                ) : null}
              </div>
              {modelPickerOpen ? (
                <div
                  className="thinking-model-list"
                  id="speed-model-options"
                  role="listbox"
                  aria-label={t('aliases.availableModels')}
                >
                  {filteredSources.length === 0 ? (
                    <div className="thinking-model-empty">
                      {sources.length ? t('aliases.noMatch') : t('aliases.noModels')}
                    </div>
                  ) : filteredSources.map((source, index) => {
                    const selected = source.id === selectedSourceId;
                    return (
                      <button
                        type="button"
                        role="option"
                        aria-selected={selected}
                        id={`speed-model-option-${index}`}
                        className={`${selected ? 'selected ' : ''}${index === activeSourceIndex ? 'active' : ''}`.trim()}
                        key={source.id}
                        onMouseEnter={() => setActiveSourceIndex(index)}
                        onClick={() => chooseSource(source)}
                        disabled={Boolean(busyAlias)}
                      >
                        <span className="thinking-model-option-copy">
                          <span>
                            <strong title={source.model}>{source.model}</strong>
                            {source.displayName && source.displayName !== source.model
                              ? <small>{source.displayName}</small>
                              : null}
                          </span>
                          <span className="thinking-model-source">
                            <em>{thinkingAliasSourceKindLabel(source.kind)}</em>
                            <small title={source.provider}>{source.provider}</small>
                          </span>
                        </span>
                        {selected ? <Check size={15} /> : null}
                      </button>
                    );
                  })}
                </div>
              ) : null}
            </div>
            {selectedSource ? (
              <div className="thinking-model-selection">
                <span>{thinkingAliasSourceKindLabel(selectedSource.kind)}</span>
                <strong title={selectedSource.provider}>{selectedSource.provider}</strong>
              </div>
            ) : (
              <small className="thinking-model-hint">{t('aliases.sourceHint')}</small>
            )}
          </div>

          <div className="thinking-alias-preview">
            <Zap size={18} />
            <div>
              <span>{t('speedAliases.fast.title')}</span>
              <code>service_tier = priority</code>
            </div>
          </div>

          <div className="thinking-alias-section-divider" aria-hidden="true" />

          <div className="thinking-alias-field">
            <div className="thinking-field-heading">
              <strong>{t('aliases.aliasName.title')}</strong>
              <span>{t('aliases.aliasName.description')}</span>
            </div>
            <input
              id="speed-alias-name"
              className="thinking-alias-input"
              value={alias}
              onChange={(event) => setAlias(event.currentTarget.value)}
              placeholder={selectedSource
                ? t('speedAliases.aliasName.example', { model: selectedSource.model })
                : t('aliases.aliasName.selectFirst')}
              disabled={Boolean(busyAlias)}
            />
          </div>

          <div className="thinking-alias-preview">
            <GitFork size={18} />
            <div>
              <span>{selectedSource?.model || t('aliases.notSelected')} <ArrowRight size={13} /> {alias || t('aliases.enterAlias')}</span>
              <code>service_tier = priority</code>
            </div>
          </div>

          <button
            type="button"
            className="primary-button thinking-alias-create"
            onClick={() => void createAlias()}
            disabled={loading || Boolean(busyAlias) || !selectedSource || !alias.trim()}
          >
            {busyAction === 'create' ? <LoaderCircle size={16} className="spin" /> : <Zap size={16} />}
            {busyAction === 'create' ? t('speedAliases.creating') : t('speedAliases.create')}
          </button>
        </section>

        <section className="panel thinking-alias-list-panel">
          <div className="thinking-alias-list-heading">
            <div>
              <h2>{t('speedAliases.createdList.title')}</h2>
              <span>{t('speedAliases.createdList.description')}</span>
            </div>
            <strong>{entries.length}</strong>
          </div>

          <div className="thinking-alias-list">
            {loading ? (
              <div className="management-loading"><LoaderCircle size={20} className="spin" />{t('aliases.loadingConfig')}</div>
            ) : entries.length === 0 ? (
              <div className="management-empty">
                <Zap size={25} />
                <strong>{t('speedAliases.empty.title')}</strong>
                <span>{t('speedAliases.empty.description')}</span>
              </div>
            ) : entries.map((entry) => (
              <article className="thinking-alias-row" key={`${entry.kind}:${entry.provider}:${entry.alias}`}>
                <div className="thinking-alias-route">
                  <div className="thinking-alias-route-source">
                    <span title={entry.sourceModel}>{entry.sourceModel}</span>
                    <small>
                      <em>{thinkingAliasSourceKindLabel(entry.kind)}</em>
                      <span title={entry.provider}>{entry.provider}</span>
                    </small>
                  </div>
                  <ArrowRight size={14} />
                  <strong title={entry.alias}>{entry.alias}</strong>
                </div>
                <span className="thinking-effort-badge">{entry.serviceTier}</span>
                <button
                  type="button"
                  className="icon-button danger"
                  onClick={() => void deleteAlias(entry)}
                  disabled={Boolean(busyAlias)}
                  title={t('speedAliases.delete', { alias: entry.alias })}
                >
                  {busyAction === 'delete' && busyAlias === entry.alias
                    ? <LoaderCircle size={15} className="spin" />
                    : <Trash2 size={15} />}
                </button>
              </article>
            ))}
          </div>
        </section>
      </div>
    </section>
  );
}
