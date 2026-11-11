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
  copyFailed: "Could not copy. Select the key and copy it manually.",
});

function KeyNotice(props: { title: string; value: string; close: () => void }) {
  const [copied, setCopied] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <div class="stack config-key-notice" data-part="key-notice" role="status" aria-label={props.title}>
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
        {copied && <span>{t("copied")}</span>}
      </div>
      {failed && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </div>
  );
}

export function ClientKeys(props: { onChanged: () => void; search?: string }) {
  const { api, unauthorized } = useKit();
  const keys = useLoad(api.clientKeys);
  const [value, setValue] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const live = useRef(true);
  useEffect(
    () => () => {
      live.current = false;
    },
    [],
  );
  const add = async () => {
    setBusy(true);
    setError(undefined);
    setValue(undefined);
    const reply = await api.addClientKey();
    if (!live.current) return;
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    setError(problem(reply));
    if (reply.body) {
      setValue(reply.body.value);
      keys.reload();
      props.onChanged();
    }
  };
  const remove = async (id: string) => {
    setBusy(true);
    setError(undefined);
    setValue(undefined);
    const reply = await api.deleteClientKey(id);
    if (!live.current) return;
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    setError(why);
    if (!why) {
      keys.reload();
      props.onChanged();
    }
  };
  if (props.search?.trim() && !t("clients").toLowerCase().includes(props.search.trim().toLowerCase()))
    return null;
  return (
    <Card
      title={t("clients")}
      actions={
        <Button disabled={busy || keys.loading} onClick={add}>
          {t("add")}
        </Button>
      }
    >
      {(error || keys.error) && <Notice tone="danger">{error ?? keys.error}</Notice>}
      {value && <KeyNotice title={t("newClient")} value={value} close={() => setValue(undefined)} />}
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
                  <ConfirmButton disabled={busy} onConfirm={() => remove(key.id)}>
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

export function ConfigConnectivity(props: { onChanged: () => void; search?: string }) {
  const { host, api, unauthorized } = useKit();
  const [value, setValue] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const live = useRef(true);
  useEffect(
    () => () => {
      live.current = false;
    },
    [],
  );
  const canRotate = host.features.has("rotateManagementKey");
  const rotate = async () => {
    if (!canRotate) return;
    setBusy(true);
    setError(undefined);
    setValue(undefined);
    const reply = await api.rotateManagementKey();
    if (!live.current) return;
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    setError(problem(reply));
    if (reply.body) {
      setValue(reply.body.value);
      props.onChanged();
    }
  };
  return (
    <div class="stack">
      <ClientKeys onChanged={props.onChanged} search={props.search} />
      {(!props.search?.trim() ||
        t("management").toLowerCase().includes(props.search.trim().toLowerCase())) && (
        <Card
          title={t("management")}
          actions={
            canRotate && (
              <Button disabled={busy} onClick={rotate}>
                {t("rotate")}
              </Button>
            )
          }
        >
          <p class="muted">{canRotate ? t("rotationHint") : t("rotationUnavailable")}</p>
          {error && <Notice tone="danger">{error}</Notice>}
          {value && <KeyNotice title={t("newManagement")} value={value} close={() => setValue(undefined)} />}
        </Card>
      )}
    </div>
  );
}
