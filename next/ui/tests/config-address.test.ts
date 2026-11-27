import { expect, test } from "bun:test";
import {
  configAddress,
  findAccount,
  HIDDEN,
  hideSecrets,
  parseConfigAddress,
} from "../kit/pages/configSource.helpers";

test("the old tabs' addresses open the Settings tab where their settings now are", () => {
  expect(parseConfigAddress("#/config")).toEqual({ tab: "settings" });
  expect(parseConfigAddress("#/config/")).toEqual({ tab: "settings" });
  expect(parseConfigAddress("#/config/common")).toEqual({ tab: "settings", section: "network" });
  expect(parseConfigAddress("#/config/network")).toEqual({ tab: "settings", section: "network" });
  expect(parseConfigAddress("#/config/logging")).toEqual({ tab: "settings", section: "logging" });
  expect(parseConfigAddress("#/config/connectivity")).toEqual({ tab: "settings", section: "keys" });
  expect(parseConfigAddress("#/config/advanced")).toEqual({ tab: "settings", section: "keys" });
  expect(parseConfigAddress("#/config/source")).toEqual({ tab: "source" });
  expect(parseConfigAddress("#/config/nope")).toEqual({ tab: "settings" });
  // Another page's address is none of Config's.
  expect(parseConfigAddress("#/configuration")).toBeUndefined();
  expect(parseConfigAddress("#/routing")).toBeUndefined();
});

// Unchanged by the Settings and Settings file tabs: the account part of the Settings file address.
test("parseConfigAddress still reads the account the settings file opens at", () => {
  expect(parseConfigAddress("#/config/source?account=alpha-1")).toEqual({
    tab: "source",
    account: "alpha-1",
  });
  expect(parseConfigAddress("#/config/source?account=a%20b%2Fc")).toEqual({
    tab: "source",
    account: "a b/c",
  });
  expect(parseConfigAddress("#/config/source?account=%E0%A4%A")).toEqual({
    tab: "source",
    account: "%E0%A4%A",
  });
  expect(parseConfigAddress("#/config/source?account=")).toEqual({ tab: "source" });
});

test("configAddress writes what parseConfigAddress reads; Settings keeps the plain address", () => {
  expect(configAddress("settings")).toBe("#/config");
  expect(configAddress("source")).toBe("#/config/source");
  expect(configAddress("source", "alpha-1")).toBe("#/config/source?account=alpha-1");
  expect(configAddress("source", "a b/c")).toBe("#/config/source?account=a%20b%2Fc");
  for (const [tab, account] of [["settings"], ["source"], ["source", "a b/c"]] as const) {
    expect(parseConfigAddress(configAddress(tab, account))).toEqual(account ? { tab, account } : { tab });
  }
});

const at = (content: string, id: string) => {
  const found = findAccount(content, id);
  return found && content.slice(found.start, found.end);
};

test("findAccount finds the id of an account in the JSON core writes on one line", () => {
  const content =
    '{"listen":"127.0.0.1:0","extension":{"accounts":[{"id":"local-1","credential":{"kind":"api_key","api_key":"pluxy-redacted-1"}}]},' +
    '"extensions":{"test/alpha":{"accounts":[{"id":"alpha-1","models":["m"]},{"id":"alpha-2","models":["m"]}]}}}';
  expect(findAccount(content, "alpha-1")).toEqual({
    start: content.indexOf('"alpha-1"') + 1,
    end: content.indexOf('"alpha-1"') + 1 + "alpha-1".length,
  });
  expect(at(content, "alpha-2")).toBe("alpha-2");
  expect(findAccount(content, "alpha-2")?.start).toBe(content.indexOf('"alpha-2"') + 1);
  expect(at(content, "local-1")).toBe("local-1");
});

