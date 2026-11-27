import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Account, type LoginMethod, type LoginMethodsExtension, problem } from "../api";
import { Icon } from "../icons";
import { LoginFlow } from "../loginFlow";
import { accountServer, Phases, providerName, StatePane } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Notice, PageHead, useKit, useLoad, usePoll } from "../ui";
import { nameProblem } from "./accounts.helpers";
import { monogram, recommendedWay, seriesOf, shortId, splitKinds } from "./addAccount.helpers";
import { AuthImport } from "./authImport";
import { MAIN_KINDS } from "./setup.helpers";

/** How many times the page asks again for a new account's check. */
const CHECK_ASKS = 10;

const t = strings("addAccount", {
  title: "Add an account",
  tip: "Sign in or add an API key",
  loading: "Loading sign-in methods…",
  failed: "Could not read the sign-in methods.",
  empty: "There is no provider to sign in with yet.",
  emptyHelp: "A provider is what pluxy talks to, such as Claude or ChatGPT. Add one in Extensions.",
  openExtensions: "Open Extensions",
  chooseProvider: "Which account do you want to add?",
  providers: "Providers",
  moreProviders: "More providers",
  moreCount: "{count} more",
  back: "All providers",
  choose: "Choose how to add it.",
  methods: "Ways to add it",
  recommended: "Recommended",
  browser: "Sign in with your account in your browser.",
  device: "Get a code to enter on another device, for a computer with no browser.",
  api_key: "Paste an API key from the service.",
  start: "Start",
  startMethod: "Start: {method}",
  added: "Added {name}.",
  addedUnnamed: "Added your {account}.",
  checking: "Checking it with the service…",
  checkOk: "pluxy checked it with the service, and it works.",
  checkFailed: "pluxy could not check it with the service: {error}",
  name: "Name",
  nameHelp: "How the account is called in lists. You can change it later.",
  namePlaceholder: "For example: Work OpenRouter",
  saveName: "Save name",
  saved: "Saved.",
  openAccount: "Open account",
  another: "Add another",
  claudeSummary: "Claude Pro or Max, or an Anthropic API key",
  codexSummary: "ChatGPT Plus or Pro, or an OpenAI API key",
  geminiSummary: "A Google AI key, or Vertex AI",
  compatibleSummary: "OpenRouter, DeepSeek or any service with an OpenAI-style key",
  kindBrowser: "browser sign-in",
  kindDevice: "device code",
  kindKey: "API key",
  kindsOr: "{list} or {last}",
});

interface MethodRow {
  id: string;
  extension: string;
  method: LoginMethod;
}

/** A provider the page offers: its extension ID and, when the caller has its own name for it, the title to show. */
export interface ProviderChoice {
  id: string;
  title?: string;
  /** What an account of it is called after "your", when "{title} account" does not read well. */
  account?: string;
}

/** One provider's sign-in ways, under the provider's name. */
export interface MethodGroup {
  extension: string;
  /** The provider's name as AccountRow shows it. */
  title: string;
  /** What an account of it is called after "your". */
  account: string;
  /** The caller gave the provider's title: it is shown as given. */
  titled: boolean;
  rows: MethodRow[];
}

/**
 * The sign-in ways grouped by provider; a provider with none is left out. In the order core lists
 * them, or, given providers, only those and in that order, each under its own title when it has one.
 */
export function groupMethods(
  extensions: LoginMethodsExtension[],
  providers?: ProviderChoice[],
): MethodGroup[] {
  const shown = providers
    ? providers.flatMap((choice) =>
        extensions
          .filter((extension) => extension.extension === choice.id)
          .slice(0, 1)
          .map((extension) => ({ extension, title: choice.title, account: choice.account })),
      )
    : extensions.map((extension) => ({ extension, title: undefined, account: undefined }));
  return shown
    .filter(({ extension }) => (extension.methods ?? []).length > 0)
    .map(({ extension, title, account }) => ({
      extension: extension.extension,
      title: title || providerName(extension.extension),
      account: account ?? `${title || providerName(extension.extension)} account`,
      titled: Boolean(title),
      rows: extension.methods.map((method) => ({
        id: `${extension.extension}/${method.method}`,
        extension: extension.extension,
        method,
      })),
    }));
}

/** What a way does, in a sentence: how it signs in. */
export function methodWords(method: LoginMethod): string {
  return method.kind === "browser" || method.kind === "device" || method.kind === "api_key"
    ? t(method.kind)
    : method.kind;
}

/**
 * The name shown for a provider: the one the caller gave, else the plain name of a main kind
 * ("ChatGPT (Codex)"), else the name the rest of the app uses.
 */
function displayName(group: Pick<MethodGroup, "extension" | "title" | "titled">): string {
  return (!group.titled && MAIN_KINDS.find((kind) => kind.id === group.extension)?.title) || group.title;
}

