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
  expect(html).not.toContain("Leave empty for core's default");
  expect(html).not.toMatch(/<fieldset[^>]*data-part="field"/);
});
