// Setup: what a new install shows first. One page, read from top to bottom: what core found on
// its machine, the providers it offers, Start, an account, the clients, Finish. Each part shows
// its state, and a part whose work is done says so in one line. Opened again later, it is a
// status of the same things.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { problem, type Setup, type SetupClient, type SetupPick } from "../api";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Badge, Button, Card, Empty, Notice, PageHead, useAction, useKit, useLoad, when } from "../ui";
import { AddAccountPage } from "./addAccount";
import { KeyNotice } from "./configKeys";
import {
  type Choices,
  hasProvider,
  isInstalled,
  isTicked,
  onThisMachine,
  providerPicks,
  type Results,
  scanState,
  signInsToName,
  splitClients,
  start,
  toInstall,
  withKey,
} from "./setup.helpers";

const t = strings("setup", {
  title: "Set up pluxy",
  short: "Setup",
  tip: "What pluxy found, and what is still missing",
  introFirst:
    "pluxy looks at this machine and offers what fits. Nothing is installed until you press Start, and nothing on this page is required.",
  introLater: "Setup is finished. This page shows the same things as they are now.",
  loading: "Loading…",
  retry: "Retry",

  found: "Found on this machine",
  foundRemote: "Found on the machine pluxy runs on",
  lookAgain: "Look again",
  looking: "Looking…",
  lookingFirst: "pluxy is looking at this machine. This takes a few seconds.",
  neverLooked: "pluxy has not looked at this machine yet.",
  nothingFound: "No client, sign-in or CLIProxyAPI setup was found.",
  clientFound: "{name} {version}",
  signInHint: "pluxy read nothing from this sign-in. It only saw that it is there.",
  importFound: {
    one: "A CLIProxyAPI setup with {count} account",
    other: "A CLIProxyAPI setup with {count} accounts",
  },
  importHow: "It is at {config}. To bring it across, run this where pluxy is installed:",
  listens: "pluxy listens on {address}.",
  reachable: "Other machines on the network can reach pluxy at this address.",
  lookedAt: "Looked {when}.",

  providers: "Providers",
  providersHint: "A provider lets pluxy use one kind of account, such as a Claude subscription.",
  tickHint: "Tick the ones you have an account for.",
  noPicks: "pluxy has no provider to offer here. The Extensions page lists what can be installed.",
  installed: "Installed",
  failed: "Could not install {title}: {error}",
  failedRetry: "Press {button} to try again.",

  startTitle: "Start",
  start: "Start",
  install: "Install",
  startWithout: "Start without a provider",
  willInstall: "{button} installs: {names}.",
  installing: "Installing…",
  nothingTicked:
    "Nothing is ticked, so pluxy will run with no provider. You can add one later on the Extensions page.",
  nothingMore: "Nothing more to install.",
  allInstalled: "Every provider on the list is installed.",

  account: "Connect an account",
  accountHint: "An account is your sign-in with a provider. pluxy sends requests through it.",
  accountAfter: "This comes after {button}: an account signs in through a provider.",
  accountsConnected: { one: "{count} account connected.", other: "{count} accounts connected." },
  accountsLink: "See them on Accounts",
  accountSkip: "You can skip this and add an account later on the Add Account page.",
  signInFound: "{label}. You can sign in to the same account here.",

  clients: "Point a client at pluxy",
  clientsHint:
    "A client is a coding tool, such as Claude Code, that sends its requests through pluxy. pluxy changes no client's files from this page: it shows the text, and you paste it.",
  noClients: "No client was found on this machine.",
  "state.configured": "Points at this pluxy",
  "state.not-configured": "Does not point at pluxy yet",
  "state.needs-update": "Its settings need an update to work with this pluxy",
  "state.refused":
    "pluxy would not change {config}: it holds something pluxy does not expect. That is fine here, because you paste the text yourself.",
  notFound: "Not found on this machine",
  showPaste: "Show what to paste",
  hidePaste: "Hide",
  otherClients: { one: "Other clients ({count})", other: "Other clients ({count})" },
  pasteLoading: "Loading the text…",
  pasteInto: "Put this in {path}:",
  pasteAddress: "The text uses {address}, the address of pluxy on its own machine.",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Could not copy. Select the text and copy it yourself.",
  keyWhere:
    "Where the text says {placeholder}, a client key goes: the password a client uses to reach pluxy. Make one here, or use one you already have.",
  makeKey: "Make a client key",
  newKey: "New client key",
  keyInText:
    "The new key is in the text above, where {placeholder} stood. Copy the text while this notice is open.",

  finishTitle: "Finish",
  finish: "Finish",
  finishHint:
    "This ends setup and opens the Dashboard. You can come back to this page at any time: it stays in the navigation.",
});

