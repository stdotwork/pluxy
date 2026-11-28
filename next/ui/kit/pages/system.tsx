// System: which pluxy this is, the models its tools can ask for, and the address each kind of
// program needs.

import type { ServedModel } from "../api";
import { Phases, providerName, StatePane } from "../parts";
import { useAccountLabels, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Button, Facts, List, Notice, PageHead, useKit, useLoad, when } from "../ui";
import { CopyButton } from "./copyButton";
import { SettingGroup, SettingsColumn } from "./settingGroup";

const t = strings("system", {
  title: "System",
  tip: "Version, models and addresses",
  safeMode:
    "pluxy was started in safe mode, so no extension runs and every request fails. Start pluxy again normally to leave it.",
  about: "About",
  version: "Version",
  built: "Built",
  address: "Address",
  settingsFile: "Settings file",
  inUse: "pluxy is using it as it is saved.",
  notInUse:
    "pluxy is not using the latest version of its settings file: {error}. It keeps working with the version before.",
  unknown: "Unknown",
  loadingAbout: "Loading version…",
  aboutFailed: "Could not read pluxy's version",
  aboutFailedHelp: "Check that pluxy is still running, then try again.",
  models: "Models your tools can ask for",
  modelCount: { one: "{count} model", other: "{count} models" },
  loadingModels: "Loading models…",
  modelsFailed: "Could not read the models",
  modelsFailedHelp: "Check that pluxy is still running, then try again.",
  noModels: "No models yet",
  noModelsText: "Models appear once an account is added and its provider's extension runs.",
  openAccounts: "Open Accounts",
  openExtensions: "Open Extensions",
  model: "Model",
  alsoCalled: "Also called",
  accounts: "Accounts",
  copy: "Copy",
  copyModel: "Copy {model}",
  copyUrl: "Copy {url}",
  refresh: "Refresh",
  addresses: "Addresses for other programs",
  addressesText:
    "Coding tools are set up in {tools}. Other programs take the base URL for the API they speak and a key from Tools.",
  tools: "Tools",
  speaks: "What it speaks",
  baseUrl: "Base URL",
  openai: "OpenAI API",
  claude: "Anthropic (Claude) API",
  gemini: "Gemini API",
  codexKey: "Codex with an API key",
  codexChatgpt: "Codex signed in with ChatGPT",
  noAddress: "This page cannot tell pluxy's address. Tools shows the address pluxy listens on.",
});

/** One model row: a model as one entry of an extension, with the other names it is served under. */
interface ModelRow {
  id: string;
  aliases: string[];
  accounts: string[];
}

/** The served names grouped by extension, those with the same upstream name folded into one row. */
export function groupModels(models: ServedModel[]): { extension: string; rows: ModelRow[] }[] {
  const groups = new Map<string, Map<string, ServedModel[]>>();
  for (const m of models) {
    const byUpstream = groups.get(m.extension) ?? new Map<string, ServedModel[]>();
    byUpstream.set(m.upstream, [...(byUpstream.get(m.upstream) ?? []), m]);
    groups.set(m.extension, byUpstream);
  }
  return [...groups]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([extension, byUpstream]) => ({
      extension,
      rows: [...byUpstream].flatMap(([upstream, names]) => {
        const main = names.find((n) => n.id === upstream) ?? names[0];
        if (!main) return [];
        return {
          id: main.id,
          aliases: names.filter((n) => n !== main).map((n) => n.id),
          accounts: [...new Set(names.flatMap((n) => n.accounts))],
        };
      }),
    }));
}

interface ProgramAddress {
  id: string;
  name: string;
  url: string;
}

/** The base URL each kind of program takes, by the API it speaks. */
export function programAddresses(origin: string): ProgramAddress[] {
  return [
    { id: "openai", name: t("openai"), url: `${origin}/v1` },
    { id: "claude", name: t("claude"), url: origin },
    { id: "gemini", name: t("gemini"), url: origin },
    { id: "codex-key", name: t("codexKey"), url: `${origin}/v1` },
    { id: "codex-chatgpt", name: t("codexChatgpt"), url: `${origin}/backend-api/codex` },
  ];
}

