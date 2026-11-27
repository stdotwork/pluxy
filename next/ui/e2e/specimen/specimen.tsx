// A test-only specimen of the shared parts (kit/parts) in each of their states, for parts.spec.ts.
// The spec builds this file and runs it inside the real panel, signed in, so the parts are drawn
// with the panel's own styles and its current theme. It renders into the element #specimen.

import { render } from "preact";
import type { Account, QuotaLimit, SetupClient, UsageEvent } from "../../kit/api";
import {
  AccountRow,
  RequestRow,
  SettingRow,
  StaleNotice,
  StatePane,
  ToolRow,
  useSetting,
  type Windows,
} from "../../kit/parts";
import { Button, ConfirmButton, Switch } from "../../kit/ui";

const now = Date.now();
const iso = (ms: number) => new Date(ms).toISOString();
const MIN = 60_000;
const HOUR = 60 * MIN;
const secs = (ms: number) => ms / 1000;

const account = (id: string, fields: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: true,
  extension: "pluxy/claude",
  ...fields,
});

const limits = (five: number, week: number): QuotaLimit[] => [
  { id: "five_hour", window_seconds: 5 * 3600, used_percent: five, resets_at: secs(now + 85 * MIN) },
  { id: "seven_day", window_seconds: 7 * 86400, used_percent: week, resets_at: secs(now + 3 * 24 * HOUR) },
];
const read = (five: number, week: number, plan?: string): Windows => ({
  state: "read",
  limits: limits(five, week),
  plan,
});

const LONG = `${"a-very-long-account-name-".repeat(9)}that-goes-on@example.com`.slice(0, 230);

const ACCOUNTS: { a: Account; w?: Windows; requests?: number }[] = [
  { a: account("acc_0000sign", { label: "work@example.com", health: { state: "needs_login" } }) },
  { a: account("acc_0000conf", { label: "Team key", source: "config", health: { state: "needs_login" } }) },
  {
    a: account("acc_0000ban0", {
      label: "old@example.com",
      extension: "pluxy/codex",
      health: { state: "banned" },
    }),
  },
  { a: account("claude-broken.json", { health: { state: "broken" } }) },
  {
    a: account("acc_0000cool", {
      label: "cooling@example.com",
      cooldowns: [{ scope: "account", kind: "rate_limit", until: iso(now + 25 * MIN) }],
    }),
    w: read(40, 20, "Pro"),
    requests: 87,
  },
  {
    a: account("acc_0000used", {
      label: "used@example.com",
      health: { state: "exhausted", until: iso(now + 26 * HOUR) },
    }),
    w: read(100, 64, "Max 20x"),
  },
  {
    a: account("acc_0000mdl0", {
      label: "models@example.com",
      cooldowns: [{ scope: "model", model: "opus-5", kind: "rate_limit", until: iso(now + 12 * MIN) }],
    }),
    w: read(93, 30, "Team"),
    requests: 1204,
  },
  { a: account("acc_0000serv", { label: "main@example.com" }), w: read(42, 7, "Max 20x"), requests: 3310 },
  {
    a: account("acc_0000free", { label: "free@example.com", extension: "pluxy/gemini" }),
    w: read(5, 1, "Free"),
  },
  { a: account("acc_0000long", { label: LONG }), w: read(10, 2, "Plus"), requests: 12 },
  { a: account("acc_0000read", { label: "reading@example.com" }), w: { state: "reading", limits: [] } },
  {
    a: account("acc_0000none", { label: "nolimits@example.com", extension: "pluxy/kimi" }),
    w: { state: "none", limits: [] },
  },
  {
    a: account("acc_0000fail", { label: "unread@example.com" }),
    w: { state: "failed", limits: [], error: "the provider did not answer" },
  },
  { a: account("acc_0000off0", { label: "spare@example.com", disabled: true }) },
];

const failing = async () => "pluxy could not reach core; check that it is running.";
const fine = () => undefined;

