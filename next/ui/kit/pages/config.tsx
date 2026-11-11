import { useEffect, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { ConfigDocument } from "../api/config";
import type { CoreSettings } from "../api/settings";
import type { PageDef } from "../shell";
import { Tabs } from "../tabs";
import { Button, Notice, PageHead, useKit } from "../ui";
import { ConfigDialog } from "./configDialog";
import { ConfigSettings, type SettingsTab, settingsChange } from "./configSettings";
import { ConfigSource } from "./configSource";
import { configText as t } from "./configStrings";

const configTabs = () => [
  { id: "common", label: t("common") },
  { id: "network", label: t("network") },
  { id: "logging", label: t("logging") },
  { id: "advanced", label: t("advanced") },
  { id: "source", label: t("source") },
];

export function ConfigPage() {
  const { api, unauthorized } = useKit();
  const [tab, setTab] = useState("common");
  const [search, setSearch] = useState("");
  const [baseSettings, setBaseSettings] = useState<CoreSettings>();
  const [settings, setSettings] = useState<CoreSettings>();
  const [doc, setDoc] = useState<ConfigDocument>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(true);
  const [sourceError, setSourceError] = useState<string>();
  const [error, setError] = useState<string>();
  const [saved, setSaved] = useState(false);
  const [leave, setLeave] = useState<{ run: () => void }>();
  const dirtySettings =
    !!settings && !!baseSettings && JSON.stringify(settings) !== JSON.stringify(baseSettings);
  const dirty = dirtySettings || (!!doc && doc.content !== text);
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const load = async () => {
    setBusy(true);
    setError(undefined);
    setSaved(false);
    const [s, c] = await Promise.all([api.coreSettings(), api.config()]);
    setBusy(false);
    if (s.status === 401 || c.status === 401) return unauthorized();
    setError(problem(s));
    setSourceError(problem(c));
    if (!c.body) setDoc(undefined);
    if (s.body) {
      setBaseSettings(s.body);
      setSettings(s.body);
    }
    if (c.body) {
      setDoc(c.body);
      setText(c.body.content);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    const beforeUnload = (e: BeforeUnloadEvent) => {
      if (!dirtyRef.current) return;
      e.preventDefault();
      e.returnValue = "";
    };
    const click = (e: MouseEvent) => {
      if (
        !dirtyRef.current ||
        e.defaultPrevented ||
        e.button !== 0 ||
        e.ctrlKey ||
        e.metaKey ||
        e.shiftKey ||
        e.altKey
      )
        return;
      const anchor = (e.target as Element).closest<HTMLAnchorElement>("a[href]");
      if (!anchor || anchor.target === "_blank") return;
      const url = new URL(anchor.href, location.href);
      if (url.href === location.href) return;
      e.preventDefault();
      e.stopPropagation();
      setLeave({ run: () => location.assign(url.href) });
    };
    const hash = (e: HashChangeEvent) => {
      if (!dirtyRef.current) return;
      e.stopImmediatePropagation();
      history.replaceState(null, "", e.oldURL);
      setLeave({ run: () => location.assign(e.newURL) });
    };
    window.addEventListener("beforeunload", beforeUnload);
    document.addEventListener("click", click, true);
    window.addEventListener("hashchange", hash, true);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      document.removeEventListener("click", click, true);
      window.removeEventListener("hashchange", hash, true);
    };
  }, []);
  const switchTab = (next: string) => {
    if (next === tab || busy) return;
    const change = () => {
      setTab(next);
      setSearch("");
      setError(undefined);
      setSaved(false);
    };
    if (dirty && (next === "source" || tab === "source"))
      setLeave({
        run: () => {
          void load();
          change();
        },
      });
    else change();
  };
  const saveSettings = async () => {
    if (!baseSettings || !settings) return;
    setError(undefined);
    setSaved(false);
    let change: ReturnType<typeof settingsChange>;
    try {
      change = settingsChange(baseSettings, settings);
    } catch (error) {
      setError(String(error instanceof Error ? error.message : error));
      return;
    }
    setBusy(true);
    const reply = await api.changeCoreSettings(change);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) {
      setBusy(false);
      return setError(why);
    }
    if (reply.body) {
      setBaseSettings(reply.body);
      setSettings(reply.body);
    }
    const current = await api.config();
    setBusy(false);
    if (current.status === 401) return unauthorized();
    if (current.body) {
      setDoc(current.body);
      setText(current.body.content);
    } else {
      setDoc(undefined);
    }
    setSourceError(problem(current));
    setSaved(true);
  };
  return (
    <>
      <PageHead title={t("title")} />
      {error && <Notice tone="danger">{error}</Notice>}
      {saved && <Notice tone="ok">{t("saved")}</Notice>}
      {!settings ? (
        <Notice tone="muted">{t("loading")}</Notice>
      ) : (
        <Tabs label={t("title")} tabs={configTabs()} active={tab} onChange={switchTab}>
          {tab === "source" ? (
            !doc ? (
              <div class="stack">
                {sourceError && <Notice tone="danger">{sourceError}</Notice>}
                <Notice tone="muted">{busy ? t("loading") : t("sourceUnavailable")}</Notice>
                <Button disabled={busy} onClick={load}>
                  {t("reload")}
                </Button>
              </div>
            ) : (
              <ConfigSource
                loading={busy}
                setBusy={setBusy}
                base={doc}
                text={text}
                setText={setText}
                reload={load}
                saved={(current) => {
                  setDoc(current);
                  setText(current.content);
                  api.coreSettings().then((reply) => {
                    if (reply.status === 401) return unauthorized();
                    if (reply.body) {
                      setSettings(reply.body);
                      setBaseSettings(reply.body);
                    } else setError(problem(reply));
                  });
                }}
              />
            )
          ) : (
            <div class="stack">
              <label class="field" data-part="field">
                <input
                  type="search"
                  aria-label={t("searchSettings")}
                  placeholder={t("searchSettings")}
                  value={search}
                  onInput={(e) => setSearch(e.currentTarget.value)}
                />
              </label>
              <ConfigSettings
                tab={tab as SettingsTab}
                search={search}
                value={settings}
                onChange={(v) => {
                  setSettings(v);
                  setSaved(false);
                }}
                dirty={dirtySettings}
                busy={busy}
                save={saveSettings}
                discard={() => {
                  setSettings(baseSettings);
                  setError(undefined);
                  setSaved(false);
                }}
              />
            </div>
          )}
        </Tabs>
      )}
      {leave && (
        <ConfigDialog title={t("unsaved")} close={() => setLeave(undefined)}>
          <p>{t("leaveMessage")}</p>
          <div class="actions">
            <Button
              variant="danger"
              onClick={() => {
                setSettings(baseSettings);
                setText(doc?.content ?? "");
                dirtyRef.current = false;
                const run = leave.run;
                setLeave(undefined);
                run();
              }}
            >
              {t("leave")}
            </Button>
            <Button onClick={() => setLeave(undefined)}>{t("stay")}</Button>
          </div>
        </ConfigDialog>
      )}
    </>
  );
}
export const configPage = (): PageDef => ({
  id: "config",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <ConfigPage />,
});
