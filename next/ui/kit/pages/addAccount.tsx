import { useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { LoginMethod, LoginMethodsExtension } from "../api";
import { LoginFlow, suggestedAccountId, validAccountId } from "../loginFlow";
import { usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Button, Empty, List, Notice, PageHead, useKit, useLoad } from "../ui";

const t = strings("addAccount", {
  title: "Add Account",
  short: "Add",
  tip: "Sign in or add an API key",
  methods: "Sign-in methods",
  method: "Method",
  browser: "Browser sign-in",
  device: "Device code",
  api_key: "API key",
  accountId: "Account ID",
  invalidId:
    "Use 1–128 letters, digits, dots, underscores or hyphens, starting with a letter or digit. The ID must not end with a dot.",
  loading: "Loading sign-in methods…",
  empty: "No running extension offers a sign-in method.",
  emptyNext: "Install an extension first on {page}.",
  setupPage: "Set up pluxy",
  retry: "Retry",
});

interface MethodTile {
  id: string;
  extension: string;
  method: LoginMethod;
}

/** One provider's sign-in ways, under the provider's name. */
export interface MethodGroup {
  extension: string;
  /** The provider's title as people know it, or its package ID where no title is known. */
  title: string;
  tiles: MethodTile[];
}

/** The sign-in ways grouped by provider, in the order core lists them; a provider with none is left out. */
export function groupMethods(
  extensions: LoginMethodsExtension[],
  titles: Record<string, string>,
): MethodGroup[] {
  return extensions
    .filter((extension) => (extension.methods ?? []).length > 0)
    .map((extension) => ({
      extension: extension.extension,
      title: titles[extension.extension] || extension.extension,
      tiles: extension.methods.map((method) => ({
        id: `${extension.extension}/${method.method}`,
        extension: extension.extension,
        method,
      })),
    }));
}

/**
 * onSuccess: called with the new account's ID once a sign-in adds it (Beginner Mode counts it).
 * embedded: the sign-in without the page's own title, for a page that shows it as one of its parts.
 */
export function AddAccountPage(props: { onSuccess?: (accountId: string) => void; embedded?: boolean } = {}) {
  const { api } = useKit();
  const p = usePrivacy();
  const methods = useLoad(api.loginMethods);
  const [accountId, setAccountId] = useState(suggestedAccountId);
  const [selected, setSelected] = useState<string>();
  const [attempt, setAttempt] = useState(0);
  const typedId = useRef(false);
  const idField = useRef<HTMLInputElement>(null);
  const idHint = useId();
  useLayoutEffect(() => {
    if (selected === undefined && attempt > 0) idField.current?.focus();
  }, [selected, attempt]);
  // The providers' titles come from the setup read; without them a group is named by its package ID.
  const setup = useLoad(api.setup);
  const titles: Record<string, string> = {};
  for (const pick of setup.data?.picks ?? []) if (pick.title) titles[pick.id] = pick.title;
  const groups = groupMethods(methods.data?.extensions ?? [], titles);
  const tiles = groups.flatMap((group) => group.tiles);
  const reset = (completed = false) => {
    setSelected(undefined);
    if (!typedId.current || completed) {
      setAccountId(suggestedAccountId());
      typedId.current = false;
    }
    setAttempt((n) => n + 1);
  };
  const validId = validAccountId(accountId);
  return (
    <>
      {!props.embedded && <PageHead title={t("title")} />}
      <div class="add-account stack">
        {methods.error && (
          <>
            <Notice tone="danger">{p.text(methods.error)}</Notice>
            <div>
              <Button onClick={methods.reload}>{t("retry")}</Button>
            </div>
          </>
        )}
        {methods.loading && <Notice tone="muted">{t("loading")}</Notice>}
        {methods.data && tiles.length === 0 && (
          <Empty>
            {t("empty")}
            {!props.embedded && (
              <> {rich(t("emptyNext"), { page: <a href="#/setup">{t("setupPage")}</a> })}</>
            )}
          </Empty>
        )}
        {tiles.length > 0 && (
          <>
            <label class="field" data-part="field">
              <span class="label">{t("accountId")}</span>
              <input
                data-slot="input"
                ref={idField}
                type="text"
                required
                maxLength={128}
                pattern="[A-Za-z0-9][A-Za-z0-9._\x2D]{0,127}"
                value={accountId}
                aria-invalid={!validId}
                aria-describedby={!validId ? idHint : undefined}
                disabled={selected !== undefined}
                autocomplete="off"
                onInput={(e) => {
                  typedId.current = true;
                  setAccountId(e.currentTarget.value);
                }}
              />
            </label>
            {!validId && (
              <small id={idHint} class="hint">
                {t("invalidId")}
              </small>
            )}
            {groups.map((group) => {
              const shown = selected ? group.tiles.filter((tile) => tile.id === selected) : group.tiles;
              if (shown.length === 0) return null;
              return (
                <section
                  key={group.extension}
                  class="account-group stack"
                  data-part="method-group"
                  data-extension={group.extension}
                  aria-label={group.title}
                >
                  <header class="card-head">
                    <h2>{group.title}</h2>
                    {group.title !== group.extension && <span class="mono muted">{group.extension}</span>}
                  </header>
                  <List<MethodTile>
                    label={`${t("methods")}: ${group.title}`}
                    items={shown}
                    id={(tile) => tile.id}
                    columns={[
                      {
                        label: t("method"),
                        value: (tile) => (
                          <section class="account-method stack" data-part="method">
                            <div class="actions">
                              <Button
                                disabled={!validId || selected !== undefined}
                                onClick={() => setSelected(tile.id)}
                              >
                                {tile.method.label || tile.method.method}
                              </Button>
                              <span class="muted">
                                {tile.method.kind === "browser" ||
                                tile.method.kind === "device" ||
                                tile.method.kind === "api_key"
                                  ? t(tile.method.kind)
                                  : tile.method.kind}
                              </span>
                            </div>
                            {selected === tile.id && (
                              <LoginFlow
                                key={`${tile.id}/${attempt}`}
                                extension={tile.extension}
                                method={tile.method}
                                newAccountId={accountId}
                                onSuccess={(id) => props.onSuccess?.(id)}
                                onReset={reset}
                              />
                            )}
                          </section>
                        ),
                      },
                    ]}
                  />
                </section>
              );
            })}
          </>
        )}
      </div>
    </>
  );
}

export const addAccountPage = (): PageDef => ({
  id: "add-account",
  group: "gateway",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <AddAccountPage />,
});