const EVENTS: UsageEvent[] = [
  {
    seq: 3,
    id: "c0ffee01",
    timestamp: iso(now - 2 * MIN),
    endpoint: "/v1/messages",
    model: "claude-opus-5",
    served_model: "claude-opus-5-20261101",
    provider: "pluxy/claude",
    account: "acc_0000serv",
    key_id: "k1",
    label: "Claude Code",
    failed: false,
    status: 200,
    attempts: 3,
    tried: [
      { account: "acc_0000cool", provider: "pluxy/claude", result: "rate_limit" },
      { account: "acc_0000used", provider: "pluxy/claude", result: "quota" },
      { account: "acc_0000serv", provider: "pluxy/claude", result: "ok" },
    ],
    latency_ms: 2200,
    tokens: { input_tokens: 18_200, output_tokens: 1_150, cached_tokens: 16_000, total_tokens: 19_350 },
  },
  {
    seq: 2,
    id: "c0ffee02",
    timestamp: iso(now - 5 * MIN),
    model: "gpt-5.5-codex-with-a-rather-long-model-name",
    provider: "pluxy/codex",
    account: "acc_0000ban0",
    key_id: "k2",
    failed: true,
    status: 529,
    error_code: "upstream",
    latency_ms: 420,
    tokens: {},
  },
  {
    seq: 1,
    id: "c0ffee03",
    timestamp: iso(now - 9 * MIN),
    model: "gemini-3-pro",
    failed: true,
    status: 401,
    error_code: "auth",
    latency_ms: 12,
    tokens: {},
  },
];
const KEYS = [
  { id: "k1", masked: "sk-****48", label: "Claude Code" },
  { id: "k2", masked: "sk-****91" },
];
const names: Record<string, string> = Object.fromEntries(ACCOUNTS.map((x) => [x.a.id, x.a.label ?? x.a.id]));
const name = (id: string) => names[id] ?? id;

const tool = (fields: Partial<SetupClient>): SetupClient => ({
  id: "claude-code",
  name: "Claude Code",
  installed: true,
  version: "2.1.30",
  config: "~/.claude/settings.json",
  state: "configured",
  ...fields,
});

/** A row whose save never answers, so it keeps saying Saving…. */
function SavingSetting() {
  const s = useSetting("info", () => new Promise<undefined>(() => {}));
  return (
    <SettingRow
      name="Log level"
      description="How much pluxy writes to its log."
      configKey="logging.level"
      saving={s.saving}
    >
      <select aria-label="Log level" value={s.value} onChange={(e) => s.set(e.currentTarget.value)}>
        <option value="info">Info</option>
        <option value="debug">Debug</option>
      </select>
    </SettingRow>
  );
}

/** A section's heading, with room above it. */
const Head = (props: { children: string }) => <h2 style={{ margin: "24px 0 8px" }}>{props.children}</h2>;