export function SystemPage() {
  const { api, host } = useKit();
  const privacy = usePrivacy();
  const status = useLoad(api.status);
  const models = useLoad(api.servedModels);
  const mask = privacy.masker(useAccountLabels(models.data));
  const s = status.data;
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button
            disabled={status.loading || models.loading}
            onClick={() => {
              status.reload();
              models.reload();
            }}
          >
            {t("refresh")}
          </Button>
        }
      />
      <SettingsColumn>
        {s?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
        <SettingGroup title={t("about")}>
          <Phases
            load={status}
            loading={t("loadingAbout")}
            failed={{ title: t("aboutFailed"), help: t("aboutFailedHelp") }}
            isEmpty={() => false}
            empty={null}
          >
            {(d) => (
              <div class="system-facts">
                <Facts
                  rows={[
                    [
                      t("version"),
                      <>
                        <span class="mono">{d.version}</span>
                        {d.commit && d.commit !== "unknown" && (
                          <>
                            {" "}
                            <span class="mono muted">{d.commit.slice(0, 12)}</span>
                          </>
                        )}
                      </>,
                    ],
                    [t("built"), when(d.built, "exact") ?? t("unknown")],
                    [
                      t("address"),
                      host.address && (
                        <span class="system-address">
                          <span class="mono">{host.address}</span>
                          <CopyButton value={host.address} name={t("copyUrl", { url: host.address })} />
                        </span>
                      ),
                    ],
                    [
                      t("settingsFile"),
                      d.desired.error ? mask(t("notInUse", { error: d.desired.error })) : t("inUse"),
                    ],
                  ]}
                />
              </div>
            )}
          </Phases>
        </SettingGroup>
        <SettingGroup
          title={t("models")}
          actions={
            models.data && models.data.models.length > 0 ? (
              <span class="muted">{t("modelCount", { count: models.data.models.length })}</span>
            ) : undefined
          }
        >
          <Phases
            load={models}
            loading={t("loadingModels")}
            failed={{ title: t("modelsFailed"), help: t("modelsFailedHelp") }}
            isEmpty={(d) => d.models.length === 0}
            empty={
              <StatePane
                kind="empty"
                title={t("noModels")}
                primary={{ label: t("openAccounts"), href: "#/accounts" }}
                secondary={{ label: t("openExtensions"), href: "#/extensions" }}
              >
                {t("noModelsText")}
              </StatePane>
            }
          >
            {(d) =>
              groupModels(d.models).map((g) => (
                <section
                  key={g.extension}
                  class="system-group setting-piece"
                  data-part="model-group"
                  aria-label={providerName(g.extension)}
                >
                  <h3>
                    <span>{providerName(g.extension)}</span>
                    <span class="system-group-id mono">{g.extension}</span>
                    <span class="system-group-count">{t("modelCount", { count: g.rows.length })}</span>
                  </h3>
                  <List<ModelRow>
                    label={providerName(g.extension)}
                    items={g.rows}
                    id={(r) => r.id}
                    columns={[
                      {
                        label: t("model"),
                        value: (r) => (
                          <span class="system-model">
                            <span class="mono">{r.id}</span>
                            <CopyButton value={r.id} name={t("copyModel", { model: r.id })} />
                          </span>
                        ),
                      },
                      { label: t("alsoCalled"), value: (r) => r.aliases.join(", "), mono: true },
                      { label: t("accounts"), value: (r) => r.accounts.map(mask).join(", "), mono: true },
                    ]}
                  />
                </section>
              ))
            }
          </Phases>
        </SettingGroup>
        <SettingGroup
          title={t("addresses")}
          about={rich(t("addressesText"), { tools: <a href="#/tools">{t("tools")}</a> })}
        >
          {host.address ? (
            <List<ProgramAddress>
              label={t("addresses")}
              items={programAddresses(host.address)}
              id={(a) => a.id}
              columns={[
                { label: t("speaks"), value: (a) => a.name },
                {
                  label: t("baseUrl"),
                  value: (a) => (
                    <span class="system-url mono" title={a.url}>
                      {a.url}
                    </span>
                  ),
                },
                {
                  label: t("copy"),
                  value: (a) => <CopyButton value={a.url} name={t("copyUrl", { url: a.url })} />,
                },
              ]}
            />
          ) : (
            <Notice tone="muted">{t("noAddress")}</Notice>
          )}
        </SettingGroup>
      </SettingsColumn>
    </>
  );
}

export const systemPage = (): PageDef => ({
  id: "system",
  dest: "settings",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <SystemPage />,
});
