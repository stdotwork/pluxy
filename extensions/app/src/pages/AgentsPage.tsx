import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type KeyboardEvent,
} from 'react';
import { invoke } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
import {
  AlertTriangle,
  BadgeCheck,
  Bot,
  Check,
  ChevronDown,
  LoaderCircle,
  Play,
  Power,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react';
import claudeIcon from '../assets/icons/claude.svg';
import codexIcon from '../assets/icons/codex.svg';
import hermesIcon from '../assets/icons/hermes.png';
import openclawIcon from '../assets/icons/openclaw.svg';
import opencodeIcon from '../assets/icons/opencode.svg';
import {
  agentModelAlias,
  filterAgentModels,
  findAgentModel,
  resolveAgentModelSelection,
} from '../services/agentModelPicker';
import type { ModelOption } from '../services/modelService';
import { getCurrentLocale, translate, useI18n } from '../i18n';
import { CodexSessionAutoRestoreCard } from './CodexSessionAutoRestoreCard';
import { CodexSessionsPanel } from './CodexSessionsPanel';

type AgentClientId =
  | 'claude-code'
  | 'claude-desktop'
  | 'codex'
  | 'opencode'
  | 'openclaw'
  | 'hermes';

type AgentModificationState = 'unconfigured' | 'applied' | 'external-changed' | 'invalid';

type AgentConfigStatus = {
  id: AgentClientId;
  name: string;
  supportedPlatform: boolean;
  installed: boolean;
  executablePath: string | null;
  launchTargets: AgentLaunchTarget[];
  version: string | null;
  configValid: boolean;
  configured: boolean;
  currentModel: string | null;
  modificationEnabled: boolean;
  modificationState: AgentModificationState;
  backupAvailable: boolean;
  appliedModel: string | null;
  warnings: string[];
  error: string | null;
};

type AgentLaunchTarget = {
  id: string;
  label: string;
  detail: string;
};

type AgentConfigActionResult = {
  outcome: 'applied' | 'default';
  enabled: boolean;
  model: string | null;
  changedFiles: string[];
  conflictFiles: string[];
};

type ChatGptCloseResult = {
  wasRunning: boolean;
  closedProcesses: number;
};

type AgentDefinition = {
  id: AgentClientId;
  name: string;
  icon?: string;
  Icon?: ComponentType<{ size?: number; 'aria-hidden'?: boolean }>;
  descriptionKey: 'agents.description.claudeCode' | 'agents.description.claudeDesktop' | 'agents.description.codex' | 'agents.description.opencode' | 'agents.description.openclaw' | 'agents.description.hermes';
};

type AgentSubpageId = 'core' | 'sessions';

type AgentSubpageDefinition = {
  id: AgentSubpageId;
  labelKey: 'agents.tabs.core' | 'agents.tabs.sessions';
  clients?: readonly AgentClientId[];
};

const agentDefinitions: AgentDefinition[] = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    icon: claudeIcon,
    descriptionKey: 'agents.description.claudeCode',
  },
  {
    id: 'claude-desktop',
    name: 'Claude Desktop',
    icon: claudeIcon,
    descriptionKey: 'agents.description.claudeDesktop',
  },
  {
    id: 'codex',
    name: 'Codex',
    icon: codexIcon,
    descriptionKey: 'agents.description.codex',
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    icon: opencodeIcon,
    descriptionKey: 'agents.description.opencode',
  },
  {
    id: 'openclaw',
    name: 'OpenClaw',
    icon: openclawIcon,
    descriptionKey: 'agents.description.openclaw',
  },
  {
    id: 'hermes',
    name: 'Hermes Agent',
    icon: hermesIcon,
    descriptionKey: 'agents.description.hermes',
  },
];

const agentSubpages: AgentSubpageDefinition[] = [
  {
    id: 'core',
    labelKey: 'agents.tabs.core',
  },
  {
    id: 'sessions',
    labelKey: 'agents.tabs.sessions',
    clients: ['codex'],
  },
];

const DEFAULT_AGENT_SUBPAGE: AgentSubpageId = 'core';

const AGENT_MODEL_SELECTIONS_KEY = 'cpa-gui.agent-model-selections.v1';
const AGENT_SELECTED_CLIENT_KEY = 'cpa-gui.agent-selected-client.v1';

const readSelectedAgentClient = (): AgentClientId => {
  const fallback = agentDefinitions[0].id;
  if (typeof window === 'undefined') return fallback;
  try {
    const saved = window.localStorage.getItem(AGENT_SELECTED_CLIENT_KEY);
    return agentDefinitions.some((agent) => agent.id === saved)
      ? (saved as AgentClientId)
      : fallback;
  } catch {
    return fallback;
  }
};

const writeSelectedAgentClient = (client: AgentClientId) => {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(AGENT_SELECTED_CLIENT_KEY, client);
  } catch {
    // Keep the current in-memory selection when persistent storage is unavailable.
  }
};