/** A button that copies text, and says so. */
function Copy(props: { value: string }) {
  const [state, setState] = useState<"copied" | "failed">();
  useEffect(() => setState(undefined), [props.value]);
  return (
    <>
      <Button
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(props.value);
            setState("copied");
          } catch {
            setState("failed");
          }
        }}
      >
        {state === "copied" ? t("copied") : t("copy")}
      </Button>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </>
  );
}

/** Text to copy, in the kit's code style, with its Copy button. */
function Code(props: { value: string }) {
  return (
    <div class="setup-code">
      <pre>{props.value}</pre>
      <div class="actions">
        <Copy value={props.value} />
      </div>
    </div>
  );
}

/** What core found: clients, sign-ins, an earlier setup, and where core listens. */
export function Found(props: {
  setup: Setup;
  remote: boolean;
  busy?: boolean;
  error?: string;
  onLook: () => void;
}) {
  const { setup } = props;
  const state = scanState(setup);
  const clients = splitClients(setup).found;
  const nothing = clients.length + setup.found.sign_ins.length + setup.found.imports.length === 0;
  const looking = state === "first" || state === "again";
  return (
    <Card
      title={props.remote ? t("foundRemote") : t("found")}
      actions={
        <Button onClick={props.onLook} disabled={looking || props.busy}>
          {looking ? t("looking") : t("lookAgain")}
        </Button>
      }
    >
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      {state === "first" ? (
        <Notice tone="muted">{t("lookingFirst")}</Notice>
      ) : (
        <ul class="setup-rows" data-part="setup-found" aria-busy={looking}>
          {clients.map((client) => (
            <li key={`client/${client.id}`}>
              <strong>{t("clientFound", { name: client.name, version: client.version ?? "" }).trim()}</strong>
              <span class="muted">{clientWords(client)}</span>
            </li>
          ))}
          {setup.found.sign_ins.map((signIn) => (
            <li key={`sign-in/${signIn.provider}/${signIn.from}`}>
              <strong>{signIn.label}</strong>
              <span class="muted">{t("signInHint")}</span>
            </li>
          ))}
          {setup.found.imports.map((found) => (
            <li key={`import/${found.config}`}>
              <strong>{t("importFound", { count: found.accounts })}</strong>
              <span class="muted">
                {rich(t("importHow"), { config: <span class="mono">{found.config}</span> })}
              </span>
              <Code value={`pluxy import cpa ${found.config}`} />
            </li>
          ))}
          {nothing && <li class="muted">{state === "never" ? t("neverLooked") : t("nothingFound")}</li>}
          {setup.found.core.listen && (
            <li>
              <span>
                {rich(t("listens"), { address: <span class="mono">{setup.found.core.listen}</span> })}
                {setup.found.core.reachable_from_network && ` ${t("reachable")}`}
              </span>
            </li>
          )}
        </ul>
      )}
      {setup.scanned_at && state !== "first" && (
        <small class="hint">{t("lookedAt", { when: when(setup.scanned_at) ?? setup.scanned_at })}</small>
      )}
    </Card>
  );
}

