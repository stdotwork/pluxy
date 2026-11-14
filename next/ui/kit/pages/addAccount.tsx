import { useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { LoginMethod } from "../api";
import { LoginFlow, suggestedAccountId, validAccountId } from "../loginFlow";
import { usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Empty, List, Notice, PageHead, useKit, useLoad } from "../ui";

const t = strings("addAccount", {
  title: "Add Account",
  short: "Add",
  tip: "OAuth authorization",
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
  retry: "Retry",
});

interface MethodTile {
  id: string;
  extension: string;
  method: LoginMethod;
}

export function AddAccountPage() {
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
  const tiles: MethodTile[] = (methods.data?.extensions ?? []).flatMap((extension) =>
    (extension.methods ?? []).map((method) => ({
      id: `${extension.extension}/${method.method}`,
      extension: extension.extension,
      method,
    })),
  );
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
      <PageHead title={t("title")} />
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
        {methods.data && tiles.length === 0 && <Empty>{t("empty")}</Empty>}
        {tiles.length > 0 && (
          <>
            <label class="field" data-part="field">
              <span class="label">{t("accountId")}</span>
              <input
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
            <List<MethodTile>
              label={t("methods")}
              items={selected ? tiles.filter((tile) => tile.id === selected) : tiles}
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
                      <span class="mono muted">{tile.extension}</span>
                      {selected === tile.id && (
                        <LoginFlow
                          key={`${tile.id}/${attempt}`}
                          extension={tile.extension}
                          method={tile.method}
                          newAccountId={accountId}
                          onSuccess={() => {}}
                          onReset={reset}
                        />
                      )}
                    </section>
                  ),
                },
              ]}
            />
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
