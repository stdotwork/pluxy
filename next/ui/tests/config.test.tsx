import { expect, test } from "bun:test";
import render from "preact-render-to-string";
import type { CoreSettings } from "../kit/api/settings";
import { ConfigSettings, changeSetting, settingsChange } from "../kit/pages/configSettings";

const settings: CoreSettings = {
  routing: {
    strategy: "round-robin",
    "session-affinity": false,
    "session-affinity-ttl": "1h",
    "force-model-prefix": false,
    retry: { "request-retry": 2, "max-retry-credentials": 0, "max-retry-interval": 0.5 },
    cooldown: {
      "disable-cooling": false,
      "transient-error-cooldown-seconds": 0.5,
      "quota-response-mode": "retryable",
    },
  },
  observability: {
    logs: {
      "logging-to-file": true,
      "logs-max-total-size-mb": 0,
      "request-log": false,
      "error-logs-max-files": 10,
    },
    usage: { "usage-statistics-enabled": true },
  },
  token_count_estimate: true,
  "proxy-url": "socks5://redacted@127.0.0.1:1080",
};

test("settingsChange refuses a cleared retry count instead of sending ignored null", () => {
  const draft = changeSetting(settings, "routing.retry.request-retry", "");
  expect(() => settingsChange(settings, draft)).toThrow("Request Retry Count");
  expect(() =>
    settingsChange(settings, changeSetting(settings, "routing.retry.request-retry", null)),
  ).toThrow("Request Retry Count");
  const unset = changeSetting(settings, "routing.retry.request-retry", null);
  expect(settingsChange(unset, unset)).toEqual({});
});

test("an unrelated edit leaves fractional durations out of PATCH", () => {
  expect(settingsChange(settings, changeSetting(settings, "routing.force-model-prefix", true))).toEqual({
    routing: { "force-model-prefix": true },
  });
});

test("the proxy URL is sent only when edited, and cleared by an empty string", () => {
  // Another setting's edit never sends the proxy as shown, so its hidden password is not touched.
  expect(settingsChange(settings, changeSetting(settings, "routing.force-model-prefix", true))).toEqual({
    routing: { "force-model-prefix": true },
  });
  expect(settingsChange(settings, changeSetting(settings, "proxy-url", ""))).toEqual({ "proxy-url": "" });
  expect(settingsChange(settings, changeSetting(settings, "proxy-url", "http://proxy.example:3128"))).toEqual(
    { "proxy-url": "http://proxy.example:3128" },
  );
  const empty = changeSetting(settings, "proxy-url", "");
  expect(settingsChange(empty, empty)).toEqual({});
});

test("the Network tab's proxy URL field may be empty and has the old placeholder", () => {
  const html = render(
    <ConfigSettings
      tab="network"
      search=""
      value={changeSetting(settings, "proxy-url", "")}
      onChange={() => {}}
      dirty={false}
      busy={false}
      save={() => {}}
      discard={() => {}}
    />,
  );
  const input = html.match(/<input[^>]*id="proxy-url"[^>]*>/)?.[0];
  expect(input).toBeDefined();
  expect(input).toContain('placeholder="socks5://user:pass@127.0.0.1:1080/"');
  expect(input).not.toContain("required");
  expect(html).toContain("Proxy URL");
});

test("duration controls accept fractional readbacks; existing retry count is required", () => {
  const html = render(
    <ConfigSettings
      tab="network"
      search=""
      value={settings}
      onChange={() => {}}
      dirty
      busy={false}
      save={() => {}}
      discard={() => {}}
    />,
  );
  for (const id of [
    "routing.retry.max-retry-interval",
    "routing.cooldown.transient-error-cooldown-seconds",
  ]) {
    const input = html.match(new RegExp(`<input[^>]*id="${id}"[^>]*>`))?.[0];
    expect(input).toContain('step="any"');
  }
  const retry = html.match(/<input[^>]*id="routing.retry.request-retry"[^>]*>/)?.[0];
  expect(retry).toContain("required");
  expect(retry).not.toContain("placeholder");
  expect(html).not.toContain("Leave empty for core's default");
  expect(html).not.toMatch(/<fieldset[^>]*data-part="field"/);
});