/** A client's state in words. */
function clientWords(client: SetupClient): string {
  if (client.state === "refused") return t("state.refused", { config: client.config });
  return t(`state.${client.state}`);
}

/** The provider picks as a checklist, each with its outcome beside it. */
export function Providers(props: {
  setup: Setup;
  choices: Choices;
  results: Results;
  busy?: boolean;
  onTick: (id: string, ticked: boolean) => void;
}) {
  const picks = providerPicks(props.setup);
  const button = props.setup.first_run ? t("start") : t("install");
  return (
    <Card title={t("providers")}>
      {picks.length === 0 ? (
        <Empty>{t("noPicks")}</Empty>
      ) : (
        <>
          <p class="muted setup-hint">
            {t("providersHint")}{" "}
            {picks.every((pick) => isInstalled(pick, props.results)) ? t("allInstalled") : t("tickHint")}
          </p>
          <ul class="setup-rows">
            {picks.map((pick) => (
              <PickRow key={pick.id} pick={pick} button={button} {...props} />
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}

function PickRow(props: {
  pick: SetupPick;
  button: string;
  choices: Choices;
  results: Results;
  busy?: boolean;
  onTick: (id: string, ticked: boolean) => void;
}) {
  const { pick } = props;
  const installed = isInstalled(pick, props.results);
  const result = props.results[pick.id];
  return (
    <li
      data-part="setup-pick"
      data-state={installed ? "installed" : result?.status === "error" ? "failed" : "offered"}
    >
      <div class="setup-row-head">
        <label class="check">
          <input
            type="checkbox"
            checked={installed || isTicked(pick, props.choices, props.results)}
            disabled={installed || props.busy}
            onChange={(e) => props.onTick(pick.id, e.currentTarget.checked)}
          />
          <span class="setup-pick-text">
            <strong>{pick.title || pick.id}</strong>
            {pick.summary && <span class="muted">{pick.summary}</span>}
            {pick.because && !installed && <small class="hint">{pick.because}</small>}
          </span>
        </label>
        {installed && <Badge tone="ok">{t("installed")}</Badge>}
      </div>
      {!installed && result?.status === "error" && (
        <Notice tone="danger">
          {t("failed", { title: pick.title || pick.id, error: result.error ?? "" })}
          <br />
          {t("failedRetry", { button: props.button })}
        </Notice>
      )}
    </li>
  );
}

/** The one primary button: it installs what is ticked, or starts with nothing. */
export function Start(props: {
  setup: Setup;
  choices: Choices;
  results: Results;
  busy?: boolean;
  error?: string;
  provider: boolean;
  onStart: () => void;
  onFinish: () => void;
}) {
  const { setup } = props;
  const picks = providerPicks(setup);
  const ids = toInstall(setup, props.choices, props.results);
  const names = picks.filter((pick) => ids.includes(pick.id)).map((pick) => pick.title || pick.id);
  const button = setup.first_run ? t("start") : t("install");
  let line: string;
  let action: ComponentChildren = null;
  if (ids.length > 0) {
    line = props.busy ? t("installing") : t("willInstall", { button, names: names.join(", ") });
    action = (
      <Button variant="primary" disabled={props.busy} onClick={props.onStart}>
        {button}
      </Button>
    );
  } else if (!props.provider && setup.first_run) {
    line = t("nothingTicked");
    action = (
      <Button variant="primary" disabled={props.busy} onClick={props.onFinish}>
        {t("startWithout")}
      </Button>
    );
  } else if (setup.first_run) {
    line = t("nothingMore");
  } else {
    // After setup the part has nothing to say until a provider is ticked.
    return null;
  }
  return (
    <Card title={setup.first_run ? t("startTitle") : t("install")}>
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      <div class="setup-line">
        <span role="status">{line}</span>
        {action}
      </div>
    </Card>
  );
}

/** The Add Account page's sign-in, once a provider is installed and core has no account. */
export function Account(props: {
  setup: Setup;
  results: Results;
  count?: number;
  provider: boolean;
  error?: string;
  onAdded: () => void;
}) {
  const { setup } = props;
  const button = setup.first_run ? t("start") : t("install");
  return (
    <Card title={t("account")}>
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      {props.count === undefined ? null : props.count > 0 ? (
        <p class="setup-done">
          {t("accountsConnected", { count: props.count })} <a href="#/accounts">{t("accountsLink")}</a>.
        </p>
      ) : !props.provider ? (
        <p class="muted setup-hint">
          {t("accountHint")} {t("accountAfter", { button })}
        </p>
      ) : (
        <>
          <p class="muted setup-hint">
            {t("accountHint")} {t("accountSkip")}
          </p>
          {signInsToName(setup, props.results).map((signIn) => (
            <Notice key={`${signIn.provider}/${signIn.from}`} tone="muted">
              {t("signInFound", { label: signIn.label })}
            </Notice>
          ))}
          <AddAccountPage embedded onSuccess={props.onAdded} />
        </>
      )}
    </Card>
  );
}

/** What a host adds to a client's row: a button that opens one of the host's own pages. */
export interface ClientAction {
  label: string;
  /** The ID of the page the button opens. */
  page: string;
}

/**
 * What the build that shows the page adds to it. The panel adds nothing; the desktop app offers
 * to set a client up on its own computer.
 */
export interface SetupOptions {
  /** The action for a client found installed, by its ID in core's scan; nothing for no action. */
  clientAction?: (client: string) => ClientAction | undefined;
  /** One sentence under the clients' heading, shown while a client has an action: what it does. */
  clientActionHint?: string;
}

/** The files to paste for one client, and a button that makes the client key they need. */
function PasteText(props: { client: SetupClient }) {
  const { api, unauthorized } = useKit();
  const paste = useLoad(() => api.clientPaste(props.client.id));
  const [key, setKey] = useState<string>();
  const [making, setMaking] = useState(false);
  const [error, setError] = useState<string>();
  const make = async () => {
    setMaking(true);
    setError(undefined);
    const reply = await api.addClientKey();
    setMaking(false);
    if (reply.status === 401) return unauthorized();
    setError(problem(reply));
    if (reply.body) setKey(reply.body.value);
  };
  const data = paste.data;
  return (
    <div class="setup-paste stack" data-part="setup-paste">
      {paste.error && (
        <>
          <Notice tone="danger">{paste.error}</Notice>
          <div>
            <Button onClick={paste.reload}>{t("retry")}</Button>
          </div>
        </>
      )}
      {paste.loading && !data && <Notice tone="muted">{t("pasteLoading")}</Notice>}
      {data && (
        <>
          {data.note && <p class="setup-hint">{data.note}</p>}
          {data.files.map((file) => (
            <div key={file.path} class="setup-file">
              <span>{rich(t("pasteInto"), { path: <span class="mono">{file.path}</span> })}</span>
              <Code value={withKey(file.content, data.key, key)} />
            </div>
          ))}
          <small class="hint">
            {rich(t("pasteAddress"), { address: <span class="mono">{data.base_url}</span> })}
          </small>
          {error && <Notice tone="danger">{error}</Notice>}
          {key ? (
            <>
              <p class="setup-hint">
                {rich(t("keyInText"), { placeholder: <span class="mono">{data.key}</span> })}
              </p>
              <KeyNotice title={t("newKey")} value={key} close={() => setKey(undefined)} seen={() => {}} />
            </>
          ) : (
            <div class="setup-line">
              <span class="muted">
                {rich(t("keyWhere"), { placeholder: <span class="mono">{data.key}</span> })}
              </span>
              <Button disabled={making} onClick={make}>
                {t("makeKey")}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ClientRow(props: { client: SetupClient; action?: ClientAction }) {
  const { client, action } = props;
  const [open, setOpen] = useState(false);
  return (
    <li data-part="setup-client" data-state={client.installed ? client.state : "not-found"}>
      <div class="setup-row-head">
        <span class="setup-pick-text">
          <strong>
            {client.name}
            {client.version && <span class="mono muted"> {client.version}</span>}
          </strong>
          {!client.installed ? (
            <span class="muted">{t("notFound")}</span>
          ) : client.state === "configured" ? (
            <span>
              <Badge tone="ok">{clientWords(client)}</Badge>
            </span>
          ) : (
            <span class="muted">{clientWords(client)}</span>
          )}
        </span>
        <div class="actions">
          {action && (
            <Button
              onClick={() => {
                location.hash = `#/${action.page}`;
              }}
            >
              {action.label}
            </Button>
          )}
          <Button onClick={() => setOpen(!open)}>{open ? t("hidePaste") : t("showPaste")}</Button>
        </div>
      </div>
      {open && <PasteText client={client} />}
    </li>
  );
}

/** Each client with its state and the text that points it at core. */
export function Clients(props: { setup: Setup; options?: SetupOptions }) {
  const { found, others } = splitClients(props.setup);
  const scanning = scanState(props.setup) === "first";
  const actions = new Map(found.map((client) => [client.id, props.options?.clientAction?.(client.id)]));
  const hint = props.options?.clientActionHint;
  return (
    <Card title={t("clients")}>
      <p class="muted setup-hint">{t("clientsHint")}</p>
      {hint && found.some((client) => actions.get(client.id)) && <p class="muted setup-hint">{hint}</p>}
      {found.length > 0 ? (
        <ul class="setup-rows">
          {found.map((client) => (
            <ClientRow key={client.id} client={client} action={actions.get(client.id)} />
          ))}
        </ul>
      ) : (
        !scanning && <p class="setup-hint">{t("noClients")}</p>
      )}
      {others.length > 0 && (
        <details class="setup-others">
          <summary>{t("otherClients", { count: others.length })}</summary>
          <ul class="setup-rows">
            {others.map((client) => (
              <ClientRow key={client.id} client={client} />
            ))}
          </ul>
        </details>
      )}
    </Card>
  );
}

export function SetupPage(props: { options?: SetupOptions }) {
  const { api, host, unauthorized, extensionsChanged } = useKit();
  const loaded = useLoad(api.setup);
  const accounts = useLoad(api.accounts);
  const packages = useLoad(api.packages);
  // The last good reply stays on screen while the next one loads, and when one fails.
  const kept = useRef<Setup | undefined>(undefined);
  if (loaded.data) kept.current = loaded.data;
  const setup = kept.current;
  const [choices, setChoices] = useState<Choices>({});
  const [results, setResults] = useState<Results>({});
  const [applying, setApplying] = useState(false);
  const [applyError, setApplyError] = useState<string>();
  const look = useAction(loaded.reload);
  const finish = useAction(() => {
    loaded.reload();
    location.hash = "#/";
  });

  // While core scans, read its answer again every second, as long as the page is visible.
  const scanning = setup?.scanning === true;
  useEffect(() => {
    if (!scanning || loaded.loading) return;
    const timer = setTimeout(() => {
      if (document.visibilityState === "visible") loaded.reload();
    }, 1000);
    const onVisible = () => {
      if (document.visibilityState === "visible") loaded.reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [scanning, loaded.loading, loaded.reload]);

  // A core that never looked (an install from before setup existed) looks once when the page opens.
  const asked = useRef(false);
  const never = setup !== undefined && scanState(setup) === "never";
  useEffect(() => {
    if (!never || asked.current) return;
    asked.current = true;
    look.run(api.scanSetup);
  }, [never]);

  const onStart = async () => {
    if (!setup || applying) return;
    setApplying(true);
    setApplyError(undefined);
    const done = await start(api, setup, choices, results);
    setApplying(false);
    if (done.reply.status === 401) return unauthorized();
    setApplyError(problem(done.reply));
    setResults(done.results);
    loaded.reload();
    packages.reload();
    extensionsChanged();
  };

  if (!setup) {
    return (
      <>
        <PageHead title={t("title")} />
        {loaded.error ? (
          <>
            <Notice tone="danger">{loaded.error}</Notice>
            <Button onClick={loaded.reload}>{t("retry")}</Button>
          </>
        ) : (
          <Notice tone="muted">{t("loading")}</Notice>
        )}
      </>
    );
  }
  return (
    <SetupView
      setup={setup}
      options={props.options}
      remote={!onThisMachine(host.address)}
      choices={choices}
      results={results}
      provider={hasProvider(setup, results, packages.data?.packages)}
      accounts={accounts.data?.accounts.length}
      applying={applying}
      finishing={finish.busy}
      looking={look.busy}
      errors={{
        look: look.error ?? (loaded.loading ? undefined : loaded.error),
        apply: applyError,
        accounts: accounts.error,
        finish: finish.error,
      }}
      onLook={() => look.run(api.scanSetup)}
      onTick={(id, ticked) => setChoices((c) => ({ ...c, [id]: ticked }))}
      onStart={onStart}
      onAdded={accounts.reload}
      onFinish={() => finish.run(api.finishSetup)}
    />
  );
}

/** The page's parts, top to bottom, from what the page holds. */
export function SetupView(props: {
  setup: Setup;
  /** What the build adds to the page. */
  options?: SetupOptions;
  /** Core runs on another machine than the one showing the page. */
  remote: boolean;
  choices: Choices;
  results: Results;
  /** Core runs a provider. */
  provider: boolean;
  /** How many accounts core has; undefined until it has said. */
  accounts?: number;
  applying?: boolean;
  finishing?: boolean;
  looking?: boolean;
  errors?: { look?: string; apply?: string; accounts?: string; finish?: string };
  onLook: () => void;
  onTick: (id: string, ticked: boolean) => void;
  onStart: () => void;
  onAdded: () => void;
  onFinish: () => void;
}) {
  const { setup, choices, results, errors } = props;
  // One primary button at a time: Finish takes it once Start has nothing left to do.
  const finishNext = toInstall(setup, choices, results).length === 0 && props.provider;
  return (
    <>
      <PageHead title={t("title")} />
      <div class="setup stack">
        <p class="muted setup-hint">{setup.first_run ? t("introFirst") : t("introLater")}</p>
        <Found
          setup={setup}
          remote={props.remote}
          busy={props.looking}
          error={errors?.look}
          onLook={props.onLook}
        />
        <Providers
          setup={setup}
          choices={choices}
          results={results}
          busy={props.applying}
          onTick={props.onTick}
        />
        <Start
          setup={setup}
          choices={choices}
          results={results}
          busy={props.applying || props.finishing}
          error={errors?.apply}
          provider={props.provider}
          onStart={props.onStart}
          onFinish={props.onFinish}
        />
        <Account
          setup={setup}
          results={results}
          count={props.accounts}
          provider={props.provider}
          error={errors?.accounts}
          onAdded={props.onAdded}
        />
        <Clients setup={setup} options={props.options} />
        {setup.first_run && (
          <Card title={t("finishTitle")}>
            {errors?.finish && <Notice tone="danger">{errors.finish}</Notice>}
            <div class="setup-line">
              <span class="muted">{t("finishHint")}</span>
              <Button
                variant={finishNext ? "primary" : "quiet"}
                disabled={props.finishing || props.applying}
                onClick={props.onFinish}
              >
                {t("finish")}
              </Button>
            </div>
          </Card>
        )}
      </div>
    </>
  );
}

export const setupPage = (options?: SetupOptions): PageDef => ({
  id: "setup",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <SetupPage options={options} />,
});
