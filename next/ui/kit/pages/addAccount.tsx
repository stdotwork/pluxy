import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Account, type LoginMethod, type LoginMethodsExtension, problem } from "../api";
import { LoginFlow } from "../loginFlow";
import { accountServer, Phases, providerName, StatePane } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Notice, PageHead, useKit, useLoad, usePoll } from "../ui";
import { nameProblem } from "./accounts.helpers";
import { AuthImport } from "./authImport";

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
  choose: "Choose how to add it.",
  methods: "Ways to add it",
  browser: "Opens your browser",
  device: "Shows a code to enter",
  api_key: "Uses an API key",
  start: "Start",
  startMethod: "Start: {method}",
  added: "Added {name}.",
  addedUnnamed: "Added your {account}.",
  checking: "Checking it with the service…",
  checkOk: "pluxy checked it with the service, and it works.",
  checkFailed: "pluxy could not check it with the service: {error}",
  name: "Name",
  namePlaceholder: "For example: Work OpenRouter",
  saveName: "Save name",
  saved: "Saved.",
  openAccount: "Open account",
  another: "Add another",
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
      rows: extension.methods.map((method) => ({
        id: `${extension.extension}/${method.method}`,
        extension: extension.extension,
        method,
      })),
    }));
}

/** What a method does, in a few words: how it signs in. */
export function methodWords(method: LoginMethod): string {
  return method.kind === "browser" || method.kind === "device" || method.kind === "api_key"
    ? t(method.kind)
    : method.kind;
}

/**
 * Once the sign-in is done: the account is added under a name pluxy has for it (often the
 * account's e-mail address, none for an API key). The name is optional and is saved only by Save name.
 */
function Added(props: { id: string; account: string; embedded?: boolean; onAnother: () => void }) {
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
  return (
    <section class="added stack" data-part="account-added">
      <Notice tone="ok">
        {label ? t("added", { name: p.id(label) }) : t("addedUnnamed", { account: props.account })}
      </Notice>
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
        class="stack"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <label class="field" data-part="field">
          <span class="label">{t("name")}</span>
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
        </label>
        <div class="actions">
          <Button type="submit" disabled={busy || !ready || unchanged}>
            {t("saveName")}
          </Button>
        </div>
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
 * Add an account: choose a provider's way to sign in, sign in, then name the account if you like.
 * No ID is asked for or sent: core makes the account's.
 * embedded: without the page's own title, the Open account link and the CLIProxyAPI import, for
 * a page that shows it as one of its parts. onSuccess: called with the new account's ID once its
 * sign-in is done. providers: only these extensions' ways to sign in, in that order, each under its
 * title when it has one (else its plain name).
 */
export function AddAccountPage(
  props: { onSuccess?: (accountId: string) => void; embedded?: boolean; providers?: ProviderChoice[] } = {},
) {
  const { api } = useKit();
  const methods = useLoad(api.loginMethods);
  const [selected, setSelected] = useState<string>();
  const [added, setAdded] = useState<{ id: string; account: string }>();
  const [attempt, setAttempt] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  // The method last left, so focus goes back to its Start button.
  const last = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    if (selected !== undefined || added !== undefined || attempt === 0) return;
    const own = last.current && `[data-method=${JSON.stringify(last.current)}] button`;
    (
      (own && root.current?.querySelector<HTMLElement>(own)) ||
      root.current?.querySelector<HTMLElement>('[data-part="method"] button')
    )?.focus();
  }, [selected, added, attempt]);
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
          <Added id={added.id} account={added.account} embedded={props.embedded} onAnother={reset} />
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
            {(data) => (
              <>
                {selected === undefined && <p>{t("choose")}</p>}
                {groups(data).map((group) => {
                  const shown = selected ? group.rows.filter((row) => row.id === selected) : group.rows;
                  if (shown.length === 0) return null;
                  return (
                    <section
                      key={group.extension}
                      class="method-group"
                      data-part="method-group"
                      data-extension={group.extension}
                      aria-label={group.title}
                    >
                      <h2>{group.title}</h2>
                      <ul class="method-list" aria-label={t("methods")}>
                        {shown.map((row) => {
                          const label = row.method.label || row.method.method;
                          return (
                            <li key={row.id} class="method" data-part="method" data-method={row.id}>
                              <div class="method-row">
                                <span class="method-text">
                                  <strong>{label}</strong>
                                  <span class="muted">{methodWords(row.method)}</span>
                                </span>
                                {selected !== row.id && (
                                  <Button
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
                                    setAdded({ id, account: group.account });
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
            )}
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
