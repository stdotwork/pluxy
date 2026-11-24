// Clients: what a coding tool needs to send its requests through pluxy. The address, the client
// keys (made and deleted here), and for each tool pluxy knows, the text to paste into its
// settings. pluxy changes no tool's files from this page: it shows the text, and the person pastes it.

import { useEffect, useState } from "preact/hooks";
import { type ClientKey, problem, type SetupClient } from "../../api";
import { useCopy, useKit } from "../../ui";
import { useCore } from "./core";
import { useRead } from "./data";
import { moment, num, plural } from "./lib";
import { Asking, Btn, Confirm, Note, Section, Trouble } from "./parts";

function CopyButton(props: { value: string; children?: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <span class="db-copy">
      <Btn kind="quiet" icon={state === "copied" ? "check" : "copy"} onClick={copy}>
        {state === "copied" ? "Copied" : (props.children ?? "Copy")}
      </Btn>
      {state === "failed" && (
        <span class="db-fix-error" role="alert">
          The browser would not copy it. Select the text and copy it yourself.
        </span>
      )}
    </span>
  );
}

const CLIENT_STATE: Record<string, string> = {
  configured: "Points at pluxy",
  "not-configured": "Installed, not pointed at pluxy yet",
  "needs-update": "Points at pluxy, but its settings are out of date",
  refused: "Installed. Its settings hold something pluxy does not expect, so paste the text yourself",
};

/** The text to paste for one tool, with the new key in place of the placeholder when one was just made. */
function Paste(props: { client: SetupClient; fresh?: string }) {
  const { api } = useKit();
  const paste = useRead(() => api.clientPaste(props.client.id), 0, props.client.id);
  const data = paste.data;
  if (paste.loading && !data) return <Asking lines={3}>Asking pluxy for the text…</Asking>;
  if (!data) return <Trouble retry={paste.reload}>{paste.error ?? "pluxy gave no text."}</Trouble>;
  return (
    <div class="db-paste">
      {data.note && <p class="db-small">{data.note}</p>}
      {data.files.map((file) => {
        const text = props.fresh ? file.content.split(data.key).join(props.fresh) : file.content;
        return (
          <figure key={file.path}>
            <figcaption>
              <span>
                Put this in <span class="db-code">{file.path}</span>
              </span>
              <CopyButton value={text} />
            </figcaption>
            {/* biome-ignore lint/a11y/noNoninteractiveTabindex: a long file scrolls sideways, and keys must reach it */}
            <pre class="db-pre" tabIndex={0}>
              {text}
            </pre>
          </figure>
        );
      })}
      <p class="db-small">
        {props.fresh ? (
          "The key you just made is already in the text."
        ) : (
          <>
            Replace <span class="db-code">{data.key}</span> with a client key. A key is shown once, when it is
            made: make one above and this text will carry it.
          </>
        )}
      </p>
    </div>
  );
}

