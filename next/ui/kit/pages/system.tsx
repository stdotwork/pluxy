import type { ComponentChildren } from "preact";
import type { ServedModel } from "../api";
import { useAccountLabels, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Button, Card, Empty, List, Notice, PageHead, useKit, useLoad, when } from "../ui";

const t = strings("system", {
  title: "System",
  short: "System",
  tip: "Runtime & diagnostics",
  about: "About",
  version: "Core version",
  commit: "Commit {commit}",
  buildTime: "Build Time",
  unknown: "Unknown",
  connection: "Connection Status:",
  connecting: "Connecting",
  connected: "Connected",
  disconnected: "Disconnected",
  revision: "Config revision",
  safeMode: "Safe mode",
  on: "On",
  off: "Off",
  safeModeNotice: "Core runs in safe mode: no extension runs, so client requests fail.",
  models: "Available Models",
  refresh: "Refresh",
  loadingModels: "Loading available models...",
  modelsEmpty: "No models are served.",
  modelsCount: { one: "{count} available model", other: "{count} available models" },
  groupCount: { one: "{count} model", other: "{count} models" },
  model: "Model",
  aliases: "Aliases",
  accounts: "Accounts",
  connect: "Connect a client",
  connectHint:
    "Point a client at one of these addresses and sign in with a client API key. Create keys in the {config}.",
  configLink: "Config Panel",
  noAddress: "This page cannot tell which address clients reach core at; use the address core listens on.",
  client: "Client",
  baseUrl: "Base URL",
  usedBy: "Used by",
  openai: "OpenAI",
  openaiUse: "OpenAI SDKs and OpenAI-compatible tools",
  claude: "Claude",
  claudeUse: "Claude Code and the Anthropic SDKs",
  gemini: "Gemini",
  geminiUse: "The Gemini SDKs and the Gemini CLI",
  codex: "Codex",
  codexUse: "The Codex CLI's base_url; a Codex signed in with ChatGPT takes {chatgpt} as chatgpt_base_url",
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

function Tile(props: { label: string; value: ComponentChildren; sub?: ComponentChildren }) {
  return (
    <div class="system-tile" data-part="tile">
      <div class="system-tile-label">{props.label}</div>
      <div class="system-tile-value">{props.value}</div>
      {props.sub && <div class="system-tile-sub mono">{props.sub}</div>}
    </div>
  );
}

interface ClientAddress {
  id: string;
  name: string;
  url: string;
  use: string;
}

function clientAddresses(origin: string): ClientAddress[] {
  return [
    { id: "openai", name: t("openai"), url: `${origin}/v1`, use: t("openaiUse") },
    { id: "claude", name: t("claude"), url: origin, use: t("claudeUse") },
    { id: "gemini", name: t("gemini"), url: origin, use: t("geminiUse") },
    {
      id: "codex",
      name: t("codex"),
      url: `${origin}/v1`,
      use: t("codexUse", { chatgpt: `${origin}/backend-api/codex` }),
    },
  ];
}

export function SystemPage() {
  const { api, host } = useKit();
  const privacy = usePrivacy();
  const mask = privacy.masker(useAccountLabels());
  const status = useLoad(api.status);
  const models = useLoad(api.servedModels);
  const s = status.data;
  const connection = status.loading && !s ? t("connecting") : s ? t("connected") : t("disconnected");
  const list = models.data?.models;
  const groups = list ? groupModels(list) : [];

  return (
    <>
      <PageHead title={t("title")} />
      {s?.safe_mode && <Notice tone="warn">{t("safeModeNotice")}</Notice>}
      <div class="stack">
        <Card title={t("about")}>
          <div class="system-tiles">
            <Tile
              label={t("version")}
              value={s?.version ?? t("unknown")}
              sub={s && s.commit !== "unknown" ? t("commit", { commit: s.commit.slice(0, 12) }) : undefined}
            />
            <Tile label={t("buildTime")} value={s ? (when(s.built) ?? t("unknown")) : t("unknown")} />
            <Tile label={t("connection")} value={connection} sub={host.address} />
            <Tile
              label={t("revision")}
              value={s ? <span class="mono">{s.active.revision}</span> : t("unknown")}
            />
            <Tile label={t("safeMode")} value={s ? (s.safe_mode ? t("on") : t("off")) : t("unknown")} />
          </div>
        </Card>
        <Card
          title={t("models")}
          actions={
            <Button onClick={models.reload} disabled={models.loading}>
              {t("refresh")}
            </Button>
          }
        >
          {models.error && <Notice tone="danger">{mask(models.error)}</Notice>}
          {models.loading && !list && <Empty>{t("loadingModels")}</Empty>}
          {list && list.length === 0 && <Empty>{t("modelsEmpty")}</Empty>}
          {list && list.length > 0 && (
            <Notice tone="muted">{t("modelsCount", { count: list.length })}</Notice>
          )}
          {groups.map((g) => (
            <section key={g.extension} class="system-group" data-part="model-group" aria-label={g.extension}>
              <h3>
                <span class="mono">{g.extension}</span>
                <span class="system-group-count">{t("groupCount", { count: g.rows.length })}</span>
              </h3>
              <List<ModelRow>
                label={g.extension}
                items={g.rows}
                id={(r) => r.id}
                columns={[
                  { label: t("model"), value: (r) => r.id, mono: true },
                  { label: t("aliases"), value: (r) => r.aliases.join(", "), mono: true },
                  { label: t("accounts"), value: (r) => r.accounts.map(mask).join(", "), mono: true },
                ]}
              />
            </section>
          ))}
        </Card>
        <Card title={t("connect")}>
          <p class="system-hint">
            {rich(t("connectHint"), { config: <a href="#/config">{t("configLink")}</a> })}
          </p>
          {host.address ? (
            <List<ClientAddress>
              label={t("connect")}
              items={clientAddresses(host.address)}
              id={(c) => c.id}
              columns={[
                { label: t("client"), value: (c) => c.name },
                { label: t("baseUrl"), value: (c) => c.url, mono: true },
                { label: t("usedBy"), value: (c) => c.use },
              ]}
            />
          ) : (
            <Notice tone="muted">{t("noAddress")}</Notice>
          )}
        </Card>
      </div>
    </>
  );
}

export const systemPage = (): PageDef => ({
  id: "system",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <SystemPage />,
});