function Specimen() {
  return (
    <div class="specimen">
      <section aria-label="Accounts">
        <Head>Accounts</Head>
        {ACCOUNTS.map((x) => (
          <AccountRow
            key={x.a.id}
            account={x.a}
            windows={x.w}
            running={new Set(["pluxy/claude", "pluxy/codex", "pluxy/gemini", "pluxy/kimi"])}
            onTurnOn={failing}
            onClearCooldown={fine}
          />
        ))}
      </section>
      <section aria-label="Accounts, wide">
        <Head>Accounts, wide</Head>
        {ACCOUNTS.slice(0, 9).map((x) => (
          <AccountRow
            key={x.a.id}
            account={x.a}
            windows={x.w}
            wide
            requests={x.requests}
            onTurnOn={failing}
            onClearCooldown={fine}
          />
        ))}
      </section>
      <section aria-label="Requests">
        <Head>Requests</Head>
        {EVENTS.map((e, i) => (
          <RequestRow
            key={e.seq}
            event={e}
            name={name}
            keys={KEYS}
            note={i === 0 ? "after 2 others" : undefined}
            story={i === 0 ? <p>Two accounts could not take it, so the third one did.</p> : undefined}
            open={i === 0 ? true : undefined}
          />
        ))}
        <Head>Latest requests (compact)</Head>
        {EVENTS.map((e) => (
          <RequestRow key={e.seq} event={e} name={name} keys={KEYS} compact />
        ))}
      </section>
      <section aria-label="Settings">
        <Head>Settings</Head>
        <SettingRow
          name="Log every request"
          description="Keeps the whole of each request in the log, for support."
          configKey="logging.requests"
        >
          <Switch label="On" checked={true} onChange={() => {}} />
        </SettingRow>
        <SavingSetting />
        <SettingRow
          name="Port"
          description="Where your coding tools reach pluxy on this computer."
          configKey="port"
          disabled="pluxy runs as a service, so its port is set where the service is."
        >
          <input aria-label="Port" value="8317" />
        </SettingRow>
        <SettingRow
          name="Allow other computers"
          description="Lets other computers on your network use pluxy."
          error="Not saved: pluxy returned HTTP 500; check Logs and try again"
        >
          <Switch label="Off" checked={false} onChange={() => {}} />
        </SettingRow>
        <SettingRow
          name="Management key"
          description="The key that manages pluxy. Replacing it signs every other window out."
        >
          <ConfirmButton question="Replace the key? Every other window signs out." onConfirm={() => {}}>
            Replace key
          </ConfirmButton>
        </SettingRow>
      </section>
      <section aria-label="Tools">
        <Head>Tools</Head>
        <ToolRow client={tool({})} action={<Button>Set up</Button>} />
        <ToolRow
          client={tool({
            id: "codex",
            name: "Codex",
            version: "0.98.0",
            config: "~/.codex/config.toml",
            state: "not-configured",
          })}
          action={<Button variant="primary">Set up</Button>}
        >
          <p>What to paste into ~/.codex/config.toml shows here.</p>
        </ToolRow>
        <ToolRow
          client={tool({
            id: "opencode",
            name: "OpenCode",
            version: "1.2.0",
            config: "~/.config/opencode/opencode.json",
            state: "needs-update",
          })}
          action={<Button>Set up</Button>}
        />
        <ToolRow
          client={tool({
            id: "cline",
            name: "Cline",
            version: undefined,
            config:
              "~/Library/Application Support/Code/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json",
            state: "refused",
          })}
          reason="The file holds settings pluxy does not understand, so it leaves it alone."
        />
        <ToolRow
          client={tool({
            id: "gemini",
            name: "Gemini CLI",
            installed: false,
            version: undefined,
            config: "~/.gemini/settings.json",
            state: "not-configured",
          })}
        />
        <ToolRow
          client={tool({
            id: "claude-desktop",
            name: "Claude Desktop",
            version: undefined,
            config: "~/.config/Claude/claude_desktop_config.json",
            state: "not-configured",
          })}
          unchecked
        />
      </section>
      <section aria-label="Panes">
        <Head>Panes</Head>
        <StatePane kind="loading" title="Loading accounts…" />
        <StatePane
          kind="empty"
          title="No accounts yet"
          mark="plus"
          primary={{ label: "Add an account", href: "#/accounts/add" }}
        >
          <p>Add the AI accounts you already pay for, and your coding tools use them through pluxy.</p>
        </StatePane>
        <StatePane
          kind="failed"
          title="Could not read the accounts"
          details="GET /accounts: dial tcp 127.0.0.1:41000: connect: connection refused"
          primary={{ label: "Try again", onClick: () => {} }}
          secondary={{ label: "Open Logs", href: "#/logs" }}
        >
          <p>pluxy did not answer. Check that it is running, then try again.</p>
        </StatePane>
        <StaleNotice at={now - 4 * MIN} error="timeout" onRetry={() => {}} />
      </section>
    </div>
  );
}

const root = document.getElementById("specimen");
if (root) render(<Specimen />, root);
