// Clients: the start of the line. Each client key with the requests it sent in the range and
// what it asked for most. Making and removing keys, and the text to paste into each coding tool,
// are today's pages, linked from here.

import { useKit } from "../../ui";
import { useAsk, useFleet, useFlow, useNow, useResource } from "./data";
import { Icon } from "./icons";
import { type Address, answered, compact, count, hashFor, RANGES, share } from "./lib";
import { useRange } from "./overview";
import { Btn, Note, PageHead, Refresh, Segmented } from "./parts";

export function Clients(props: { address: Address }) {
  const { host, api } = useKit();
  const ask = useAsk();
  const fleet = useFleet();
  const now = useNow();
  const range = useRange(props.address);
  const flow = useFlow(range, {});
  const keys = useResource("client-keys", () => ask(api.clientKeys()));
  const list = keys.data?.keys ?? fleet.keys;
  const s = flow.data?.summary;
  const byKey = new Map((s?.keys ?? []).map((g) => [g.name, g]));
  const most = Math.max(1, ...(s?.keys ?? []).map((g) => g.total_requests));
  const topModels = (id: string) =>
    (flow.data?.keyModel ?? [])
      .filter((l) => l.from === id)
      .sort((a, b) => b.requests - a.requests)
      .slice(0, 3);
  const sentence = !list
    ? keys.error
      ? "The client keys could not be read."
      : "Reading client keys…"
    : list.length === 0
      ? "No client keys yet."
      : `${list.length} client ${list.length === 1 ? "key" : "keys"}. A coding tool sends its requests with one.`;
  return (
    <div class="dc-page" data-view="clients">
      <PageHead title="Clients" sub={sentence}>
        <Segmented
          label="Range"
          value={range.id}
          options={RANGES.map((r) => ({ value: r.id, label: r.id, title: r.words }))}
          onChange={(v) => {
            host.prefs.set("design-c.range", v);
            location.hash = hashFor("clients", { range: v });
          }}
        />
        <Refresh
          at={flow.at}
          now={now}
          busy={flow.loading || keys.loading}
          onRefresh={() => {
            keys.reload();
            flow.reload();
          }}
        />
        <Btn kind="primary" icon="key" href="#/config/connectivity">
          Manage keys
        </Btn>
      </PageHead>

      {keys.error && (
        <Note
          tone="danger"
          action={
            <Btn size="small" onClick={keys.reload}>
              Try again
            </Btn>
          }
        >
          Reading the client keys failed: {keys.error}.
        </Note>
      )}
      {flow.error && (
        <Note tone="warn">The requests could not be read, so the shares are missing: {flow.error}.</Note>
      )}

      {list && list.length === 0 ? (
        <section class="dc-card dc-empty">
          <h2>No client keys yet</h2>
          <p>
            A client key is what a coding tool puts where it would put a provider's API key. Make one, then
            paste it into the tool.
          </p>
          <Btn kind="primary" icon="key" href="#/config/connectivity">
            Manage keys
          </Btn>
        </section>
      ) : (
        <section class="dc-card dc-lane" aria-label="Client keys">
          <ul class="dc-rows dc-keys">
            {!list && (
              <li class="dc-row" role="status">
                <span class="dc-sr">Reading client keys</span>
                <span class="dc-skel dc-skel-line" />
                <span class="dc-skel dc-skel-word" />
              </li>
            )}
            {(list ?? []).map((k) => {
              const g = byKey.get(k.id);
              const models = topModels(k.id);
              return (
                <li key={k.id} class="dc-row">
                  <div class="dc-row-who">
                    <span class="dc-row-name dc-mono">
                      <Icon name="key" size={15} /> {k.masked}
                    </span>
                    <span class="dc-row-sub dc-mono dc-ellipsis" title={`Key ID ${k.id}`}>
                      {k.id}
                    </span>
                  </div>
                  <div class="dc-row-use">
                    <span class="dc-num">{g ? count(g.total_requests) : s ? "0" : "–"}</span>
                    <span class="dc-row-sub">
                      {g && s
                        ? `requests · ${share(g.total_requests, s.total_requests)} of all · ${answered(g.success_count, g.total_requests)} answered`
                        : s
                          ? `requests in ${range.words}`
                          : ""}
                    </span>
                    {g && (
                      <span class="dc-bar">
                        <i style={{ width: `${((g.total_requests - g.failure_count) / most) * 100}%` }} />
                        {g.failure_count > 0 && (
                          <i data-failed="" style={{ width: `${(g.failure_count / most) * 100}%` }} />
                        )}
                      </span>
                    )}
                  </div>
                  <div class="dc-key-models">
                    {models.length > 0 && <span class="dc-row-sub">Asks most for</span>}
                    {models.map((m) => (
                      <span key={m.to} class="dc-key-model">
                        <span class="dc-ellipsis" title={m.to}>
                          {m.to}
                        </span>
                        <span class="dc-num dc-muted">{compact(m.requests)}</span>
                      </span>
                    ))}
                  </div>
                  <div class="dc-row-act">
                    <a href={hashFor("map", { range: range.id, key: k.id })}>On the map</a>
                    <a href={hashFor("activity", { range: range.id, key: k.id })}>Requests</a>
                  </div>
                </li>
              );
            })}
          </ul>
        </section>
      )}

      <section class="dc-card dc-card-pad dc-next" aria-label="Connect a coding tool">
        <div>
          <h2 class="dc-h2">Connect a coding tool</h2>
          <p class="dc-muted">
            Point the tool at {host.address ? <code class="dc-mono">{host.address}</code> : "pluxy's address"}{" "}
            and give it a client key. Setup has the exact text to paste for each tool it finds.
          </p>
        </div>
        <Btn href="#/setup" icon="go">
          Set up a tool
        </Btn>
      </section>
    </div>
  );
}
