// What the client setup pages (Claude Code, Claude Desktop, Codex, Grok Build, Hermes Agent,
// Oh My Pi, OpenCode, Pi) share, so a fix to one is a fix to all: a vault secret and the keys a page
// offers from it, a text field, the Start CLI card of the pages that start a client plainly, and
// the file preview that is tied to the files the page's fields select.

import { useEffect, useId, useRef, useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { Button, Card, Choices, Notice, useAction, useLoad } from "../../ui/kit/ui";
import { folderWait } from "./clientHelp";
import { reply } from "./host";
import { keptTerminal, launcherProblem, useDeviceChanged } from "./startCli";

/** A vault secret as `vault_secrets` lists it: never its value. */
export interface Secret {
  id: string;
  label: string;
  /** Set for a captured client key: the package it may be given to. */
  client_key?: { client: string };
}

/** The keys a page offers from `vault_secrets`'s answer (`{secrets: [...]}`): plain secrets, and
 * client keys bound to its package pkg; a client key bound to another package would be refused. */
export function keysOf(pkg: string, body: { secrets?: Secret[] } | undefined): Secret[] {
  return (body?.secrets ?? []).filter((s) => !s.client_key || s.client_key.client === pkg);
}

/** A labelled text field: one line, or with `area` a few lines; `list` names a datalist. */
export function Text(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  list?: string;
  area?: boolean;
}) {
  const id = useId();
  const input = (e: { currentTarget: { value: string } }) => props.onInput(e.currentTarget.value);
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      {props.area ? (
        <textarea id={id} rows={4} spellcheck={false} value={props.value} onInput={input} />
      ) : (
        <input
          id={id}
          type="text"
          autocomplete="off"
          spellcheck={false}
          list={props.list}
          value={props.value}
          onInput={input}
        />
      )}
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

/** The texts a Start CLI card takes from its page's strings. */
export type StartCliText = (
  key: "start" | "cwd" | "recent" | "override" | "overrideHint" | "terminal" | "launch",
) => string;

/**
 * The Start CLI card of a page that starts its client in a terminal on this device: the folder, the
 * program path, the terminal, and the launch the client's tool recipes. `client` is the name the
 * launcher keeps recent folders under; `recipe` asks the tool for the launch.
 */
export function StartCli(props: {
  available: boolean;
  client: string;
  recipe: () => Promise<Reply<Record<string, unknown>>>;
  t: StartCliText;
}) {
  const { t } = props;
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  useDeviceChanged(terminals.reload);
  const action = useAction(state.reload);
  const recent = state.data?.history[props.client] ?? [];
  const shownTerminal = keptTerminal(terminal, terminals.data?.terminals);
  const start = () =>
    action.run(async () => {
      const recipe = await props.recipe();
      if (!recipe.body) return recipe;
      return reply("launcher_launch", {
        launch: {
          ...recipe.body,
          cwd: cwd.trim(),
          terminal: shownTerminal,
          ...(override.trim() ? { override: override.trim() } : {}),
        },
      });
    });
  return (
    <Card title={t("start")}>
      <Text label={t("cwd")} hint={folderWait(cwd)} value={cwd} onInput={setCwd} />
      {recent.length > 0 && (
        <Choices
          label={t("recent")}
          value={recent.includes(cwd) ? cwd : undefined}
          choices={recent.map((r) => ({ value: r, label: r }))}
          onChange={setCwd}
        />
      )}
      <Text label={t("override")} hint={t("overrideHint")} value={override} onInput={setOverride} />
      {terminals.data && (
        <Choices
          label={t("terminal")}
          value={shownTerminal}
          choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
          onChange={setTerminal}
        />
      )}
      {terminals.error && <Notice tone="warn">{launcherProblem(terminals.error)}</Notice>}
      {action.error && <Notice tone="danger">{launcherProblem(action.error)}</Notice>}
      <Button variant="primary" onClick={start} disabled={action.busy || !props.available || !cwd.trim()}>
        {t("launch")}
      </Button>
    </Card>
  );
}

/**
 * The file preview of a client page, tied to the files its fields select. `key` names that selection
 * (the request the page sends to read it, with anything else that makes another file: the
 * environment); the page passes it on every render. The preview is the last one `readFor` took for
 * the current key, and is gone as soon as the key changes, so the page never offers a write for
 * files its fields no longer show.
 *
 * `readFor(readKey, call)` runs a read: `readKey` is the key the read was started with, and `call`
 * makes the request. An answer, successful or failed, for a selection the person has since left is
 * no result (an empty success), so the page shows neither its preview nor its error. `isCurrent`
 * says the same for a page that acts on the answer itself.
 */
export function usePreview<P>(key: string) {
  const [held, setHeld] = useState<{ key: string; preview: P }>();
  const latest = useRef(key);
  latest.current = key;
  // The preview shown is always the current key's; a changed key also drops what was held, so
  // returning to the old selection does not bring the old preview back.
  useEffect(() => setHeld((h) => (h && h.key !== key ? undefined : h)), [key]);
  const isCurrent = (readKey: string) => readKey === latest.current;
  const readFor = async (readKey: string, call: () => Promise<Reply<P>>): Promise<Reply<P>> => {
    const r = await call();
    if (!isCurrent(readKey)) return { status: 200 };
    if (r.body) setHeld({ key: readKey, preview: r.body });
    return r;
  };
  return { preview: held?.key === key ? held.preview : undefined, readFor, isCurrent };
}