/** The sentence under a provider's tile: what accounts it takes, or else the ways it offers. */
function tileSummary(group: MethodGroup): string {
  switch (shortId(group.extension)) {
    case "claude":
      return t("claudeSummary");
    case "codex":
      return t("codexSummary");
    case "gemini":
      return t("geminiSummary");
    case "openai-compatible":
      return t("compatibleSummary");
  }
  const words = [...new Set(group.rows.map((row) => row.method.kind))].map((kind) =>
    kind === "browser"
      ? t("kindBrowser")
      : kind === "device"
        ? t("kindDevice")
        : kind === "api_key"
          ? t("kindKey")
          : kind,
  );
  const last = words.pop();
  const text = words.length > 0 ? t("kindsOr", { list: words.join(", "), last: last ?? "" }) : (last ?? "");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/**
 * A provider's mark: a tile with its first letter in the provider's own colour (a key for another
 * service with an API key). Always next to the provider's name, so it is hidden from a screen reader.
 */
function ProviderMark(props: { extension: string; title: string }) {
  return (
    <span
      class="provider-mark"
      data-part="provider-mark"
      data-hue={seriesOf(props.extension)}
      aria-hidden="true"
    >
      {shortId(props.extension) === "openai-compatible" ? (
        <Icon name="key" size={14} />
      ) : (
        monogram(props.title, props.extension)
      )}
    </span>
  );
}

/** One provider to pick: its mark, its name and what accounts it takes. compact: only mark and name. */
function ProviderTile(props: { group: MethodGroup; compact?: boolean; onPick: () => void }) {
  const title = displayName(props.group);
  return (
    <li>
      <button
        type="button"
        data-part="provider-tile"
        data-extension={props.group.extension}
        data-compact={props.compact ? "" : undefined}
        onClick={props.onPick}
      >
        <ProviderMark extension={props.group.extension} title={title} />
        <span class="provider-text">
          <strong>{title}</strong>
          {!props.compact && <span class="muted">{tileSummary(props.group)}</span>}
        </span>
      </button>
    </li>
  );
}

/**
 * Once the sign-in is done: the account is added under a name pluxy has for it (often the
 * account's e-mail address, none for an API key). The name is optional and is saved only by Save name.
 */
function Added(props: { id: string; group: MethodGroup; embedded?: boolean; onAnother: () => void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const accounts = useLoad(api.accounts);
  const account: Account | undefined = accounts.data?.accounts.find((a) => a.id === props.id);
  // The Name field edits the real name. With privacy mode on it starts empty, with the masked name as its
  // example, so what is saved is only what the person types, never the mask.
  // An account with no name yet and an address is offered the address as its name; it is saved
  // only by Save name.
  const label = account?.label?.trim() ?? "";
  const saved0 = p.on ? "" : label;
  const prefill = saved0 || (p.on || !account ? "" : (accountServer(account) ?? ""));
  const [typed, setTyped] = useState<string>();
  const [kept, setKept] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [saved, setSaved] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  const value = typed ?? prefill;
  const unchanged = value.trim() === (kept ?? saved0).trim();
  const ready = accounts.phase !== "loading";
  // pluxy checks a new account with its service as it is added, which takes a moment: the page
  // asks again until the check has an answer, a few times at most.
  const check = account?.check;
  const asks = useRef(0);
  const waiting = ready && (check === undefined || check.state === "checking") && asks.current < CHECK_ASKS;
  usePoll(
    () => {
      asks.current++;
      accounts.reload();
    },
    1000,
    waiting,
  );
  useLayoutEffect(() => {
    if (ready) field.current?.focus();
  }, [ready]);
  const save = async () => {
    const why = nameProblem(value);
    if (why) return setError(why);
    setBusy(true);
    setError(undefined);
    setSaved(false);
    const r = await api.setAccount(props.id, { label: value.trim() });
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const failed = problem(r);
    if (failed !== undefined)
      return setError(p.text(failed, [...accountIdentities(account ?? {}), props.id]));
    setKept(value.trim());
    setSaved(true);
  };
  const provider = displayName(props.group);
  return (
    <section class="added card" data-part="account-added">
      <div class="added-head" role="status">
        <span class="added-check">
          <Icon name="check" size={16} />
        </span>
        <div class="added-text">
          <strong>
            {label ? t("added", { name: p.id(label) }) : t("addedUnnamed", { account: props.group.account })}
          </strong>
          <span class="added-provider">
            <ProviderMark extension={props.group.extension} title={provider} />
            <span class="muted">{provider}</span>
          </span>
        </div>
      </div>
      {check?.state === "ok" && <p data-part="account-check">{t("checkOk")}</p>}
      {check?.state === "failed" && (
        <Notice tone="warn">
          {t("checkFailed", {
            error: p.text(check.error?.message ?? "", [...accountIdentities(account ?? {}), props.id]),
          })}
        </Notice>
      )}
      {waiting && (
        <p class="muted" data-part="account-check">
          {t("checking")}
        </p>
      )}
      <form
        class="added-name"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <label class="field" data-part="field">
          <span class="label">{t("name")}</span>
          <span class="added-name-row">
            <input
              data-slot="input"
              ref={field}
              type="text"
              autocomplete="off"
              placeholder={p.on && label ? p.id(label) : t("namePlaceholder")}
              value={value}
              disabled={!ready}
              aria-invalid={error ? "true" : undefined}
              onInput={(e) => {
                setTyped(e.currentTarget.value);
                setError(undefined);
                setSaved(false);
              }}
            />
            <Button type="submit" disabled={busy || !ready || unchanged}>
              {t("saveName")}
            </Button>
          </span>
          <span class="hint">{t("nameHelp")}</span>
        </label>
        {error && <Notice tone="danger">{error}</Notice>}
        {saved && <Notice tone="ok">{t("saved")}</Notice>}
      </form>
      <div class="actions">
        {!props.embedded && (
          <a
            class="btn"
            data-part="button"
            data-variant="primary"
            href={`#/accounts/${encodeURIComponent(props.id)}`}
          >
            {t("openAccount")}
          </a>
        )}
        <Button onClick={props.onAnother}>{t("another")}</Button>
      </div>
    </section>
  );
}

/**
 * Add an account: choose a provider, then its way to sign in, sign in, then name the account if you
 * like. No ID is asked for or sent: core makes the account's.
 * embedded: without the page's own title, the provider tiles, the Open account link and the CLIProxyAPI
 * import, for a page that shows it as one of its parts (the providers were chosen before, so their ways
 * show at once). onSuccess: called with the new account's ID once its sign-in is done. providers: only
 * these extensions' ways to sign in, in that order, each under its title when it has one (else its
 * plain name).
 */
export function AddAccountPage(
  props: { onSuccess?: (accountId: string) => void; embedded?: boolean; providers?: ProviderChoice[] } = {},
) {
  const { api } = useKit();
  const methods = useLoad(api.loginMethods);
  // The provider whose ways are shown (its extension ID), and the way started.
  const [provider, setProvider] = useState<string>();
  const [selected, setSelected] = useState<string>();
  const [added, setAdded] = useState<{ id: string; group: MethodGroup }>();
  const [attempt, setAttempt] = useState(0);
  const [moreOpen, setMoreOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  // The way last left, so focus goes back to its Start button, and the provider last left, so
  // it goes back to its tile.
  const last = useRef<string | undefined>(undefined);
  const lastProvider = useRef<string | undefined>(undefined);
  const [backs, setBacks] = useState(0);
  useLayoutEffect(() => {
    if (selected !== undefined || added !== undefined || attempt === 0) return;
    const own = last.current && `[data-method=${JSON.stringify(last.current)}] button`;
    (
      (own && root.current?.querySelector<HTMLElement>(own)) ||
      root.current?.querySelector<HTMLElement>('[data-part="method"] button')
    )?.focus();
  }, [selected, added, attempt]);
  useLayoutEffect(() => {
    if (backs === 0 || provider !== undefined) return;
    const own = lastProvider.current && `[data-extension=${JSON.stringify(lastProvider.current)}]`;
    (
      (own && root.current?.querySelector<HTMLElement>(own)) ||
      root.current?.querySelector<HTMLElement>('[data-part="provider-tile"]')
    )?.focus();
  }, [backs, provider]);
  const reset = () => {
    setSelected(undefined);
    setAdded(undefined);
    setAttempt((n) => n + 1);
  };
  const groups = (data: { extensions: LoginMethodsExtension[] }) =>
    groupMethods(data.extensions ?? [], props.providers);
  return (
    <>
      {!props.embedded && <PageHead title={t("title")} />}
      <div class="add-account stack" ref={root}>
        {added !== undefined ? (
          <Added id={added.id} group={added.group} embedded={props.embedded} onAnother={reset} />
        ) : (
          <Phases
            load={methods}
            loading={t("loading")}
            failed={{ title: t("failed") }}
            isEmpty={(data) => groups(data).length === 0}
            empty={
              <StatePane
                kind="empty"
                title={t("empty")}
                primary={{ label: t("openExtensions"), href: "#/extensions" }}
              >
                {t("emptyHelp")}
              </StatePane>
            }
          >
            {(data) => {
              const all = groups(data);
              // A person picks a provider first, unless there is only one to pick or the caller (the
              // guided setup) already chose the providers.
              const alone = props.embedded || all.length === 1;
              if (!alone && provider === undefined) {
                const { tiles, more } = splitKinds(all);
                const pick = (group: MethodGroup) => () => {
                  lastProvider.current = group.extension;
                  setProvider(group.extension);
                };
                return (
                  <>
                    <p>{t("chooseProvider")}</p>
                    <ul class="provider-tiles" data-part="provider-tiles" aria-label={t("providers")}>
                      {tiles.map((group) => (
                        <ProviderTile key={group.extension} group={group} onPick={pick(group)} />
                      ))}
                      {more.length > 0 && (
                        <li class="provider-more-item">
                          <button
                            type="button"
                            class="provider-more"
                            aria-expanded={moreOpen}
                            onClick={() => setMoreOpen(!moreOpen)}
                          >
                            <span class="provider-more-mark" aria-hidden="true">
                              <Icon name="plus" size={14} />
                            </span>
                            <span class="provider-text">
                              <strong>{t("moreProviders")}</strong>
                              <span class="muted">{t("moreCount", { count: more.length })}</span>
                            </span>
                          </button>
                        </li>
                      )}
                    </ul>
                    {moreOpen && more.length > 0 && (
                      <ul
                        class="provider-tiles"
                        data-part="provider-tiles"
                        data-more=""
                        aria-label={t("moreProviders")}
                      >
                        {more.map((group) => (
                          <ProviderTile key={group.extension} group={group} compact onPick={pick(group)} />
                        ))}
                      </ul>
                    )}
                  </>
                );
              }
              const open = alone ? all : all.filter((group) => group.extension === provider);
              return (
                <>
                  {!alone && selected === undefined && (
                    <div class="provider-back">
                      <Button
                        variant="ghost"
                        onClick={() => {
                          setProvider(undefined);
                          setBacks((n) => n + 1);
                        }}
                      >
                        <Icon name="chevron" size={14} />
                        {t("back")}
                      </Button>
                    </div>
                  )}
                  {props.embedded && selected === undefined && <p>{t("choose")}</p>}
                  {open.map((group) => {
                    const shown = selected ? group.rows.filter((row) => row.id === selected) : group.rows;
                    if (shown.length === 0) return null;
                    const suggested = recommendedWay(group.rows.map((row) => row.method));
                    // The one filled button of the view: the suggested way, or the only one there is.
                    const filled = props.embedded
                      ? undefined
                      : (suggested ?? (group.rows.length === 1 ? group.rows[0]?.method.method : undefined));
                    const title = displayName(group);
                    return (
                      <section
                        key={group.extension}
                        class="method-group"
                        data-part="method-group"
                        data-extension={group.extension}
                        aria-label={title}
                      >
                        <div class="provider-head">
                          <ProviderMark extension={group.extension} title={title} />
                          <h2>{title}</h2>
                        </div>
                        {!props.embedded && selected === undefined && <p>{t("choose")}</p>}
                        <ul class="method-list" aria-label={t("methods")}>
                          {shown.map((row) => {
                            const label = row.method.label || row.method.method;
                            return (
                              <li
                                key={row.id}
                                class="method"
                                data-part="method"
                                data-method={row.id}
                                data-open={selected === row.id ? "" : undefined}
                              >
                                <div class="method-row">
                                  <span class="method-text">
                                    <strong>
                                      {label}
                                      {suggested === row.method.method && (
                                        <span class="method-badge">{t("recommended")}</span>
                                      )}
                                    </strong>
                                    <span class="muted">{methodWords(row.method)}</span>
                                  </span>
                                  {selected !== row.id && (
                                    <Button
                                      variant={filled === row.method.method ? "primary" : undefined}
                                      disabled={selected !== undefined}
                                      label={t("startMethod", { method: label })}
                                      onClick={() => {
                                        last.current = row.id;
                                        setSelected(row.id);
                                      }}
                                    >
                                      {t("start")}
                                    </Button>
                                  )}
                                </div>
                                {selected === row.id && (
                                  <LoginFlow
                                    key={`${row.id}/${attempt}`}
                                    extension={row.extension}
                                    method={row.method}
                                    onSuccess={(id) => {
                                      setAdded({ id, group });
                                      setSelected(undefined);
                                      props.onSuccess?.(id);
                                    }}
                                    onReset={reset}
                                  />
                                )}
                              </li>
                            );
                          })}
                        </ul>
                      </section>
                    );
                  })}
                </>
              );
            }}
          </Phases>
        )}
        {/* Kept mounted while a sign-in is under way, so the lines of an import stay until the page is left. */}
        {!props.embedded && (
          <div hidden={selected !== undefined || added !== undefined}>
            <AuthImport />
          </div>
        )}
      </div>
    </>
  );
}

export const addAccountPage = (): PageDef => ({
  id: "add-account",
  dest: "accounts",
  under: "accounts",
  title: t("title"),
  tip: t("tip"),
  render: () => <AddAccountPage />,
});
