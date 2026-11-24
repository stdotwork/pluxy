import { defineConfig, devices } from "@playwright/test";

// The browser tests start their own core (e2e/*.spec.ts), so no web server is configured here.
// Every test runs in Chromium. Extension pages are a security boundary, so their test also runs in
// Firefox and WebKit, in CI and when PLUXY_ALL_BROWSERS=1 is set (next/ui/README.md).
const viewport = { width: 1280, height: 800 };
const allBrowsers = Boolean(process.env.CI) || process.env.PLUXY_ALL_BROWSERS === "1";
export default defineConfig({
  testDir: "e2e",
  timeout: 120_000,
  workers: 1,
  reporter: process.env.CI ? "list" : "line",
  use: { headless: true, trace: "retain-on-failure" },
  projects: [
    // The panel's project keeps a screenshot of every test: CI uploads them as proof of the UI.
    { name: "chromium", use: { ...devices["Desktop Chrome"], viewport, screenshot: "on" } },
    ...(allBrowsers
      ? [
          {
            name: "firefox",
            testMatch: "extension-pages.spec.ts",
            use: { ...devices["Desktop Firefox"], viewport },
          },
          {
            name: "webkit",
            testMatch: "extension-pages.spec.ts",
            use: { ...devices["Desktop Safari"], viewport },
          },
        ]
      : []),
  ],
});
