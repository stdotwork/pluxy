// Beginner Mode: the guided setup, in the old app's two steps. Connect a model is the kit's own Add
// Account (every sign-in method core's extensions offer, browser, device code or API key);
// Connect an agent lists the agent clients this window can set up whose optional tool is
// installed on this device or offered by a registry, installs and enables that tool, and then
// shows the client's own page, where the person applies the configuration and starts the client.
// The guide writes nothing itself and holds no draft: every write is the client page's, after
// its native confirmation, and every native confirmation is answered in its own dialog, never by
// the guide. A client joins the guide by being an agent page (`AgentPage`): a window page that
// names the package it drives.

import { useState } from "preact/hooks";
import { AddAccountPage } from "../../ui/kit/pages/addAccount";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import { Button, Card, Empty, Notice, PageHead, useAction, useKit, useLoad } from "../../ui/kit/ui";
import { type DeviceCatalog, device } from "./device";
import { type Item, items } from "./easyModeItems";

/** An agent client's setup page in the window, and the optional tool it drives. */
export type AgentPage = PageDef & { package: string };

/** The launcher every client's Start CLI runs through. */
export const LAUNCHER = "pluxy/execution";

const t = strings("easyMode", {
  title: "Beginner Mode",
  short: "Easy",
  tip: "Guided model and agent setup",
  intro: "Connect a model, then connect an agent. You can repeat either step later from its own page.",
  stepsLabel: "Connection wizard steps",
  current: "Step {current} of {total}",
  step1: "Connect a model",
  step1Description: "Sign in or add an API key",
  step2: "Connect an agent",
  step2Description: "Select a client and model, then apply the configuration",
  providerDescription: "Sign in with your account or enter an API key. Choose a method below to begin.",
  connected: {
    one: "{count} connected source detected",
    other: "{count} connected sources detected",
  },
  none: "No source connected yet. Sign in or add an API key first.",
  next: "Next: connect an agent",
  back: "Previous",
  agentDescription: "Select a client, install and enable its tool, then set up its models and start it.",
  clients: "Agent clients",
  noClients: "No agent client this app can set up is installed or offered by a registry on this device.",
  launcher: "Launcher",
  launcherHint: "Client pages use this tool to launch clients.",
  noLauncher:
    "The launcher (pluxy/execution) is not installed or offered by a registry, so clients cannot launch from here.",
  fullAccess:
    "A native tool has full access to your user account. Enabling one opens a native confirmation that shows its source, digest and signer; answer it there.",
  offered: "Not installed",
  installed: "Installed, not enabled",
  enabled: "Enabled",
  install: "Install",
  enable: "Enable",
  waiting: "Waiting for the native confirmation…",
  setUp: "Set up",
  registryError: "Registry {name}: {error}",
  safeMode: "Extensions are in safe mode, so native tools are off. Restart the app without --safe-mode.",
});

/** A package's row: its state, and the one action that moves it on. */
function PackageRow(props: {
  item: Item;
  title: string;
  onChanged: () => void;
  onSetUp?: () => void;
  selected?: boolean;
  /** Safe mode: no native tool runs, so Enable and Set up stay off. */
  safe?: boolean;
}) {
  const { item } = props;
  const act = useAction(props.onChanged);
  const [enabling, setEnabling] = useState(false);
  const enable = () =>
    act.run(async () => {
      setEnabling(true);
      try {
        return await device.setEnabled(item.id, true);
      } finally {
        setEnabling(false);
      }
    });
  return (
    <li class="stack" data-part="easy-package" data-package={item.id} data-state={item.state}>
      <div class="actions">
        <strong>{props.title}</strong>
        <span class="mono muted">{item.id}</span>
        <span class="muted">{t(item.state)}</span>
      </div>
      {act.error && <Notice tone="danger">{act.error}</Notice>}
      {enabling && <Notice tone="muted">{t("waiting")}</Notice>}
      <div class="actions">
        {item.state === "offered" && (
          <Button
            variant="primary"
            disabled={act.busy}
            onClick={() => act.run(() => device.install(item.registry ?? "", item.id, item.version))}
          >
            {t("install")}
          </Button>
        )}
        {item.state === "installed" && (
          <Button variant="primary" disabled={act.busy || props.safe} onClick={enable}>
            {t("enable")}
          </Button>
        )}
        {props.onSetUp && (
          <Button
            variant={props.selected ? "quiet" : "primary"}
            disabled={act.busy || props.safe || item.state !== "enabled"}
            onClick={props.onSetUp}
          >
            {t("setUp")}
          </Button>
        )}
      </div>
    </li>
  );
}

