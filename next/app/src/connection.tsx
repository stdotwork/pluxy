// The app's Connection page: how the app reaches core. Three modes:
// - managed: the app runs a core of its own, one of the versions it installed from the core
//   source or adopted, with a config it keeps in its data folder, and stops it when the app quits;
// - local: a core already running on this computer, such as a system service; the app only
//   connects, and leaves it running;
// - remote: a core on another computer, over https; shown with its address while connected.

import { useEffect, useId, useRef, useState } from "preact/hooks";
import { Badge, Button, Card, Choices, Empty, Facts, Notice, PageHead } from "../../ui/kit/ui";
import {
  type Connection,
  type ConnectionChange,
  connectionSet,
  coreAdopt,
  coreInstall,
  coreRestart,
  coreRollback,
  coreVersions,
  keyEntry,
  type Mode,
  type Versions,
} from "./host";

const MODES = [
  { value: "managed" as const, label: "Run core here", note: "the app starts and stops it" },
  { value: "local" as const, label: "Core on this computer", note: "already running" },
  { value: "remote" as const, label: "Remote core", note: "over https" },
];

function Field(props: { label: string; hint?: string; value: string; onInput: (v: string) => void }) {
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
      <input type="text" {...common} />
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function describe(c: Connection): [string, string][] {
  const rows: [string, string][] = [];
  if (c.target) rows.push(["Core", c.target]);
  if (c.mode === "managed" && c.core) {
    const process = {
      running: `running, pid ${c.core.pid}`,
      stopped: "stopped",
      missing: "not installed",
    };
    rows.push(["Process", process[c.core.state]]);
    if (c.core.version) rows.push(["Current version", c.core.version]);
    if (c.core.detail) rows.push(["Last words", c.core.detail]);
  }
  if (c.mode === "local" || c.mode === "remote")
    rows.push(["Key", c.remembered ? "kept on this device" : "this session only"]);
  return rows;
}

const OUTCOMES = { done: "Done", cancelled: "Cancelled: nothing changed", failed: "Failed" };

/** The app's own core versions: what is installed and runs, what the core source offers, and a
 * core program waiting to be adopted. Each change opens the native confirmation. */
function CoreVersions(props: { connection: Connection }) {
  const c = props.connection;
  const [v, setV] = useState<Versions>();
  const [error, setError] = useState<string>();
  // The last outcome's number when a change was asked for: the change waits for its
  // confirmation until a later outcome arrives.
  const [waiting, setWaiting] = useState<number>();
  const latest = useRef(0);
  // The connection changes when a change has ended (pluxy-connection): read the versions again.
  // Only the latest read counts.
  useEffect(() => {
    const n = ++latest.current;
    coreVersions().then(
      (next) => n === latest.current && setV(next),
      (e) => n === latest.current && setError(String(e)),
    );
  }, [c]);
  const seq = v?.outcome?.seq ?? 0;
  const asked = waiting !== undefined && seq <= waiting;
  async function ask(run: () => Promise<void>) {
    setError(undefined);
    setWaiting(seq);
    try {
      await run();
    } catch (e) {
      setWaiting(undefined);
      setError(String(e));
    }
  }
  if (!v)
    return error ? (
      <Card title="Core versions">
        <Notice tone="danger">{error}</Notice>
      </Card>
    ) : null;
  const running = c.core?.state === "running" ? c.core.version : undefined;
  const installed = new Set(v.installed.map((i) => i.id));
  const available = new Set(v.available.map((a) => a.version));
  const offered = v.available.filter((a) => !installed.has(a.version));
  const verb = running ? "Update to" : "Install";
  return (
    <Card title="Core versions">
      {c.core?.adopt && (
        <>
          <Notice tone="warn">
            The app runs {c.core.adopt} only once it is copied into the app's own cores, and runs that copy.
          </Notice>
          <div class="actions">
            <Button disabled={asked} onClick={() => ask(coreAdopt)}>
              Adopt this core program
            </Button>
          </div>
        </>
      )}
      {v.failed && (
        <Notice tone="danger">
          Core {v.failed.version} did not start: {v.failed.reason}
        </Notice>
      )}
      {v.installed.length === 0 ? (
        <Empty>No core installed yet.</Empty>
      ) : (
        <Facts
          rows={v.installed.map((i) => [
            i.id,
            <span key={i.id}>
              {i.kind === "adopted" ? `adopted from ${i.adopted_from}` : "signed release"}{" "}
              {i.id === running && <Badge tone="ok">running</Badge>}
              {i.id === v.previous && <Badge tone="muted">previous</Badge>}{" "}
              {i.id !== running && (
                <Button disabled={asked} onClick={() => ask(() => coreRollback(i.id))}>
                  Roll back to {i.id}
                </Button>
              )}{" "}
              {i.kind === "signed" && available.has(i.id) && (
                <Button disabled={asked} onClick={() => ask(() => coreInstall(i.id))}>
                  Install {i.id} again
                </Button>
              )}
            </span>,
          ])}
        />
      )}
      {v.source ? (
        offered.length > 0 && (
          <div class="actions">
            {offered.map((a) => (
              <Button key={a.version} disabled={asked} onClick={() => ask(() => coreInstall(a.version))}>
                {verb} {a.version}
              </Button>
            ))}
          </div>
        )
      ) : (
        <Notice tone="muted">Set a core source below to install a core.</Notice>
      )}
      {v.source_error && <Notice tone="danger">{v.source_error}</Notice>}
      {asked && <Notice tone="muted">Confirm or cancel in the pluxy native confirmation window.</Notice>}
      {v.outcome && !asked && (
        <Notice tone={v.outcome.result === "failed" ? "danger" : "muted"}>
          {OUTCOMES[v.outcome.result]} ({v.outcome.action} {v.outcome.version})
          {v.outcome.detail ? `: ${v.outcome.detail}` : ""}
        </Notice>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
    </Card>
  );
}

export function ConnectionPage(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const [mode, setMode] = useState<Mode | undefined>(c.mode);
  const [url, setUrl] = useState(c.url ?? "");
  const [caFile, setCaFile] = useState(c.ca_file ?? "");
  const [corePath, setCorePath] = useState(c.core_path ?? "");
  const [coreSource, setCoreSource] = useState(c.core_source ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  async function apply(run: () => Promise<Connection>) {
    setBusy(true);
    setError(undefined);
    try {
      const next = await run();
      if (next.error) setError(next.error);
      props.onChange(next);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  const change = (): ConnectionChange => {
    if (mode === "managed")
      return { mode, core_path: corePath || undefined, core_source: coreSource || undefined };
    return {
      mode: mode ?? "local",
      url,
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
            <Notice tone="warn">
              Not signed in to this core: enter its management key in the native dialog.
            </Notice>
          )}
          {c.mode === "managed" && c.core?.state !== "missing" && (
            <div class="actions">
              <Button disabled={busy} onClick={() => apply(coreRestart)}>
                {c.core?.state === "running" ? "Restart core" : "Start core"}
              </Button>
            </div>
          )}
        </Card>
      )}
      {c.mode === "managed" && <CoreVersions connection={c} />}
      <Card title={c.mode ? "Change" : "How should the app reach core?"}>
        <form
          aria-label="Connection"
          onSubmit={(e) => {
            e.preventDefault();
            apply(async () => {
              const next = change();
              if (next.mode === "managed") return connectionSet(next);
              const result = await keyEntry(next);
              return result.connection;
            });
          }}
        >
          <Choices label="Mode" value={mode} choices={MODES} onChange={setMode} />
          {mode === "managed" && (
            <>
              <Field
                label="Core source"
                hint="The full path of an index of signed core releases. The app has no built-in source."
                value={coreSource}
                onInput={setCoreSource}
              />
              <Field
                label="Core program (optional)"
                hint="A core program of your own, by full path, to adopt: the app copies it into its own cores and runs the copy. Empty: the pluxy program beside the app."
                value={corePath}
                onInput={setCorePath}
              />
            </>
          )}
          {(mode === "local" || mode === "remote") && (
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
            {busy ? "Connecting…" : mode === "managed" ? "Connect" : "Enter management key"}
          </Button>
        </form>
      </Card>
    </>
  );
}
