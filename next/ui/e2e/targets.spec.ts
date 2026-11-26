// Every control a pointer can hit is at least 24 by 24 CSS pixels to hit (WCAG 2.2, 2.5.8): the
// drawn control may be smaller when its hit area reaches past it. The hit area is what a click at
// a point lands on, found with elementFromPoint, not the size of the drawn box.
import { expect, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();
const MIN = 24;

test.beforeAll(async () => {
  for (const id of ["target-a", "target-b"]) {
    const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
      id,
      extension: "test/alpha",
      method: "api_key",
      input: { api_key: "ak-targets-test" },
    });
    expect(r.status).toBe(202);
    await expect
      .poll(
        async () =>
          (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login
            .state,
      )
      .toBe("done");
  }
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": true } } })).status,
  ).toBe(200);
  for (const model of ["fake-model", "server-error"]) {
    const r = await fetch(`${core.base}/v1/chat/completions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }),
    });
    await r.arrayBuffer();
  }
});

/** How many controls the page shows, and the ones smaller than MIN to hit, each described. */
async function measure(page: Page): Promise<{ controls: number; small: string[] }> {
  return page.evaluate((min) => {
    const found: string[] = [];
    let controls = 0;
    const selector = 'a[href], button, input:not([type="hidden"]), select, textarea, [role="tab"], summary';
    for (const el of document.querySelectorAll<HTMLElement>(selector)) {
      if (el.closest(".skip-link") || (el as HTMLButtonElement).disabled) continue;
      const style = getComputedStyle(el);
      if (style.visibility === "hidden" || style.display === "none") continue;
      // What a closed <details> holds is not shown, though it still has a box.
      if (el.tagName !== "SUMMARY" && el.closest("details:not([open])")) continue;
      let rect = el.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      // A link inside a sentence is exempt (WCAG 2.5.8, inline): its parent holds other text.
      if (
        el.tagName === "A" &&
        [...(el.parentElement?.childNodes ?? [])].some(
          (n) => n.nodeType === Node.TEXT_NODE && (n.textContent ?? "").trim() !== "",
        )
      )
        continue;
      controls++;
      el.scrollIntoView({ block: "center", inline: "center" });
      rect = el.getBoundingClientRect();
      const owns = (x: number, y: number) => {
        const hit = document.elementFromPoint(x, y);
        if (!hit) return false;
        if (el === hit || el.contains(hit)) return true;
        // A click on a control's label reaches the control.
        return [...((el as HTMLInputElement).labels ?? [])].some((l) => l === hit || l.contains(hit));
      };
      const cx = rect.left + rect.width / 2;
      const cy = rect.top + rect.height / 2;
      const reach = (dx: number, dy: number) => {
        let n = 0;
        while (n < 60 && owns(cx + dx * (n + 1), cy + dy * (n + 1))) n++;
        return n;
      };
      const width = reach(-1, 0) + reach(1, 0) + 1;
      const height = reach(0, -1) + reach(0, 1) + 1;
      if (width < min || height < min) {
        const name = el.getAttribute("aria-label") ?? el.textContent?.trim().slice(0, 30) ?? "";
        const cls = typeof el.className === "string" ? el.className.split(" ")[0] : "";
        found.push(
          `${el.tagName.toLowerCase()}${el.getAttribute("type") ? `[${el.getAttribute("type")}]` : ""}.${cls} "${name}" ${width}x${height}`,
        );
      }
    }
    return { controls, small: [...new Set(found)] };
  }, MIN);
}

/** Fails when a control on the page is too small to hit, or when the page shows no controls. */
async function expectLargeEnough(page: Page) {
  const { controls, small } = await measure(page);
  expect(small).toEqual([]);
  expect(controls).toBeGreaterThan(0);
}

const PAGES = [
  "Home",
  "Accounts",
  "Add Account",
  "Routing",
  "Quota Management",
  "Logs",
  "Requests",
  "Usage",
  "Set up pluxy",
  "Config Panel",
  "System",
  "Extensions",
  "Themes",
];

for (const name of PAGES) {
  test(`the controls on ${name} are at least ${MIN}x${MIN} to hit`, async ({ page }) => {
    await openPanel(page, core);
    await signIn(page, core);
    // By its exact name: "Usage" is also part of an extension page's name.
    await goTo(page, name);
    await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
    await page.waitForLoadState("networkidle");
    if (name === "Accounts") {
      await expect(page.getByRole("button", { name: "target-a", exact: true })).toBeVisible();
      await page.getByRole("button", { name: "target-a", exact: true }).click();
      await expect(page.getByRole("complementary", { name: "Details: target-a" })).toBeVisible();
    }
    await expectLargeEnough(page);
    // Each tab of a page with tabs.
    const tabs = page.getByRole("main").getByRole("tab");
    for (let i = 1; i < (await tabs.count()); i++) {
      await tabs.nth(i).click();
      await expect(tabs.nth(i)).toHaveAttribute("aria-selected", "true");
      await page.waitForLoadState("networkidle");
      await expectLargeEnough(page);
    }
  });
}