function ConnectAgent(props: { agents: AgentPage[]; catalog?: DeviceCatalog; onChanged: () => void }) {
  const registries = useLoad(device.registries);
  const [chosen, setChosen] = useState<string>();
  const changed = () => {
    registries.reload();
    props.onChanged();
  };
  const clients = items(
    props.agents.map((a) => a.package),
    props.catalog,
    registries.data,
  );
  const [launcher] = items([LAUNCHER], props.catalog, registries.data);
  const page = props.agents.find((a) => a.id === chosen);
  const safe = props.catalog?.safe_mode === true;
  const ready = !safe && clients.find((c) => c.id === page?.package)?.state === "enabled";
  const broken = (registries.data ?? []).filter((r) => r.error);
  return (
    <>
      <p class="muted">{t("agentDescription")}</p>
      {props.catalog?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
      {registries.error && <Notice tone="warn">{registries.error}</Notice>}
      {broken.map((r) => (
        <Notice key={r.name} tone="warn">
          {t("registryError", { name: r.name, error: r.error ?? "" })}
        </Notice>
      ))}
      <Notice tone="warn">{t("fullAccess")}</Notice>
      <Card title={t("clients")}>
        {clients.length === 0 ? (
          <Empty>{registries.loading ? "…" : t("noClients")}</Empty>
        ) : (
          <ul class="stack" data-part="easy-clients">
            {clients.map((c) => {
              const a = props.agents.find((x) => x.package === c.id);
              return (
                <PackageRow
                  key={c.id}
                  item={c}
                  title={a?.title ?? c.id}
                  onChanged={changed}
                  selected={a?.id === chosen}
                  onSetUp={() => setChosen(a?.id)}
                  safe={safe}
                />
              );
            })}
          </ul>
        )}
      </Card>
      <Card title={t("launcher")}>
        <p class="muted">{t("launcherHint")}</p>
        {launcher ? (
          <ul class="stack">
            <PackageRow item={launcher} title={t("launcher")} onChanged={changed} safe={safe} />
          </ul>
        ) : (
          <Notice tone="warn">{t("noLauncher")}</Notice>
        )}
      </Card>
      {page && ready && (
        <section class="stack" data-part="easy-client-page">
          {page.render()}
        </section>
      )}
    </>
  );
}

export function EasyModePage(props: { agents: AgentPage[]; catalog?: DeviceCatalog; onChanged: () => void }) {
  const { api } = useKit();
  const accounts = useLoad(api.accounts);
  const [step, setStep] = useState<1 | 2>(1);
  const count = accounts.data?.accounts.length ?? 0;
  const steps = [
    { n: 1 as const, title: t("step1"), description: t("step1Description") },
    { n: 2 as const, title: t("step2"), description: t("step2Description") },
  ];
  return (
    <div class="stack" data-part="easy-mode">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      <nav aria-label={t("stepsLabel")} data-part="easy-steps">
        <p class="muted">{t("current", { current: step, total: steps.length })}</p>
        <div class="actions">
          {steps.map((s) => (
            <Button
              key={s.n}
              variant={s.n === step ? "primary" : "quiet"}
              disabled={s.n === 2 && count === 0}
              onClick={() => setStep(s.n)}
            >
              {`${s.n}. ${s.title}`}
            </Button>
          ))}
        </div>
        <p class="muted">{steps[step - 1]?.description}</p>
      </nav>
      {step === 1 ? (
        <section class="stack" data-part="easy-step" data-step="1">
          <h2>{t("step1")}</h2>
          <p class="muted">{t("providerDescription")}</p>
          <AddAccountPage onSuccess={accounts.reload} />
          <Card>
            <div class="actions" data-part="easy-sources">
              {accounts.error && <Notice tone="danger">{accounts.error}</Notice>}
              <span>{count > 0 ? t("connected", { count }) : t("none")}</span>
              <Button variant="primary" disabled={count === 0} onClick={() => setStep(2)}>
                {t("next")}
              </Button>
            </div>
          </Card>
        </section>
      ) : (
        <section class="stack" data-part="easy-step" data-step="2">
          <h2>{t("step2")}</h2>
          <ConnectAgent agents={props.agents} catalog={props.catalog} onChanged={props.onChanged} />
          <div class="actions">
            <Button onClick={() => setStep(1)}>{t("back")}</Button>
          </div>
        </section>
      )}
    </div>
  );
}

export const easyModePage = (
  agents: AgentPage[],
  catalog: DeviceCatalog | undefined,
  onChanged: () => void,
): PageDef => ({
  id: "easy-mode",
  group: "operate",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <EasyModePage agents={agents} catalog={catalog} onChanged={onChanged} />,
});
