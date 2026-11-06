import { defineConfig, devices } from "@playwright/test";

// The browser test starts its own core (e2e/panel.spec.ts), so no web server is configured here.
export default defineConfig({
  testDir: "e2e",
  timeout: 120_000,
  workers: 1,
  reporter: process.env.CI ? "list" : "line",
  use: { headless: true, trace: "retain-on-failure" },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } },
  ],
});