const readAgentModelSelections = (): Partial<Record<AgentClientId, string>> => {
  if (typeof window === 'undefined') return {};
  try {
    const payload = window.localStorage.getItem(AGENT_MODEL_SELECTIONS_KEY);
    if (!payload) return {};
    const parsed = JSON.parse(payload) as Record<string, unknown>;
    return agentDefinitions.reduce<Partial<Record<AgentClientId, string>>>((result, agent) => {
      const value = parsed[agent.id];
      if (typeof value === 'string' && value.trim()) result[agent.id] = value.trim();
      return result;
    }, {});
  } catch {
    return {};
  }
};

const writeAgentModelSelections = (
  selections: Partial<Record<AgentClientId, string>>,
) => {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(AGENT_MODEL_SELECTIONS_KEY, JSON.stringify(selections));
  } catch {
    // Local storage can be unavailable in hardened webviews; the in-memory selection still works.
  }
};

const reconcileAgentModelSelections = (
  current: Partial<Record<AgentClientId, string>>,
  models: ModelOption[],
) => {
  return agentDefinitions.reduce<Partial<Record<AgentClientId, string>>>((result, agent) => {
    const existing = current[agent.id] ?? '';
    result[agent.id] = resolveAgentModelSelection(models, existing);
    return result;
  }, {});
};

function AgentMark({ definition, size = 26 }: { definition: AgentDefinition; size?: number }) {
  if (definition.icon) {
    return <img src={definition.icon} alt="" className="provider-logo" />;
  }
  const Icon = definition.Icon ?? Bot;
  return <Icon size={size} aria-hidden />;
}

const listStatusText = (status: AgentConfigStatus | undefined) => {
  const locale = getCurrentLocale();
  if (!status) return translate(locale, 'agents.list.detecting');
  if (!status.supportedPlatform) return translate(locale, 'agents.list.unsupported');
  if (!status.installed) return translate(locale, 'agents.list.notInstalled');
  if (status.modificationState === 'external-changed') return translate(locale, 'agents.status.externalChanged');
  if (status.modificationState === 'invalid') return translate(locale, 'agents.status.invalid');
  if (status.modificationState === 'applied') return translate(locale, 'agents.list.modified', { model: status.appliedModel ?? '—' });
  return status.version
    ? translate(locale, 'agents.list.installedVersion', { version: status.version })
    : translate(locale, 'agents.list.installed');
};

type AgentModelPickerProps = {
  models: ModelOption[];
  value: string;
  loading: boolean;
  error: string;
  disabled: boolean;
  onChange: (value: string) => void;
  onRefresh: () => void;
};

type AgentModelDropdownLayout = {
  top: number;
  left: number;
  width: number;
  height: number;
};

