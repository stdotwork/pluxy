import { useCallback, useEffect, useMemo, useState } from "react";
import { Check, LoaderCircle, RotateCcw, Search, SlidersHorizontal, X } from "lucide-react";
import { Dialog } from "../components/Dialog";
import { Switch } from "../components/Switch";
import { Checkbox } from "../components/Checkbox";
import { MessageNotice } from "../appNotice";
import { useI18n } from "../i18n";
import {
  getZCodeModelCatalogEditor,
  saveZCodeModelCatalogEditor,
  type ZCodeCatalogEditorModel,
  type ZCodeCatalogEditorSnapshot,
} from "../services/zcodeModelCatalog";

const COMMON_REASONING_LEVELS = ["disabled", "enabled", "low", "medium", "high", "xhigh", "max"];

export function ZCodeModelCatalogDialog({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: () => void | Promise<void>;
}) {
  const { t } = useI18n();
  const [snapshot, setSnapshot] = useState<ZCodeCatalogEditorSnapshot | null>(null);
  const [models, setModels] = useState<ZCodeCatalogEditorModel[]>([]);
  const [modelOrder, setModelOrder] = useState<string[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    setNotice("");
    try {
      const next = await getZCodeModelCatalogEditor();
      setSnapshot(next);
      setModels(next.models);
      setModelOrder(next.modelOrder);
      setSelectedId((current) => current || next.models[0]?.id || "");
    } catch (e) {
      setSnapshot(null);
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const selectedModel = useMemo(
    () => models.find((m) => m.id === selectedId) ?? null,
    [models, selectedId]
  );

  const filteredModels = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return models;
    return models.filter((m) => m.id.toLowerCase().includes(q));
  }, [models, search]);

  const toggleModelEnabled = (id: string, enabled: boolean) => {
    setModels((prev) =>
      prev.map((m) => (m.id === id ? { ...m, enabled } : m))
    );
  };

  const updateSelectedConfig = (
    updater: (prev: ZCodeCatalogEditorModel["configuration"]) => ZCodeCatalogEditorModel["configuration"]
  ) => {
    if (!selectedModel) return;
    setModels((prev) =>
      prev.map((m) => (m.id === selectedId ? { ...m, configuration: { ...updater(m.configuration), isManual: true } } : m))
    );
  };

  const toggleReasoningLevel = (level: string) => {
    if (!selectedModel) return;
    updateSelectedConfig((prev) => {
      const base = prev.reasoningLevels;
      const exists = base.includes(level);
      const nextLevels = exists
        ? base.filter((l) => l !== level)
        : [...base, level];
      return { ...prev, reasoningLevels: nextLevels };
    });
  };

  const restoreSelectedDefaults = () => {
    if (!selectedModel) return;
    setModels((prev) =>
      prev.map((m) => (m.id === selectedId ? { ...m, configuration: { ...m.defaults } } : m))
    );
  };

  const save = async () => {
    if (!snapshot || loading || saving) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const next = await saveZCodeModelCatalogEditor({
        models,
        modelOrder,
      });
      setSnapshot(next);
      setModels(next.models);
      setModelOrder(next.modelOrder);
      await onSaved();
      onClose();
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      className="config-dialog codex-catalog-dialog"
      backdropClassName="codex-catalog-backdrop"
      labelledBy="zcode-catalog-title"
      onClose={saving ? undefined : onClose}
      closeDisabled={saving}
      dismissOnBackdrop
    >
      <header className="config-dialog-heading codex-catalog-heading">
        <div>
          <h2 id="zcode-catalog-title">
            <SlidersHorizontal size={18} style={{ display: "inline", verticalAlign: "middle", marginRight: 8 }} />
            {t("agents.zcode.catalog.title")}
          </h2>
          <p>{t("agents.zcode.catalog.subtitle")}</p>
        </div>
        <button type="button" className="icon-button quiet" onClick={onClose} disabled={saving} aria-label={t("common.close")}>
          <X size={18} aria-hidden="true" />
        </button>
      </header>

      <div className="surface-section codex-catalog-body">
        <aside className="codex-catalog-sidebar">
          <div className="codex-catalog-search">
            <Search size={15} aria-hidden />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t("agents.catalog.search")}
            />
          </div>
          <div className="codex-catalog-models" role="listbox">
            {loading ? (
              <div className="codex-catalog-state">
                <LoaderCircle size={18} className="spin" />
                {t("agents.catalog.loading")}
              </div>
            ) : filteredModels.length ? (
              filteredModels.map((model) => (
                <button
                  type="button"
                  key={model.id}
                  className={model.id === selectedId ? "active" : ""}
                  onClick={() => setSelectedId(model.id)}
                >
                  <strong style={{ display: "block", textAlign: "left" }}>{model.id}</strong>
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", width: "100%", gap: "8px", marginTop: "2px" }}>
                    <div style={{ display: "flex", gap: "6px", fontSize: "11px", opacity: 0.8, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                      {model.configuration.contextWindow ? (
                        <span>{`${Math.round(model.configuration.contextWindow / 1024)}K`}</span>
                      ) : null}
                      {model.configuration.supportsImage ? <span>· {t("agents.zcode.catalog.vision")}</span> : null}
                    </div>
                    <div style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                      <Switch
                        size="sm"
                        checked={model.enabled}
                        onChange={(e) => toggleModelEnabled(model.id, e?.target?.checked ?? !model.enabled)}
                        title={t(model.enabled ? "agents.zcode.catalog.enabledInZcode" : "agents.zcode.catalog.hiddenInZcode")}
                      />
                    </div>
                  </div>
                </button>
              ))
            ) : (
              <div className="codex-catalog-state">{t("agents.catalog.empty")}</div>
            )}
          </div>
        </aside>

        <main className="codex-catalog-editor">
          {error ? <MessageNotice tone="error" message={error} /> : null}
          {notice ? <MessageNotice tone="success" message={notice} /> : null}

          {selectedModel ? (
            <div className="codex-catalog-form">
              <div className="codex-catalog-model-heading" style={{ gridColumn: "1 / -1" }}>
                <div>
                  <h3>{selectedModel.id}</h3>
                  <span className="customized">
                    {t("agents.zcode.catalog.managed")}
                  </span>
                </div>
              </div>

              <label>
                <span>{t("agents.zcode.catalog.contextWindow")}</span>
                <input
                  type="number"
                  min={1}
                  step={1024}
                  value={selectedModel.configuration.contextWindow ?? ""}
                  placeholder={t("agents.zcode.catalog.contextWindowPlaceholder")}
                  onChange={(e) => {
                    const val = e.target.value;
                    updateSelectedConfig((prev) => ({
                      ...prev,
                      contextWindow: val ? Number(val) : undefined,
                    }));
                  }}
                />
              </label>

              <label>
                <span>{t("agents.zcode.catalog.maxOutput")}</span>
                <input
                  type="number"
                  min={1}
                  step={1024}
                  value={selectedModel.configuration.maxOutputTokens ?? ""}
                  placeholder={t("agents.zcode.catalog.maxOutputPlaceholder")}
                  onChange={(e) => {
                    const val = e.target.value;
                    updateSelectedConfig((prev) => ({
                      ...prev,
                      maxOutputTokens: val ? Number(val) : undefined,
                    }));
                  }}
                />
              </label>

              <fieldset className="wide codex-catalog-choice-group surface-frame">
                <legend>{t("agents.zcode.catalog.reasoningLevels")}</legend>
                <div className="codex-catalog-chips">
                  {COMMON_REASONING_LEVELS.map((level) => {
                    const activeLevels = selectedModel.configuration.reasoningLevels;
                    const checked = activeLevels.includes(level);
                    return (
                      <label key={level} className={checked ? "selected" : ""}>
                        <Checkbox
                          checked={checked}
                          onChange={() => toggleReasoningLevel(level)}
                        />
                        <span>
                          {checked ? <Check size={13} /> : null}
                          {level}
                        </span>
                      </label>
                    );
                  })}
                </div>
              </fieldset>

              <fieldset className="wide codex-catalog-choice-group surface-frame">
                <legend>{t("agents.zcode.catalog.inputTypes")}</legend>
                <div className="codex-catalog-checkboxes">
                  <label>
                    <Checkbox checked disabled onChange={() => undefined} />
                    <span>{t("agents.zcode.catalog.text")}</span>
                  </label>
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsImage}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsImage;
                        updateSelectedConfig((prev) => ({ ...prev, supportsImage: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.image")}</span>
                  </label>
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsPdf}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsPdf;
                        updateSelectedConfig((prev) => ({ ...prev, supportsPdf: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.pdf")}</span>
                  </label>
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsVideo}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsVideo;
                        updateSelectedConfig((prev) => ({ ...prev, supportsVideo: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.video")}</span>
                  </label>
                </div>
              </fieldset>

              <fieldset className="wide codex-catalog-choice-group surface-frame">
                <legend>{t("agents.zcode.catalog.advanced")}</legend>
                <div className="codex-catalog-checkboxes">
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsJsonSchemaOutput}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsJsonSchemaOutput;
                        updateSelectedConfig((prev) => ({ ...prev, supportsJsonSchemaOutput: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.jsonSchema")}</span>
                  </label>
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsNativeWebSearch}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsNativeWebSearch;
                        updateSelectedConfig((prev) => ({ ...prev, supportsNativeWebSearch: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.webSearch")}</span>
                  </label>
                  <label>
                    <Checkbox
                      checked={selectedModel.configuration.supportsMidConversationSystem}
                      onChange={(e) => {
                        const chk = e?.target?.checked ?? !selectedModel.configuration.supportsMidConversationSystem;
                        updateSelectedConfig((prev) => ({ ...prev, supportsMidConversationSystem: chk }));
                      }}
                    />
                    <span>{t("agents.zcode.catalog.midSystem")}</span>
                  </label>
                </div>
              </fieldset>
            </div>
          ) : (
            <div className="codex-catalog-state">{t("agents.catalog.empty")}</div>
          )}
        </main>
      </div>

      <footer className="dialog-footer codex-catalog-footer" style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <button type="button" className="secondary-button" onClick={restoreSelectedDefaults} disabled={saving || !selectedModel}>
            <RotateCcw size={15} />
            {t("agents.zcode.catalog.restore")}
          </button>
        </div>
        <div className="dialog-footer-actions" style={{ display: "flex", gap: "8px" }}>
          <button type="button" className="secondary-button" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </button>
          <button type="button" className="primary-button" onClick={save} disabled={saving || loading}>
            {saving ? <LoaderCircle size={15} className="spin" /> : null}
            {t("common.save")}
          </button>
        </div>
      </footer>
    </Dialog>
  );
}