export function Clients() {
  const { api, host, unauthorized } = useKit();
  const core = useCore();
  const keys = useRead(api.clientKeys);
  const setup = useRead(api.setup);
  const week = useRead(() => api.usage("since=7d"));
  const [fresh, setFresh] = useState<{ key: ClientKey; value: string }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [open, setOpen] = useState<string>();

  const scanning = setup.data?.scanning === true;
  const reloadSetup = setup.reload;
  useEffect(() => {
    if (!scanning) return;
    const timer = setTimeout(reloadSetup, 1000);
    return () => clearTimeout(timer);
  }, [scanning, reloadSetup]);

  const make = async () => {
    setBusy(true);
    setError(undefined);
    const r = await api.addClientKey();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    setError(problem(r));
    if (r.body) {
      setFresh({ key: r.body.key, value: r.body.value });
      keys.reload();
    }
  };
  const remove = async (id: string) => {
    setBusy(true);
    setError(undefined);
    const r = await api.deleteClientKey(id);
    setBusy(false);
    if (r.status === 401) return unauthorized();
    setError(problem(r));
    if (fresh?.key.id === id) setFresh(undefined);
    keys.reload();
  };
  const look = async () => {
    setBusy(true);
    const r = await api.scanSetup();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    setError(problem(r));
    setup.reload();
  };

  const used = new Map<string, number>();
  for (const g of week.data?.keys ?? []) used.set(g.name, g.total_requests);
  const list = keys.data?.keys;
  const clients = setup.data?.found.clients ?? [];
  const address = host.address;

  return (
    <article class="db-page db-clients">
      <header class="db-page-head">
        <div>
          <h1>Clients</h1>
          <p class="db-lead">
            A client is a coding tool, such as Claude Code or Codex, that sends its requests through pluxy. It
            needs two things: pluxy's address and a client key.
          </p>
        </div>
      </header>

      {error && <Trouble quiet>{error}</Trouble>}

      <div class="db-spread">
        <div>
          {address && (
            <Section title="Address">
              <p class="db-address">
                <span class="db-code">{address}</span>
                <CopyButton value={address} />
              </p>
              <p class="db-small">
                {setup.data?.found.core.reachable_from_network
                  ? "Other machines on your network can reach pluxy at this machine's address."
                  : "Only programs on this machine can reach it."}
              </p>
            </Section>
          )}

          <Section
            title="Client keys"
            aside={
              <Btn kind="primary" icon="key" disabled={busy} onClick={make}>
                Make a key
              </Btn>
            }
          >
            {fresh && (
              <div class="db-fresh" role="status">
                <p>
                  <b>Here is the new key.</b> pluxy shows it only this once, so copy it now.
                </p>
                <p class="db-address">
                  <span class="db-code">{fresh.value}</span>
                  <CopyButton value={fresh.value} />
                </p>
              </div>
            )}
            {!list && keys.loading && <Asking lines={2}>Asking pluxy for its keys…</Asking>}
            {!list && keys.error && <Trouble retry={keys.reload}>{keys.error}</Trouble>}
            {list && list.length === 0 && (
              <p class="db-quiet">
                No client keys. Without one, pluxy takes requests from any program that can reach it.
              </p>
            )}
            {list && list.length > 0 && (
              <ul class="db-keys">
                {list.map((k) => (
                  <li key={k.id}>
                    <span class="db-code">{k.masked}</span>
                    <span class="db-keys-used">
                      {week.data
                        ? used.get(k.id)
                          ? `${plural(used.get(k.id) ?? 0, "request")} this week`
                          : "Not used this week"
                        : ""}
                      {fresh?.key.id === k.id ? " · new" : ""}
                    </span>
                    <Confirm
                      danger
                      ask="Tools that use this key stop working."
                      yes="Delete it"
                      disabled={busy}
                      label={`Delete key ${k.masked}`}
                      onConfirm={() => remove(k.id)}
                    >
                      Delete
                    </Confirm>
                  </li>
                ))}
              </ul>
            )}
            {list && <p class="db-small">{num(list.length)} in all. A key's value is never shown again.</p>}
          </Section>
        </div>

        <div>
          <Section
            title="Coding tools"
            aside={
              setup.data && (
                <button type="button" class="db-linkish" disabled={busy || scanning} onClick={look}>
                  {scanning ? "Looking…" : setup.data.scanned_at ? "Look again" : "Look for them"}
                </button>
              )
            }
          >
            {!setup.data && setup.loading && <Asking lines={3}>Asking pluxy which tools it knows…</Asking>}
            {!setup.data && setup.error && (
              <Note>
                This pluxy cannot look for coding tools. Any tool that speaks the OpenAI, Claude or Gemini API
                works: give it the address as its base URL and a client key as its API key.
              </Note>
            )}
            {setup.data && clients.length === 0 && (
              <p class="db-quiet">
                {setup.data.scanned_at
                  ? `pluxy found no coding tools on its machine when it looked ${moment(Date.parse(setup.data.scanned_at), core.now)}.`
                  : "pluxy has not looked for coding tools on its machine yet."}
              </p>
            )}
            <ul class="db-tools">
              {clients.map((c) => (
                <li key={c.id} data-open={open === c.id ? "" : undefined}>
                  <div class="db-tool-line">
                    <div>
                      <b class="db-name">{c.name}</b>
                      {c.version && <small> {c.version}</small>}
                      <p>{c.installed ? (CLIENT_STATE[c.state] ?? c.state) : "Not found on this machine"}</p>
                    </div>
                    <Btn
                      kind={open === c.id ? "quiet" : "plain"}
                      onClick={() => setOpen(open === c.id ? undefined : c.id)}
                      label={`${open === c.id ? "Hide" : "Show"} what to paste for ${c.name}`}
                    >
                      {open === c.id ? "Hide" : "Show what to paste"}
                    </Btn>
                  </div>
                  {open === c.id && <Paste client={c} fresh={fresh?.value} />}
                </li>
              ))}
            </ul>
            {setup.data && (
              <p class="db-small">
                Any other tool that speaks the OpenAI, Claude or Gemini API works too: give it the address as
                its base URL and a client key as its API key.
              </p>
            )}
          </Section>
        </div>
      </div>
    </article>
  );
}