test("findAccount finds the id in indented JSON and in YAML, quoted or not", () => {
  const json = JSON.stringify(
    { extensions: { "a/b": { accounts: [{ id: "one" }, { id: "two" }] } } },
    null,
    2,
  );
  expect(findAccount(json, "two")?.start).toBe(json.indexOf('"two"') + 1);

  const yaml = [
    "extensions:",
    "  pluxy/codex:",
    "    accounts:",
    "      - id: plain # a comment",
    "        credential:",
    "          kind: api_key",
    '      - id: "double"',
    "      - id: 'single'",
    "      - {id: flow, models: [m]}",
    "      -",
    "        priority: 1",
    "        id: later",
    "",
  ].join("\n");
  for (const [id, before] of [
    ["plain", "- id: "],
    ["double", '- id: "'],
    ["single", "- id: '"],
    ["flow", "{id: "],
    ["later", "        id: "],
  ] as const) {
    const start = yaml.indexOf(before + id) + before.length;
    expect(findAccount(yaml, id)).toEqual({ start, end: start + id.length });
  }
});

test("findAccount skips a commented-out entry", () => {
  expect(findAccount("accounts:\n  # - id: old\n", "old")).toBeUndefined();
  const both = "accounts:\n  # - id: old\n  - id: old # was: id: old\n";
  const start = both.indexOf("- id: old #") + "- id: ".length;
  expect(findAccount(both, "old")).toEqual({ start, end: start + 3 });
  // A # inside a quoted string is not a comment.
  const quoted = '{"note":"a #b","id":"x"}';
  expect(at(quoted, "x")).toBe("x");
});

test("findAccount takes the whole id, the id key and exactly one place", () => {
  const content = '{"accounts":[{"id":"alpha-10"},{"id":"alpha-1x"},{"name":"alpha-1"},{"id":"axb"}]}';
  expect(findAccount(content, "alpha-1")).toBeUndefined(); // a prefix of one id, the value of another key
  expect(at(content, "alpha-10")).toBe("alpha-10");
  expect(findAccount(content, "a.b")).toBeUndefined(); // a dot is a dot, not any character
  expect(findAccount(content, "missing")).toBeUndefined();
  expect(findAccount('{"a":[{"id":"x"}],"b":[{"id":"x"}]}', "x")).toBeUndefined(); // two places: unsure
});

test("findAccount finds nothing for an account of a provider section, whose id the file does not hold", () => {
  const yaml = [
    "claude-api-key:",
    "  - api-key: pluxy-redacted-1",
    "    prefix: team",
    "openai-compatibility:",
    "  - name: openrouter",
    "    base-url: https://openrouter.ai/api/v1",
    "",
  ].join("\n");
  expect(findAccount(yaml, "claude-api-key-1")).toBeUndefined();
  expect(findAccount(yaml, "openrouter")).toBeUndefined();
});

test("hideSecrets hides keys and passwords typed into a config.yaml, and leaves placeholders and other settings", () => {
  const placeholder = "pluxy-redacted-0123456789abcdef01234567";
  const yaml = [
    "management-key: brand-new-1234567890",
    "client-keys:",
    "  - sk-abc123456789",
    `  - ${placeholder}`,
    "requests:",
    "  proxy-url: http://u:p4ss@proxy.example:3128",
    "claude-api-key:",
    '  - api-key: "sk-ant-xyz"',
    "    base-url: https://api.example.com",
    "    prefix: team",
    "observability:",
    "  logs:",
    "    request-log: true",
    "",
  ].join("\n");
  expect(hideSecrets(yaml, "yaml")).toEqual([
    `management-key: ${HIDDEN}\n`,
    "client-keys:\n",
    `  - ${HIDDEN}\n`,
    `  - ${placeholder}\n`,
    "requests:\n",
    `  proxy-url: http://${HIDDEN}@proxy.example:3128\n`,
    "claude-api-key:\n",
    `  - api-key: "${HIDDEN}"\n`,
    "    base-url: https://api.example.com\n",
    "    prefix: team\n",
    "observability:\n",
    "  logs:\n",
    "    request-log: true\n",
  ]);
});

test("hideSecrets hides each secret value in the JSON core writes on one line", () => {
  const json =
    '{"management_key":"brand-new-1234567890","client_keys":["sk-1aaaaaaaa",{"key":"sk-2bbbbbbbb"}],"logs":{"to_file":true}}';
  expect(hideSecrets(json, "json")).toEqual([
    `{"management_key":"${HIDDEN}","client_keys":["${HIDDEN}",{"key":"${HIDDEN}"}],"logs":{"to_file":true}}`,
  ]);
});
