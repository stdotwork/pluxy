import { defineConfig, devices } from "@playwright/test";

// The browser tests start their own core (e2e/*.spec.ts), so no web server is configured here.
// Extension pages are a security boundary, so their test runs in all three engines; the rest of
// the panel runs in Chromium.
const viewport = { width: 1280, height: 800 };
export default defineConfig({
  testDir: "e2e",
  timeout: 120_000,
  workers: 1,
  reporter: process.env.CI ? "list" : "line",
  use: { headless: true, trace: "retain-on-failure" },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"], viewport } },
    { name: "firefox", testMatch: "extension-pages.spec.ts", use: { ...devices["Desktop Firefox"], viewport } },
    { name: "webkit", testMatch: "extension-pages.spec.ts", use: { ...devices["Desktop Safari"], viewport } },
  ],
});