function AgentModelPicker({
  models,
  value,
  loading,
  error,
  disabled,
  onChange,
  onRefresh,
}: AgentModelPickerProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);
  const [dropdownLayout, setDropdownLayout] = useState<AgentModelDropdownLayout | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const visibleModels = useMemo(() => filterAgentModels(models, search), [models, search]);
  const choices = useMemo(
    () => visibleModels.map((model) => ({ name: model.name, alias: model.alias ?? '' })),
    [visibleModels],
  );
  const selectedAlias = agentModelAlias(models, value);

  const updateDropdownLayout = useCallback(() => {
    const root = rootRef.current;
    if (!root) return;

    const rect = root.getBoundingClientRect();
    const edgeGap = 12;
    const triggerGap = 6;
    const preferredHeight = 282;
    const minimumHeight = 150;
    const spaceBelow = Math.max(0, window.innerHeight - rect.bottom - triggerGap - edgeGap);
    const spaceAbove = Math.max(0, rect.top - triggerGap - edgeGap);
    const placeAbove = spaceBelow < preferredHeight && spaceAbove > spaceBelow;
    const availableHeight = placeAbove ? spaceAbove : spaceBelow;
    const height = Math.min(preferredHeight, Math.max(minimumHeight, availableHeight));
    const width = Math.min(rect.width, window.innerWidth - edgeGap * 2);
    const left = Math.min(
      Math.max(edgeGap, rect.left),
      Math.max(edgeGap, window.innerWidth - edgeGap - width),
    );
    const desiredTop = placeAbove
      ? rect.top - triggerGap - height
      : rect.bottom + triggerGap;
    const top = Math.min(
      Math.max(edgeGap, desiredTop),
      Math.max(edgeGap, window.innerHeight - edgeGap - height),
    );

    setDropdownLayout({ top, left, width, height });
  }, []);

  useLayoutEffect(() => {
    if (!open) {
      setDropdownLayout(null);
      return undefined;
    }

    updateDropdownLayout();
    window.addEventListener('resize', updateDropdownLayout);
    window.addEventListener('scroll', updateDropdownLayout);
    return () => {
      window.removeEventListener('resize', updateDropdownLayout);
      window.removeEventListener('scroll', updateDropdownLayout);
    };
  }, [open, updateDropdownLayout]);

  useEffect(() => {
    if (!open) return undefined;
    const close = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    setSearch('');
    const selectedIndex = filterAgentModels(models, '').findIndex(
      (model) => model.name.toLocaleLowerCase() === value.trim().toLocaleLowerCase(),
    );
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
    requestAnimationFrame(() => searchRef.current?.focus());
  }, [open]);

  useEffect(() => {
    setActiveIndex((current) => Math.min(current, Math.max(choices.length - 1, 0)));
  }, [choices.length]);

  const choose = (name: string) => {
    onChange(name);
    setOpen(false);
  };

  const moveActive = (offset: number) => {
    if (choices.length === 0) return;
    setActiveIndex((current) => (current + offset + choices.length) % choices.length);
  };

  const handleSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      moveActive(1);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      moveActive(-1);
    } else if (event.key === 'Enter' && choices[activeIndex]) {
      event.preventDefault();
      choose(choices[activeIndex].name);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      setOpen(false);
    }
  };

  return (
    <div className={`agent-model-picker ${open ? 'open' : ''}`} ref={rootRef}>
      <button
        type="button"
        className="agent-model-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (!open && ['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(event.key)) {
            event.preventDefault();
            setOpen(true);
          }
        }}
      >
        <span>
          <strong title={value || undefined}>
            {value || (loading ? t('agents.model.loading') : error ? t('agents.model.loadFailed') : models.length ? t('agents.model.select') : t('agents.model.none'))}
          </strong>
          {selectedAlias ? <small title={selectedAlias}>{selectedAlias}</small> : null}
        </span>
        <ChevronDown size={17} aria-hidden />
      </button>

      {open ? (
        <div
          className="agent-model-dropdown"
          style={dropdownLayout
            ? dropdownLayout
            : { top: 0, left: 0, width: 0, height: 0, visibility: 'hidden' }}
        >
          <div className="agent-model-search">
            <Search size={15} aria-hidden />
            <input
              ref={searchRef}
              value={search}
              onChange={(event) => {
                setSearch(event.currentTarget.value);
                setActiveIndex(0);
              }}
              onKeyDown={handleSearchKeyDown}
              placeholder={t('agents.model.search')}
              role="combobox"
              aria-controls="agent-model-listbox"
              aria-expanded="true"
            />
            {search ? (
              <button
                type="button"
                className="icon-button quiet"
                onClick={() => {
                  setSearch('');
                  setActiveIndex(0);
                  searchRef.current?.focus();
                }}
                title={t('agents.model.clearSearch')}
              >
                <X size={14} />
              </button>
            ) : null}
            <button type="button" className="icon-button quiet" onClick={onRefresh} disabled={loading} title={t('agents.model.refresh')}>
              <RefreshCw size={14} className={loading ? 'spin' : ''} />
            </button>
          </div>

          <div className="agent-model-list" id="agent-model-listbox" role="listbox">
            {loading && models.length === 0 ? (
              <div className="agent-model-empty"><LoaderCircle size={18} className="spin" />{t('agents.model.fetching')}</div>
            ) : error && models.length === 0 ? (
              <div className="agent-model-empty error"><strong>{t('agents.model.loadFailed')}</strong><span>{error}</span></div>
            ) : choices.length === 0 ? (
              <div className="agent-model-empty">
                <strong>{search.trim() ? t('agents.model.noMatch') : t('agents.model.unavailable')}</strong>
                <span>{search.trim() ? t('agents.model.tryKeywords') : t('agents.model.connectFirst')}</span>
              </div>
            ) : choices.map((choice, index) => {
              const selected = choice.name.toLocaleLowerCase() === value.trim().toLocaleLowerCase();
              return (
                <button
                  type="button"
                  role="option"
                  aria-selected={selected}
                  className={`agent-model-option ${selected ? 'selected' : ''} ${index === activeIndex ? 'active' : ''}`}
                  key={choice.name}
                  onMouseEnter={() => setActiveIndex(index)}
                  onClick={() => choose(choice.name)}
                >
                  <span>
                    <strong title={choice.name}>{choice.name}</strong>
                    <small>{choice.alias || t('agents.model.available')}</small>
                  </span>
                  {selected ? <Check size={16} aria-hidden /> : null}
                </button>
              );
            })}
          </div>
          <div className="agent-model-dropdown-footer">
            <span>{t('agents.model.count', { count: models.length })}</span>
            {error && models.length > 0 ? <span className="error">{t('agents.model.stale')}</span> : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function AgentsPage() {
  const { t } = useI18n();
  const [selected, setSelected] = useState<AgentClientId>(readSelectedAgentClient);
  const [activeSubpage, setActiveSubpage] = useState<AgentSubpageId>(DEFAULT_AGENT_SUBPAGE);
  const [statuses, setStatuses] = useState<AgentConfigStatus[]>([]);
  const [models, setModels] = useState<ModelOption[]>([]);
  const [modelByClient, setModelByClient] = useState<Partial<Record<AgentClientId, string>>>(
    readAgentModelSelections,
  );
  const [launchTargetByClient, setLaunchTargetByClient] = useState<Partial<Record<AgentClientId, string>>>({});
  const [loading, setLoading] = useState(true);
  const [modelLoading, setModelLoading] = useState(false);
  const [busyAction, setBusyAction] = useState<'apply' | 'default' | 'clear' | 'launch' | 'close-app' | null>(null);
  const busy = busyAction !== null;
  const [detectionError, setDetectionError] = useState('');
  const [modelError, setModelError] = useState('');
  const [modelSelectionError, setModelSelectionError] = useState('');
  const [configurationError, setConfigurationError] = useState('');
  const [launchError, setLaunchError] = useState('');
  const [defaultError, setDefaultError] = useState('');
  const [defaultConfirmOpen, setDefaultConfirmOpen] = useState(false);
  const [clearError, setClearError] = useState('');
  const [clearNotice, setClearNotice] = useState('');
  const [clearConfirmOpen, setClearConfirmOpen] = useState(false);
  const [closeAppError, setCloseAppError] = useState('');
  const [closeAppNotice, setCloseAppNotice] = useState('');
  const [closeAppConfirmOpen, setCloseAppConfirmOpen] = useState(false);

  const loadStatuses = useCallback(async (forceRefresh = false) => {
    const command = forceRefresh
      ? 'refresh_agent_config_statuses'
      : 'get_agent_config_statuses';
    const nextStatuses = await invoke<AgentConfigStatus[]>(command);
    setStatuses(nextStatuses);
  }, []);

  const loadModels = useCallback(async () => {
    setModelLoading(true);
    setModelError('');
    try {
      const nextModels = await invoke<ModelOption[]>('get_agent_models');
      setModels(nextModels);
      setModelSelectionError('');
      setModelByClient((current) => {
        const next = reconcileAgentModelSelections(current, nextModels);
        writeAgentModelSelections(next);
        return next;
      });
    } catch (requestError) {
      setModelError(String(requestError));
    } finally {
      setModelLoading(false);
    }
  }, []);

  const refresh = useCallback(async () => {
    setLoading(true);
    setDetectionError('');
    try {
      await Promise.all([loadStatuses(true), loadModels()]);
    } catch (requestError) {
      setDetectionError(String(requestError));
    } finally {
      setLoading(false);
    }
  }, [loadModels, loadStatuses]);

  useEffect(() => {
    setLoading(true);
    setDetectionError('');
    void Promise.all([loadStatuses(), loadModels()])
      .catch((requestError) => setDetectionError(String(requestError)))
      .finally(() => setLoading(false));
  }, [loadModels, loadStatuses]);

  useEffect(() => {
    let disposed = false;
    let stop: (() => void) | null = null;
    void listen('config-files-changed', () => {
      if (disposed) return;
      setDetectionError('');
      void Promise.all([loadStatuses(true), loadModels()]).catch((requestError) => {
        if (!disposed) setDetectionError(String(requestError));
      });
    }).then((unlisten) => {
      if (disposed) unlisten();
      else stop = unlisten;
    });
    return () => {
      disposed = true;
      stop?.();
    };
  }, [loadModels, loadStatuses]);

  useEffect(() => {
    writeSelectedAgentClient(selected);
  }, [selected]);

  useEffect(() => {
    setActiveSubpage(DEFAULT_AGENT_SUBPAGE);
    setModelSelectionError('');
    setConfigurationError('');
    setLaunchError('');
    setDefaultError('');
    setDefaultConfirmOpen(false);
    setClearError('');
    setClearNotice('');
    setClearConfirmOpen(false);
    setCloseAppError('');
    setCloseAppNotice('');
    setCloseAppConfirmOpen(false);
  }, [selected]);

  useEffect(() => {
    setLaunchTargetByClient((current) => agentDefinitions.reduce<Partial<Record<AgentClientId, string>>>(
      (next, definition) => {
        const targets = statuses.find((status) => status.id === definition.id)?.launchTargets ?? [];
        const previous = current[definition.id] ?? '';
        next[definition.id] = targets.some((target) => target.id === previous)
          ? previous
          : targets[0]?.id ?? '';
        return next;
      },
      {},
    ));
  }, [statuses]);

  const activeDefinition = agentDefinitions.find((agent) => agent.id === selected)
    ?? agentDefinitions[0];
  const activeStatus = statuses.find((status) => status.id === selected) ?? null;
  const savedSelectedModel = modelByClient[selected] ?? '';
  const selectedModelOption = findAgentModel(models, savedSelectedModel);
  const selectedModel = selectedModelOption?.name ?? '';
  const activeLaunchTargets = activeStatus?.launchTargets ?? [];
  const selectedLaunchTargetId = launchTargetByClient[selected] ?? activeLaunchTargets[0]?.id ?? '';
  const selectedLaunchTarget = activeLaunchTargets.find(
    (target) => target.id === selectedLaunchTargetId,
  ) ?? activeLaunchTargets[0] ?? null;
  const appliedModel = activeStatus?.appliedModel ?? activeStatus?.currentModel ?? '';
  const draftChanged = Boolean(
    selectedModel.trim()
      && appliedModel.trim()
      && selectedModel.trim() !== appliedModel.trim(),
  );
  const canEnable = Boolean(
    activeStatus?.supportedPlatform
      && activeStatus.installed
      && !modelLoading
      && selectedModelOption,
  );
  const canLaunch = Boolean(
    activeStatus?.supportedPlatform
      && activeStatus.installed
      && selectedLaunchTarget
      && (selected === 'codex'
        || (activeStatus.modificationEnabled && activeStatus.modificationState === 'applied')),
  );
  const modelHint = modelSelectionError
    || modelError
    || (modelLoading
      ? t('agents.model.readingAvailable')
      : models.length === 0
        ? ''
        : activeStatus?.modificationState === 'applied'
          ? t('agents.model.current', { model: appliedModel || '—' })
          : t('agents.model.firstSelection', { count: models.length }));
  const modificationDescription = activeStatus?.modificationState === 'external-changed'
    ? t('agents.modify.externalChanged')
    : activeStatus?.modificationState === 'invalid'
      ? t('agents.modify.invalid')
      : activeStatus?.modificationState === 'applied'
        ? t('agents.modify.applied')
        : '';
  const footerMessage = activeStatus?.modificationState === 'applied'
    ? draftChanged
      ? t('agents.footer.changed')
      : t('agents.footer.applied')
    : activeStatus?.modificationState === 'external-changed'
      ? t('agents.footer.externalChanged')
      : activeStatus?.modificationState === 'invalid'
        ? t('agents.footer.invalidManaged')
        : !activeStatus?.supportedPlatform
          ? t('agents.footer.unsupported')
          : !activeStatus.installed
            ? t('agents.footer.installFirst')
            : activeStatus.launchTargets.length === 0
              ? t('agents.footer.noCommand')
              : '';

  const refreshModels = () => {
    void loadModels();
  };

  const reloadStatusesAfterAction = async () => {
    setDetectionError('');
    try {
      await loadStatuses(true);
    } catch (requestError) {
      setDetectionError(String(requestError));
    }
  };

  const selectModel = (value: string) => {
    const model = findAgentModel(models, value);
    if (!model) return;
    setModelSelectionError('');
    setModelByClient((current) => {
      const next = { ...current, [selected]: model.name };
      writeAgentModelSelections(next);
      return next;
    });
  };

  const requireSelectedModel = () => {
    if (modelLoading) {
      setModelSelectionError(t('agents.error.modelsLoading'));
      return null;
    }
    if (models.length === 0) {
      setModelSelectionError(modelError || t('agents.error.noModels'));
      return null;
    }
    const model = findAgentModel(models, selectedModel);
    if (!model) {
      setModelSelectionError(t('agents.error.selectionGone'));
      return null;
    }
    setModelSelectionError('');
    return model.name;
  };

  const applyConfigurationChanges = async () => {
    setConfigurationError('');
    const model = requireSelectedModel();
    if (!model) return;
    setBusyAction('apply');
    try {
      await invoke<AgentConfigActionResult>('apply_agent_config', {
        client: selected,
        model,
      });
      await reloadStatusesAfterAction();
    } catch (requestError) {
      setConfigurationError(String(requestError));
    } finally {
      setBusyAction(null);
    }
  };

  const resetConfigurationToDefault = async () => {
    const model = requireSelectedModel();
    if (!model) return;
    setBusyAction('default');
    setDefaultError('');
    try {
      await invoke<AgentConfigActionResult>('reset_agent_config_to_default', {
        client: selected,
        model,
      });
      setDefaultConfirmOpen(false);
      await reloadStatusesAfterAction();
    } catch (requestError) {
      setDefaultError(String(requestError));
    } finally {
      setBusyAction(null);
    }
  };

  const clearCodexConfiguration = async () => {
    setBusyAction('clear');
    setClearError('');
    setClearNotice('');
    try {
      await invoke<string[]>('clear_codex_config');
      setClearConfirmOpen(false);
      setClearNotice(t('agents.clear.success'));
      await reloadStatusesAfterAction();
    } catch (requestError) {
      setClearError(String(requestError));
    } finally {
      setBusyAction(null);
    }
  };

  const launchAgent = async () => {
    setBusyAction('launch');
    setLaunchError('');
    try {
      if (selected !== 'codex' && draftChanged) {
        throw new Error(t('agents.error.applyFirst'));
      }
      await invoke('launch_agent', { client: selected, target: selectedLaunchTarget?.id });
    } catch (requestError) {
      setLaunchError(String(requestError));
    } finally {
      setBusyAction(null);
    }
  };

  const closeChatGptApp = async () => {
    setBusyAction('close-app');
    setCloseAppError('');
    setCloseAppNotice('');
    try {
      const result = await invoke<ChatGptCloseResult>('close_chatgpt_app');
      setCloseAppConfirmOpen(false);
      setCloseAppNotice(result.wasRunning
        ? t('agents.closeApp.success', { count: result.closedProcesses })
        : t('agents.closeApp.notRunning'));
    } catch (requestError) {
      setCloseAppError(String(requestError));
    } finally {
      setBusyAction(null);
    }
  };

  const openDefaultConfirmation = () => {
    setDefaultError('');
    setDefaultConfirmOpen(true);
  };

  const closeDefaultConfirmation = () => {
    setDefaultError('');
    setDefaultConfirmOpen(false);
  };

  const openClearConfirmation = () => {
    setClearError('');
    setClearConfirmOpen(true);
  };

  const closeClearConfirmation = () => {
    setClearError('');
    setClearConfirmOpen(false);
  };

  const openCloseAppConfirmation = () => {
    setCloseAppError('');
    setCloseAppConfirmOpen(true);
  };

  const closeCloseAppConfirmation = () => {
    setCloseAppError('');
    setCloseAppConfirmOpen(false);
  };

  const availableSubpages = agentSubpages.filter(
    (subpage) => !subpage.clients || subpage.clients.includes(selected),
  );

  return (
    <section className="page management-page agents-page">
      <header className="management-header">
        <div>
          <span>Agent Clients</span>
          <h1>{t('agents.title')}</h1>
        </div>
        <div className="agent-header-actions">
          {detectionError ? (
            <span className="agent-inline-message error" role="alert" aria-live="polite">
              {detectionError}
            </span>
          ) : null}
          <button type="button" className="secondary-button compact-button" onClick={() => void refresh()} disabled={loading || busy}>
            <RefreshCw size={16} className={loading ? 'spin' : ''} />
            {t('agents.redetect')}
          </button>
        </div>
      </header>

      <div className="agent-workbench">
        <aside className="panel agent-client-list">
          <div className="agent-list-heading">
            <Bot size={18} />
            <div><strong>{t('agents.localClients')}</strong><span>{t('agents.selectClient')}</span></div>
          </div>
          <div className="agent-list-items">
            {agentDefinitions.map((agent) => {
              const status = statuses.find((item) => item.id === agent.id);
              return (
                <button
                  type="button"
                  className={selected === agent.id ? 'active' : ''}
                  key={agent.id}
                  onClick={() => {
                    setActiveSubpage(DEFAULT_AGENT_SUBPAGE);
                    setSelected(agent.id);
                  }}
                  disabled={busy}
                >
                  <span className="agent-client-icon"><AgentMark definition={agent} /></span>
                  <span><strong>{agent.name}</strong><small>{listStatusText(status)}</small></span>
                  <i
                    className={status?.modificationEnabled ? 'configured' : status?.installed ? 'installed' : ''}
                    aria-hidden="true"
                  />
                </button>
              );
            })}
          </div>
        </aside>

        <section className="panel agent-config-panel">
          <div className="agent-subpage-tabs" role="tablist" aria-label={t('agents.tabs.label')}>
            {availableSubpages.map((subpage) => (
              <button
                type="button"
                id={`agent-subpage-tab-${subpage.id}`}
                role="tab"
                className={activeSubpage === subpage.id ? 'active' : ''}
                aria-selected={activeSubpage === subpage.id}
                aria-controls={`agent-subpage-panel-${subpage.id}`}
                tabIndex={activeSubpage === subpage.id ? 0 : -1}
                key={subpage.id}
                onClick={() => setActiveSubpage(subpage.id)}
              >
                {t(subpage.labelKey)}
              </button>
            ))}
          </div>

          {activeSubpage === 'core' ? (
            <div
              className="agent-core-config"
              id="agent-subpage-panel-core"
              role="tabpanel"
              aria-labelledby="agent-subpage-tab-core"
            >
              <div className="agent-status-grid">
                <div>
                  <span><BadgeCheck size={14} />{t('agents.installStatus')}</span>
                  <strong>{activeStatus?.installed ? t('agents.clientDetected') : t('agents.clientNotDetected')}</strong>
                </div>
                <div>
                  <span>{t('agents.clientVersion')}</span>
                  <strong title={activeStatus?.version ?? undefined}>{activeStatus?.version ?? t('agents.notFetched')}</strong>
                </div>
              </div>

              {activeStatus?.error || activeStatus?.warnings.length ? (
                <div className="agent-status-messages" aria-live="polite">
                  {activeStatus.error ? (
                    <span className="agent-inline-message error" role="alert">{activeStatus.error}</span>
                  ) : (
                    <span className="agent-inline-message warning">{activeStatus.warnings.join('；')}</span>
                  )}
                </div>
              ) : null}

              <section className="agent-core-setting-section agent-model-section">
                <div className="agent-section-heading">
                  <div><strong>{t('agents.useModel')}</strong></div>
                  {draftChanged ? <span className="agent-pending-badge">{t('agents.pending')}</span> : null}
                </div>
                <AgentModelPicker
                  models={models}
                  value={selectedModel}
                  loading={modelLoading}
                  error={modelError}
                  disabled={busy || !activeStatus?.installed || !activeStatus.supportedPlatform}
                  onChange={selectModel}
                  onRefresh={refreshModels}
                />
                {modelHint ? (
                  <span
                    className={`agent-model-hint ${modelSelectionError || modelError ? 'error' : ''}`}
                    role={modelSelectionError || modelError ? 'alert' : undefined}
                    aria-live="polite"
                  >
                    {modelHint}
                  </span>
                ) : null}
              </section>

              <section className={`agent-core-setting-section agent-modification-actions ${activeStatus?.modificationState === 'applied' ? 'enabled' : ''}`}>
                <div className="agent-section-heading">
                  <div>
                    <strong>{t('agents.modify.title')}</strong>
                    {modificationDescription ? <span>{modificationDescription}</span> : null}
                  </div>
                </div>
                <div className="agent-modification-control">
                  <div className={`agent-modification-buttons ${selected === 'codex' ? 'codex' : ''}`}>
                    <button
                      type="button"
                      className="primary-button"
                      onClick={() => void applyConfigurationChanges()}
                      disabled={
                        busy
                        || !canEnable
                      }
                    >
                      {busyAction === 'apply' ? <LoaderCircle size={16} className="spin" /> : <Sparkles size={16} />}
                      {t('agents.modify.apply')}
                    </button>
                    <button
                      type="button"
                      className="secondary-button"
                      onClick={openDefaultConfirmation}
                      disabled={busy || !canEnable}
                    >
                      {busyAction === 'default' ? <LoaderCircle size={16} className="spin" /> : <RefreshCw size={16} />}
                      {t('agents.modify.default')}
                    </button>
                    {selected === 'codex' ? (
                      <button
                        type="button"
                        className="danger-button"
                        onClick={openClearConfirmation}
                        disabled={busy}
                      >
                        {busyAction === 'clear' ? <LoaderCircle size={16} className="spin" /> : <Trash2 size={16} />}
                        {t('agents.modify.clear')}
                      </button>
                    ) : null}
                  </div>
                  {configurationError ? (
                    <span className="agent-inline-message error" role="alert" aria-live="polite">
                      {configurationError}
                    </span>
                  ) : null}
                  {clearNotice ? (
                    <span className="agent-inline-message" role="status" aria-live="polite">
                      {clearNotice}
                    </span>
                  ) : null}
                </div>
              </section>

              {selected === 'codex' ? <CodexSessionAutoRestoreCard /> : null}

              <div className="agent-config-footer">
                {footerMessage ? (
                  <div className="agent-config-summary">
                    {activeStatus?.modificationState === 'applied' ? <Check size={16} /> : <Sparkles size={16} />}
                    <span>{footerMessage}</span>
                  </div>
                ) : null}
                <div className="agent-launch-control">
                  <div className="agent-launch-actions">
                    {activeLaunchTargets.length > 1 ? (
                      <div className="agent-launch-targets" aria-label={t('agents.launchMethods')}>
                        {activeLaunchTargets.map((target) => (
                          <button
                            type="button"
                            className={target.id === selectedLaunchTarget?.id ? 'active' : ''}
                            key={target.id}
                            onClick={() => setLaunchTargetByClient((current) => ({
                              ...current,
                              [selected]: target.id,
                            }))}
                            disabled={busy}
                            title={target.detail}
                          >
                            {target.label.replace('Codex ', '')}
                          </button>
                        ))}
                      </div>
                    ) : null}
                    <button
                      type="button"
                      className="primary-button"
                      onClick={() => void launchAgent()}
                      disabled={
                        busy
                        || !canLaunch
                        || (selected !== 'codex' && draftChanged)
                      }
                      title={selected === 'codex'
                        ? selectedLaunchTarget?.detail
                        : draftChanged
                          ? t('agents.launch.applyFirst')
                          : activeStatus?.modificationState === 'applied'
                            ? selectedLaunchTarget?.detail
                            : t('agents.launch.enableFirst')}
                    >
                      {busyAction === 'launch' ? <LoaderCircle size={16} className="spin" /> : <Play size={16} />}
                      {busyAction === 'launch' ? t('agents.launch.starting') : selectedLaunchTarget ? t('agents.launch.start', { target: selectedLaunchTarget.label }) : t('agents.launch.unavailable')}
                    </button>
                    {selected === 'codex' ? (
                      <button
                        type="button"
                        className="danger-button agent-close-app-button"
                        onClick={openCloseAppConfirmation}
                        disabled={busy}
                      >
                        {busyAction === 'close-app' ? <LoaderCircle size={16} className="spin" /> : <Power size={16} />}
                        {busyAction === 'close-app' ? t('agents.launch.closingChatgpt') : t('agents.launch.closeChatgpt')}
                      </button>
                    ) : null}
                  </div>
                  {launchError ? (
                    <span className="agent-inline-message error" role="alert" aria-live="polite">
                      {launchError}
                    </span>
                  ) : null}
                  {closeAppNotice ? (
                    <span className="agent-inline-message" role="status" aria-live="polite">
                      {closeAppNotice}
                    </span>
                  ) : null}
                </div>
              </div>
            </div>
          ) : null}

          {selected === 'codex' && activeSubpage === 'sessions' ? (
            <div
              className="agent-sessions-page"
              id="agent-subpage-panel-sessions"
              role="tabpanel"
              aria-labelledby="agent-subpage-tab-sessions"
            >
              <CodexSessionsPanel managedProviderActive={activeStatus?.modificationState === 'applied'} />
            </div>
          ) : null}
        </section>
      </div>

      {defaultConfirmOpen ? (
        <div className="config-dialog-backdrop">
          <section className="config-dialog agent-restore-dialog" role="alertdialog" aria-modal="true" aria-labelledby="agent-default-title">
            <div className="config-dialog-heading">
              <div><AlertTriangle size={19} /><h2 id="agent-default-title">{t('agents.default.title')}</h2></div>
            </div>
            <p>
              {t('agents.default.description', { name: activeDefinition.name })}
            </p>
            {defaultError ? (
              <span className="agent-inline-message error" role="alert" aria-live="polite">
                {defaultError}
              </span>
            ) : null}
            <div className="config-dialog-actions two-actions">
              <button type="button" className="secondary-button" onClick={closeDefaultConfirmation} disabled={busy}>{t('common.cancel')}</button>
              <button type="button" className="danger-button" onClick={() => void resetConfigurationToDefault()} disabled={busy}>
                {busyAction === 'default' ? <LoaderCircle size={16} className="spin" /> : null}
                {t('agents.default.confirm')}
              </button>
            </div>
          </section>
        </div>
      ) : null}

      {clearConfirmOpen ? (
        <div className="config-dialog-backdrop">
          <section className="config-dialog agent-restore-dialog" role="alertdialog" aria-modal="true" aria-labelledby="agent-clear-title">
            <div className="config-dialog-heading">
              <div><AlertTriangle size={19} /><h2 id="agent-clear-title">{t('agents.clear.title')}</h2></div>
            </div>
            <p>{t('agents.clear.description')}</p>
            {clearError ? (
              <span className="agent-inline-message error" role="alert" aria-live="polite">
                {clearError}
              </span>
            ) : null}
            <div className="config-dialog-actions two-actions">
              <button type="button" className="secondary-button" onClick={closeClearConfirmation} disabled={busy}>{t('common.cancel')}</button>
              <button type="button" className="danger-button" onClick={() => void clearCodexConfiguration()} disabled={busy}>
                {busyAction === 'clear' ? <LoaderCircle size={16} className="spin" /> : <Trash2 size={16} />}
                {t('agents.clear.confirm')}
              </button>
            </div>
          </section>
        </div>
      ) : null}

      {closeAppConfirmOpen ? (
        <div className="config-dialog-backdrop">
          <section className="config-dialog agent-restore-dialog" role="alertdialog" aria-modal="true" aria-labelledby="agent-close-app-title">
            <div className="config-dialog-heading">
              <div><AlertTriangle size={19} /><h2 id="agent-close-app-title">{t('agents.closeApp.title')}</h2></div>
            </div>
            <p>{t('agents.closeApp.description')}</p>
            {closeAppError ? (
              <span className="agent-inline-message error" role="alert" aria-live="polite">
                {closeAppError}
              </span>
            ) : null}
            <div className="config-dialog-actions two-actions">
              <button type="button" className="secondary-button" onClick={closeCloseAppConfirmation} disabled={busy}>{t('common.cancel')}</button>
              <button type="button" className="danger-button" onClick={() => void closeChatGptApp()} disabled={busy}>
                {busyAction === 'close-app' ? <LoaderCircle size={16} className="spin" /> : <Power size={16} />}
                {t('agents.closeApp.confirm')}
              </button>
            </div>
          </section>
        </div>
      ) : null}
    </section>
  );
}
