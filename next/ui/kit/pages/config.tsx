// The Config page: two tabs, Settings (rows that save the moment they change) and Settings file
// (the file as text, saved after a review). Each tab reads what it needs by itself, so neither
// waits for the other, and a settings file that does not parse is said once, on both tabs.

import { useEffect, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { ConfigDocument } from "../api/config";
import type { CoreSettings } from "../api/settings";
import { StaleNotice, StatePane } from "../parts";
import { usePrivacy } from "../privacy";
import { guardLeave, type PageDef } from "../shell";
import { Tabs } from "../tabs";
import { Button, Notice, PageHead, useKit } from "../ui";
import { ConfigDialog } from "./configDialog";
import { useReplaceKey } from "./configKeys";
import { type ChangeSetting, ConfigSettings } from "./configSettings";
import { ConfigSource } from "./configSource";
import {
  type ConfigSection,
  type ConfigTab,
  configAddress,
  configTabIds,
  parseConfigAddress,
} from "./configSource.helpers";
import { fileError, configText as t } from "./configStrings";
import { nest, settingAt } from "./routing.rules";
import { SettingsColumn, SettingsSearchScope } from "./settingGroup";

/** A waiting or failed pane of a tab, in the card its rows would be in. */
function PaneCard(props: Parameters<typeof StatePane>[0]) {
  return (
    <div class="setting-group-card" data-part="setting-card">
      <StatePane {...props} />
    </div>
  );
}

/** core's settings as read: loading, read (at is when), or failed (over what was read before, if anything). */
type SettingsLoad =
  | { phase: "loading" }
  | { phase: "ready"; data: CoreSettings; at: number }
  | { phase: "failed"; error: string; data?: CoreSettings; at?: number };

/** The settings file as read: loading, read, does not parse (broken), or could not be read. */
type FileLoad =
  | { phase: "loading" }
  | { phase: "ready"; doc: ConfigDocument }
  | { phase: "broken"; error: string }
  | { phase: "failed"; error: string };

/** settings with the setting at key (a dotted path) set to value; the rest is shared, not copied. */
function withSetting(settings: CoreSettings, key: string, value: unknown): CoreSettings {
  const [first, ...rest] = key.split(".");
  const at = settings as unknown as Record<string, unknown>;
  const inner = rest.length
    ? withSetting((at[first as string] ?? {}) as CoreSettings, rest.join("."), value)
    : value;
  return { ...at, [first as string]: inner } as unknown as CoreSettings;
}

export function ConfigPage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  // The tab, the section and the account the file opens at are in the page's address.
  const [opened] = useState(() => parseConfigAddress(location.hash));
  const [tab, setTab] = useState<ConfigTab>(opened?.tab ?? "settings");
  const [account, setAccount] = useState(opened?.account);
  const [scrollTo, setScrollTo] = useState<{ section?: ConfigSection; n: number }>({
    section: opened?.section,
    n: 0,
  });
  const [settings, setSettings] = useState<SettingsLoad>({ phase: "loading" });
  const [file, setFile] = useState<FileLoad>({ phase: "loading" });
  const [text, setText] = useState("");
  const [notInUse, setNotInUse] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [leave, setLeave] = useState<{ run: () => void }>();
  const doc = file.phase === "ready" ? file.doc : undefined;
  const dirty = !!doc && doc.content !== text;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const live = useRef(true);
  useEffect(
    () => () => {
      live.current = false;
    },
    [],
  );

  // Every read and save of the settings in the order sent. A reply to a read sent before a save
  // would put the saved row back to what it was, so it is not taken.
  const sent = useRef({ requests: 0, lastSave: 0 });
  const loadSettings = async () => {
    setSettings((s) => (s.phase === "failed" && !s.data ? { phase: "loading" } : s));
    const n = ++sent.current.requests;
    const r = await api.coreSettings();
    if (!live.current) return;
    if (r.status === 401) return unauthorized();
    if (sent.current.lastSave > n) return;
    const why = problem(r);
    if (why === undefined && r.body) return setSettings({ phase: "ready", data: r.body, at: Date.now() });
    setSettings((s) => ({
      phase: "failed",
      error: why ?? "",
      data: s.phase === "loading" ? undefined : s.data,
      at: s.phase === "loading" ? undefined : s.at,
    }));
  };
  /**
   * Reads the file again. keepEdits: the editor's edits stay; while it has some, only a file that
   * no longer parses changes the tab (the text stays in the page, the editor hidden).
   */
  const loadFile = async (keepEdits = false) => {
    if (!keepEdits) setFile({ phase: "loading" });
    const r = await api.config();
    if (!live.current) return;
    if (r.status === 401) return unauthorized();
    if (r.status === 422 || r.error?.code === "invalid_config") {
      setFile({ phase: "broken", error: fileError(r.error?.message ?? "") });
    } else if (keepEdits && dirtyRef.current) {
      return;
    } else if (r.status >= 200 && r.status < 300 && r.body) {
      setFile({ phase: "ready", doc: r.body });
      setText(r.body.content);
    } else {
      setFile({ phase: "failed", error: problem(r) ?? "" });
    }
  };
  /** Whether pluxy runs an older settings file than the newest, because it refused the newest. */
  const loadStatus = async () => {
    const r = await api.status();
    if (!live.current || !r.body) return;
    const { desired, active } = r.body;
    setNotInUse(desired.revision !== active.revision && desired.error ? desired.error : undefined);
  };
  const loadAll = () => Promise.all([loadSettings(), loadFile(), loadStatus()]);
  /**
   * After a write core refused with 422: the file may no longer parse. Reads it and the status
   * again, keeping the editor's text, so a broken file puts the page in its broken state.
   */
  const refused = () => {
    void loadFile(true);
    void loadStatus();
  };
  /** Saves one setting. Only that setting is taken from the reply, so replies coming back in another order than sent cannot put another row back. */
  const change: ChangeSetting = async (key, value) => {
    sent.current.lastSave = ++sent.current.requests;
    const reply = await api.changeCoreSettings(nest(key, value));
    if (!live.current) return reply;
    if (reply.status >= 200 && reply.status < 300 && reply.body) {
      const held = settingAt(reply.body, key);
      // A new listen address waits for a restart: the reply says which address waits.
      const waiting = reply.body.listen_after_restart;
      setSettings((s) =>
        s.phase === "loading" || !s.data
          ? s
          : {
              phase: "ready",
              data: {
                ...withSetting(s.data, key, held),
                ...(key === "listen" ? { listen_after_restart: waiting } : {}),
              },
              at: Date.now(),
            },
      );
      void loadFile(true);
      void loadStatus();
    } else if (reply.status === 422) refused();
    return reply;
  };
  useEffect(() => {
    void loadAll();
  }, []);

  // An address with a section scrolls the Settings tab to it, once its rows are there.
  const settingsShown = settings.phase !== "loading" && (settings.phase === "ready" || !!settings.data);
  useEffect(() => {
    if (tab !== "settings" || !scrollTo.section || !settingsShown) return;
    document.getElementById?.(scrollTo.section)?.scrollIntoView?.({ block: "start" });
  }, [scrollTo, tab, settingsShown]);

  const keys = useReplaceKey({ replaced: () => void loadFile(true), refused });
  // Leaving Config: a new key that was shown goes; one whose reply comes later waits for the next visit.
  const left = useRef(keys.left);
  left.current = keys.left;
  useEffect(() => () => left.current(), []);

  const switchRef = useRef<(tab: ConfigTab, account?: string, section?: ConfigSection) => boolean>(
    () => false,
  );
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
    window.addEventListener("beforeunload", beforeUnload);
    document.addEventListener("click", click, true);
    // Back and a typed #/… address change the hash; the shell asks its guards before it follows.
    const unguard = guardLeave((go) => {
      // An address of this page moves between its tabs, which is not leaving it. Staying is
      // answered when the tab did not change: the page asks first, or is busy.
      const to = parseConfigAddress(location.hash);
      if (to) return !switchRef.current(to.tab, to.account, to.section);
      if (!dirtyRef.current) return false;
      setLeave({ run: go });
      return true;
    });
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      document.removeEventListener("click", click, true);
      unguard();
    };
  }, []);

  /** Shows a tab. False when it did not, or not yet: it is asking first, or the file is busy. */
  const switchTab = (next: ConfigTab, nextAccount?: string, section?: ConfigSection): boolean => {
    if (busy) return false;
    const change = () => {
      if (next !== "settings") keys.left();
      setTab(next);
      setAccount(nextAccount);
      setScrollTo((s) => ({ section, n: s.n + 1 }));
      // The shell is not asked: a replaced address is no change of the hash.
      history.replaceState(null, "", configAddress(next, nextAccount));
    };
    if (next !== tab && dirty) {
      setLeave({
        run: () => {
          setText(doc?.content ?? "");
          change();
        },
      });
      return false;
    }
    change();
    return true;
  };
  switchRef.current = switchTab;

  const tryAgain = () => void loadAll();
  const disabled = file.phase === "broken" ? t("fixFileFirst") : undefined;
  return (
    <>
      <PageHead title={t("title")} />
      <SettingsColumn class="config-page">
        {file.phase === "broken" && (
          <Notice tone="danger">
            {t("fileBroken", { error: p.text(file.error) })}{" "}
            <Button onClick={tryAgain}>{t("tryAgain")}</Button>
          </Notice>
        )}
        {notInUse && (
          <Notice tone="warn">{t("notInUse", { error: p.text(notInUse.replace(/\.$/, "")) })}</Notice>
        )}
        <Tabs
          label={t("title")}
          tabs={configTabIds.map((id) => ({ id, label: t(id) }))}
          active={tab}
          onChange={(next) => {
            if (next !== tab) switchTab(next as ConfigTab);
          }}
        >
          {tab === "settings" ? (
            settings.phase === "loading" ? (
              <PaneCard kind="loading" title={t("loadingSettings")} />
            ) : !settings.data ? (
              <PaneCard
                kind="failed"
                title={t("settingsFailed")}
                details={settings.phase === "failed" ? settings.error : undefined}
                primary={{ label: t("tryAgain"), onClick: tryAgain }}
              />
            ) : (
              <>
                {settings.phase === "failed" && (
                  <StaleNotice at={settings.at} error={settings.error} onRetry={tryAgain} />
                )}
                <SettingsSearchScope>
                  <ConfigSettings
                    settings={settings.data}
                    format={doc?.format}
                    disabled={disabled}
                    keys={keys}
                    change={change}
                  />
                </SettingsSearchScope>
              </>
            )
          ) : file.phase === "loading" ? (
            <PaneCard kind="loading" title={t("loadingFile")} />
          ) : file.phase === "failed" ? (
            <PaneCard
              kind="failed"
              title={t("fileFailed")}
              details={file.error}
              primary={{ label: t("tryAgain"), onClick: () => void loadFile() }}
            />
          ) : file.phase === "ready" ? (
            <ConfigSource
              busy={busy}
              setBusy={setBusy}
              base={file.doc}
              account={account}
              text={text}
              setText={setText}
              reload={() => {
                void loadFile();
                void loadStatus();
              }}
              refused={refused}
              saved={(current) => {
                setFile({ phase: "ready", doc: current });
                setText(current.content);
                void loadSettings();
                void loadStatus();
              }}
            />
          ) : null}
        </Tabs>
        {leave && (
          <ConfigDialog title={t("leaveQuestion")} close={() => setLeave(undefined)}>
            <div class="actions">
              <Button
                variant="danger"
                onClick={() => {
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
      </SettingsColumn>
    </>
  );
}
export const configPage = (): PageDef => ({
  id: "config",
  dest: "settings",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <ConfigPage />,
});
