// The page the desktop app's native test shows (test/probe): from inside its sandboxed frame, it
// tries to reach the app's native commands every way a page could, then writes what happened
// into its own report setting through the channel, the one way out it has. Each attempt's
// status: "blocked" (it failed), "succeeded" (it worked: the test fails) or "attempted" (only
// the app can tell: the test checks the app reached no command from this page's webview).

import { connect } from "./pluxy-page.js";

const results = {};
const short = (v) => String(v).slice(0, 120);

async function attempt(name, fn) {
  try {
    const v = await Promise.race([
      Promise.resolve().then(fn),
      new Promise((_, reject) => setTimeout(() => reject(new Error("timed out")), 3000)),
    ]);
    results[name] = { status: "succeeded", detail: short(JSON.stringify(v) ?? v) };
  } catch (e) {
    results[name] = { status: "blocked", detail: short(e) };
  }
}

const pluxy = await connect();

await attempt("own tauri internals", () => {
  if (!window.__TAURI_INTERNALS__) throw new Error("absent");
  return "present";
});
await attempt("own tauri global", () => {
  if (!window.__TAURI__) throw new Error("absent");
  return "present";
});
for (const cmd of ["connection_get", "core_call", "page_send", "plugin:event|listen"]) {
  await attempt(`invoke ${cmd}`, () => window.__TAURI_INTERNALS__.invoke(cmd, {}));
  await attempt(`parent invoke ${cmd}`, () => window.parent.__TAURI_INTERNALS__.invoke(cmd, {}));
  await attempt(`top invoke ${cmd}`, () => window.top.__TAURI_INTERNALS__.invoke(cmd, {}));
}
// WebKit offers the webview's message handler to every frame; what it does with the message is
// the app's to tell.
try {
  const h = window.ipc ?? window.webkit?.messageHandlers?.ipc;
  if (!h) throw new Error("absent");
  for (const cmd of ["connection_get", "core_call", "sign_out"]) {
    h.postMessage(JSON.stringify({ cmd, callback: 1, error: 2, payload: {}, options: {} }));
  }
  results["native message handler"] = { status: "attempted", detail: "posted" };
} catch (e) {
  results["native message handler"] = { status: "blocked", detail: short(e) };
}
await attempt("fetch ipc scheme", () =>
  fetch("ipc://localhost/connection_get", { method: "POST" }).then((r) => r.status),
);
await attempt("fetch page scheme", () => fetch("pluxy-page://localhost/frame").then((r) => r.text()));
await attempt("read the app document", () => window.parent.document.body.innerHTML);
await attempt("navigate the top", () => {
  window.top.location.href = "about:blank";
  return "navigated";
});

const report = JSON.stringify(results);
document.getElementById("report").textContent = JSON.stringify(results, null, 2);
await pluxy.call("config.set", { values: { report } });
document.getElementById("state").textContent = "reported";
