// The app's Connection page: how the app reaches core. Three modes:
// - managed: the app runs a core of its own, with a config it keeps in its data folder, and
//   stops it when the app quits;
// - local: a core already running on this computer, such as a system service; the app only
//   connects, and leaves it running;
// - remote: a core on another computer, over https; shown with its address while connected.

import { useId, useState } from "preact/hooks";
import { Button, Card, Choices, Facts, Notice, PageHead, Switch } from "../../ui/kit/ui";
import { type Connection, type ConnectionChange, connectionSet, coreRestart, type Mode } from "./host";

const MODES = [
  { value: "managed" as const, label: "Run core here", note: "the app starts and stops it" },
  { value: "local" as const, label: "Core on this computer", note: "already running" },
  { value: "remote" as const, label: "Remote core", note: "over https" },
];

function Field(props: {
  label: string;
  hint?: string;
  value: string;
  secret?: boolean;
  onInput: (v: string) => void;
}) {
  const id = useId();
  const common = {
    id,
    value: props.value,
    autocomplete: "off",
    spellcheck: false,
    onInput: (e: { currentTarget: HTMLInputElement }) => props.onInput(e.currentTarget.value),
  };
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      {props.secret ? <input type="password" {...common} /> : <input type="text" {...common} />}
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function describe(c: Connection): [string, string][] {
  const rows: [string, string][] = [];
  if (c.target) rows.push(["Core", c.target]);
  if (c.mode === "managed" && c.core) {
    rows.push(["Process", c.core.state === "running" ? `running, pid ${c.core.pid}` : c.core.state]);
    if (c.core.detail) rows.push(["Last words", c.core.detail]);
  }
  if (c.mode === "local" || c.mode === "remote")
    rows.push(["Key", c.remembered ? "kept on this device" : "this session only"]);
  return rows;
}

export function ConnectionPage(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const [mode, setMode] = useState<Mode | undefined>(c.mode);
  const [url, setUrl] = useState(c.url ?? "");
  const [key, setKey] = useState("");
  const [remember, setRemember] = useState(c.remembered);
  const [caFile, setCaFile] = useState(c.ca_file ?? "");
  const [corePath, setCorePath] = useState(c.core_path ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  async function apply(run: () => Promise<Connection>) {
    setBusy(true);
    setError(undefined);
    try {
      const next = await run();
      if (next.error) setError(next.error);
      else setKey(""); // the native side has it now
      props.onChange(next);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  const change = (): ConnectionChange => {
    if (mode === "managed") return { mode, core_path: corePath || undefined };
    return {
      mode: mode ?? "local",
      url,
      key: key || undefined,
      remember,
      ca_file: mode === "remote" ? caFile || undefined : undefined,
    };
  };

  return (
    <>
      <PageHead title="Connection" />
      {c.mode && (
        <Card title="Now">
          <Facts rows={describe(c)} />
          {c.error && <Notice tone="danger">{c.error}</Notice>}
          {c.target && !c.signed_in && (
            <Notice tone="warn">Not signed in to this core: give its management key below.</Notice>
          )}
          {c.mode === "managed" && (
            <div class="actions">
              <Button disabled={busy} onClick={() => apply(coreRestart)}>
                {c.core?.state === "running" ? "Restart core" : "Start core"}
              </Button>
            </div>
          )}
        </Card>
      )}
      <Card title={c.mode ? "Change" : "How should the app reach core?"}>
        <form
          aria-label="Connection"
          onSubmit={(e) => {
            e.preventDefault();
            apply(() => connectionSet(change()));
          }}
        >
          <Choices label="Mode" value={mode} choices={MODES} onChange={setMode} />
          {mode === "managed" && (
            <Field
              label="Core program"
              hint="Empty: the pluxy program installed beside the app."
              value={corePath}
              onInput={setCorePath}
            />
          )}
          {(mode === "local" || mode === "remote") && (
            <>
              <Field
                label="Address"
                hint={
                  mode === "local"
                    ? "This computer only, such as http://127.0.0.1:8400."
                    : "https only, such as https://pluxy.example."
                }
                value={url}
                onInput={setUrl}
              />
              <Field
                label="Management key"
                hint={c.mode === mode && c.url === url ? "Empty: keep the key the app has." : undefined}
                secret
                value={key}
                onInput={setKey}
              />
              <Switch label="Keep the key on this device" checked={remember} onChange={setRemember} />
            </>
          )}
          {mode === "remote" && (
            <Field
              label="Certificate authority (optional)"
              hint="A PEM file to trust for this core, for a certificate you made yourself."
              value={caFile}
              onInput={setCaFile}
            />
          )}
          {error && <Notice tone="danger">{error}</Notice>}
          <Button type="submit" variant="primary" disabled={busy || !mode}>
            {busy ? "Connecting…" : "Connect"}
          </Button>
        </form>
      </Card>
    </>
  );
}
