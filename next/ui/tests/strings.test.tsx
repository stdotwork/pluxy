import { afterEach, describe, expect, test } from "bun:test";
import { renderToString } from "preact-render-to-string";
import { api } from "../kit/api";
import type { Host } from "../kit/host";
import { AccountsPage } from "../kit/pages/accounts";
import { format, rich, setLocale, strings } from "../kit/strings";
import { DEFAULT_LAYOUT } from "../kit/theme/contract";
import type { ThemeState } from "../kit/theme/state";
import { type Kit, KitContext } from "../kit/ui";

const t = strings("test", {
  refused: "Revision {revision} was refused: {why}",
  accounts: { one: "{count} account", other: "{count} accounts" },
});

afterEach(() => setLocale(undefined));

describe("placeholders", () => {
  test("each {name} is replaced by its value", () => {
    expect(format("Revision {revision} was refused: {why}", { revision: 7, why: "bad port" })).toBe(
      "Revision 7 was refused: bad port",
    );
    expect(t("refused", { revision: 3, why: "no reason given" })).toBe(
      "Revision 3 was refused: no reason given",
    );
  });

  test("a placeholder with no value stays as written", () => {
    expect(format("{a} and {b}", { a: "x" })).toBe("x and {b}");
    expect(format("no placeholders")).toBe("no placeholders");
  });
});

describe("markup in placeholders", () => {
  test("rich puts each part in its placeholder and keeps the rest", () => {
    const path = <span class="mono">README.md</span>;
    expect(rich("See {path} and {other}.", { path })).toEqual(["See ", path, " and ", "{other}", "."]);
    expect(renderToString(<p>{rich("See {path}.", { path })}</p>)).toBe(
      '<p>See <span class="mono">README.md</span>.</p>',
    );
  });
});

describe("plurals", () => {
  test("English: one for 1, other for every other count", () => {
    expect(t("accounts", { count: 1 })).toBe("1 account");
    expect(t("accounts", { count: 0 })).toBe("0 accounts");
    expect(t("accounts", { count: 2 })).toBe("2 accounts");
    expect(t("accounts", { count: 21 })).toBe("21 accounts");
  });

  test("a locale's plural forms follow its language's rules", () => {
    setLocale({
      lang: "ru",
      texts: { "test.accounts": { one: "{count} аккаунт", other: "{count} аккаунтов" } },
    });
    expect(t("accounts", { count: 1 })).toBe("1 аккаунт");
    expect(t("accounts", { count: 21 })).toBe("21 аккаунт");
    expect(t("accounts", { count: 5 })).toBe("5 аккаунтов");
  });
});

describe("locales", () => {
  test("a locale's text replaces the English, and a key it lacks falls back to English", () => {
    setLocale({ lang: "de", texts: { "test.refused": "Revision {revision} abgelehnt: {why}" } });
    expect(t("refused", { revision: 2, why: "x" })).toBe("Revision 2 abgelehnt: x");
    expect(t("accounts", { count: 2 })).toBe("2 accounts");
  });

  test("the Accounts page shows the locale's heading, and English where the locale has no text", () => {
    const host: Host = {
      call: () => new Promise(() => {}), // never answers: the page renders as it does while loading
      signedIn: () => true,
      signIn: async () => undefined,
      signOut: () => {},
      prefs: { get: () => null, set: () => {} },
      features: new Set(),
    };
    const themes = {
      resolved: { layout: DEFAULT_LAYOUT },
      subscribe: () => () => {},
    } as unknown as ThemeState;
    const kit: Kit = { host, api: api(host), themes, unauthorized: () => {}, extensionsChanged: () => {} };
    const render = () =>
      renderToString(
        <KitContext.Provider value={kit}>
          <AccountsPage />
        </KitContext.Provider>,
      );

    expect(render()).toContain('<h1 data-part="page-title">Accounts</h1>');

    setLocale({ lang: "de", texts: { "accounts.title": "Konten" } });
    const html = render();
    expect(html).toContain('<h1 data-part="page-title">Konten</h1>');
    expect(html).not.toContain('<h1 data-part="page-title">Accounts</h1>');
    expect(html).toContain(">Refresh</button>");
  });
});
