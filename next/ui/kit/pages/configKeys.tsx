import { useEffect, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import { strings } from "../strings";
import { Button, Card, ConfirmButton, Empty, List, Notice, useKit, useLoad } from "../ui";

const t = strings("configKeys", {
  clients: "Client API Keys (access.api-keys)",
  management: "Management Key",
  add: "Add",
  remove: "Delete",
  rotate: "Rotate",
  copy: "Copy",
  copied: "Copied",
  close: "Close",
  empty: "No API keys",
  loading: "Loading client API keys...",
  number: "#",
  row: "#{number}",
  key: "API Key",
  actions: "Actions",
  newClient: "New client API key",
  newManagement: "New management key",
  once: "Copy this key now. It will not be shown again after you close this notice or leave this tab.",
  rotationHint:
    "Rotating replaces the management key immediately. Other management clients must sign in with the new key.",
  rotationUnavailable: "Management key rotation is unavailable in this host.",
  rotationUnknown:
    "The management key may have changed. Find the current key in core's config file and sign in with it before rotating again.",
  copyFailed: "Could not copy. Select the key and copy it manually.",
});

export function keyCards(tab: string, search: string) {
  const query = search.trim().toLowerCase();
  return {
    clients: query ? t("clients").toLowerCase().includes(query) : tab === "common" || tab === "connectivity",
    management: query ? t("management").toLowerCase().includes(query) : tab === "connectivity",
  };
}
interface NewKey {
  id: number;
  value: string;
  tab: string;
  seen: boolean;
}
/** Owned by ConfigPage, so a reply survives leaving a tab before it arrives. */
export function useConfigKeys(tab: string, onChanged: () => void) {
  const { api, unauthorized } = useKit();
  const [clients, setClients] = useState<NewKey[]>([]);
  const [management, setManagement] = useState<NewKey>();
  const [adding, setAdding] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [clientError, setClientError] = useState<string>();
  const [rotationError, setRotationError] = useState<string>();
  const nextId = useRef(0);
  const currentTab = useRef(tab);
  currentTab.current = tab;
  // A seen notice is cleared on leaving its tab. An unseen late reply waits for a visit.
  useEffect(() => {
    setClients((keys) => keys.filter((key) => !key.seen || key.tab === tab));
    setManagement((key) => (key?.seen && key.tab !== tab ? undefined : key));
  }, [tab]);
  const add = async () => {
    if (adding) return;
    const origin = currentTab.current;
    setAdding(true);
    setClientError(undefined);
    const reply = await api.addClientKey();
    setAdding(false);
    if (reply.status === 401) return unauthorized();
    setClientError(problem(reply));
    if (reply.body) {
      const key = { id: ++nextId.current, value: reply.body.value, tab: origin, seen: false };
      setClients((keys) => [...keys, key]);
      onChanged();
    }
    return reply;
  };
  const rotate = async () => {
    if (rotating || management) return;
    const origin = currentTab.current;
    setRotating(true);
    setRotationError(undefined);
    const reply = await api.rotateManagementKey();
    setRotating(false);
    if (reply.status === 401) return unauthorized();
    setRotationError(reply.status === 0 ? t("rotationUnknown") : problem(reply));
    if (reply.body) {
      setManagement({ id: ++nextId.current, value: reply.body.value, tab: origin, seen: false });
      onChanged();
    }
  };
  return {
    clients,
    management,
    adding,
    rotating,
    clientError,
    rotationError,
    add,
    rotate,
    seenClient: (id: number) =>
      setClients((keys) => keys.map((key) => (key.id === id ? { ...key, seen: true } : key))),
    closeClient: (id: number) => setClients((keys) => keys.filter((key) => key.id !== id)),
    seenManagement: () => setManagement((key) => (key ? { ...key, seen: true } : key)),
    closeManagement: () => setManagement(undefined),
  };
}
type KeysState = ReturnType<typeof useConfigKeys>;

function KeyNotice(props: { title: string; value: string; close: () => void; seen: () => void }) {
  const [copied, setCopied] = useState(false);
  const [failed, setFailed] = useState(false);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    ref.current?.focus();
    props.seen();
  }, []);
  return (
    <section
      ref={ref}
      tabIndex={-1}
      class="stack config-key-notice"
      data-part="key-notice"
      aria-label={props.title}
    >
      <strong>{props.title}</strong>
      <span>{t("once")}</span>
      <code data-part="key-value">{props.value}</code>
      <div class="actions">
        <Button
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(props.value);
              setCopied(true);
              setFailed(false);
            } catch {
              setFailed(true);
            }
          }}
        >
          {t("copy")}
        </Button>
        <Button onClick={props.close}>{t("close")}</Button>
        {copied && <span role="status">{t("copied")}</span>}
      </div>
      {failed && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </section>
  );
}
function ClientKeys(props: { state: KeysState; tab: string; onChanged: () => void }) {
  const { api, unauthorized } = useKit();
  const keys = useLoad(api.clientKeys);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string>();
  const actions = useRef<HTMLDivElement>(null);
  const state = props.state;
  const add = async () => {
    if (state.adding || keys.loading || deleting) return;
    const reply = await state.add();
    if (reply && !problem(reply)) keys.reload();
  };
  const remove = async (id: string) => {
    setDeleting(true);
    setError(undefined);
    const reply = await api.deleteClientKey(id);
    setDeleting(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    setError(why);
    keys.reload();
    actions.current?.querySelector<HTMLButtonElement>("button")?.focus();
    if (!why) props.onChanged();
  };
  return (
    <Card
      title={t("clients")}
      actions={
        <div ref={actions}>
          <button
            type="button"
            class="btn"
            data-part="button"
            data-variant="quiet"
            aria-disabled={state.adding || keys.loading || deleting}
            onClick={add}
          >
            {t("add")}
          </button>
        </div>
      }
    >
      {(error || state.clientError || keys.error) && (
        <Notice tone="danger">{error ?? state.clientError ?? keys.error}</Notice>
      )}
      {state.clients
        .filter((key) => key.tab === props.tab)
        .map((key) => (
          <KeyNotice
            key={key.id}
            title={t("newClient")}
            value={key.value}
            close={() => state.closeClient(key.id)}
            seen={() => state.seenClient(key.id)}
          />
        ))}
      {keys.loading && !keys.data && <Notice tone="muted">{t("loading")}</Notice>}
      {keys.data &&
        (keys.data.keys.length === 0 ? (
          <Empty>{t("empty")}</Empty>
        ) : (
          <List
            label={t("clients")}
            items={keys.data.keys.map((key, index) => ({ ...key, number: index + 1 }))}
            id={(key) => key.id}
            columns={[
              { label: t("number"), value: (key) => t("row", { number: key.number }) },
              { label: t("key"), value: (key) => key.masked, mono: true },
              {
                label: t("actions"),
                value: (key) => (
                  <ConfirmButton disabled={state.adding || deleting} onConfirm={() => remove(key.id)}>
                    {t("remove")}
                  </ConfirmButton>
                ),
              },
            ]}
          />
        ))}
    </Card>
  );
}
export function ConfigConnectivity(props: {
  state: KeysState;
  tab: string;
  search: string;
  onChanged: () => void;
}) {
  const { host } = useKit();
  const canRotate = host.features.has("rotateManagementKey");
  const shown = keyCards(props.tab, props.search);
  const state = props.state;
  const value = state.management?.tab === props.tab ? state.management : undefined;
  return (
    <div class="stack">
      {shown.clients && <ClientKeys state={state} tab={props.tab} onChanged={props.onChanged} />}
      {shown.management && (
        <Card
          title={t("management")}
          actions={
            canRotate && (
              <Button disabled={state.rotating || !!state.management} onClick={state.rotate}>
                {t("rotate")}
              </Button>
            )
          }
        >
          <p class="muted">{canRotate ? t("rotationHint") : t("rotationUnavailable")}</p>
          {state.rotationError && <Notice tone="danger">{state.rotationError}</Notice>}
          {value && (
            <KeyNotice
              key={value.id}
              title={t("newManagement")}
              value={value.value}
              close={state.closeManagement}
              seen={state.seenManagement}
            />
          )}
        </Card>
      )}
    </div>
  );
}
