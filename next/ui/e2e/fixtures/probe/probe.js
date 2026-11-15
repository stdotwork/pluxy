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
// The app's schemes at each address they have: their own on Linux and macOS, http://<scheme>.localhost
// on Windows. This page is on the pages site, so it cannot tell from its own address which one
// the app uses (ingress-test.js, served by the page scheme, can): it tries both.
const schemes = (name) => [`${name}://localhost`, `http://${name}.localhost`];
for (const base of schemes("ipc"))
  await attempt(`fetch ipc scheme ${base}`, () =>
    fetch(`${base}/connection_get`, { method: "POST" }).then((r) => r.status),
  );
for (const base of schemes("pluxy-page"))
  await attempt(`fetch page scheme ${base}`, () => fetch(`${base}/frame`).then((r) => r.text()));
await attempt("read the app document", () => window.parent.document.body.innerHTML);
await attempt("navigate the top", () => {
  window.top.location.href = "about:blank";
  return "navigated";
});

for (const base of schemes("pluxy-page"))
  await attempt(`direct pages-site scheme ${base}`, () =>
    fetch(`${base}/send?pages_site_probe`, {
      method: "POST",
      mode: "no-cors",
      body: JSON.stringify({
        type: "call",
        data: { v: 0, id: "real-site", method: "theme.get", params: null },
      }),
    }),
  );

// The native test's requests come as soon as the core has the report, which can be before the
// report's call returns here: listen first.
// The native test asks the actual frame document to navigate within itself. Setting the
// iframe src from its parent can reload a sandboxed frame rather than change only its hash.
window.addEventListener("message", (e) => {
  if (e.source === parent && e.data?.nativeHash === true) {
    location.hash = "native-fragment";
    parent.postMessage({ nativeHashChanged: true }, "*");
  }
});

// The native test also asks the frame to call every app command through a Tauri bridge of its
// own, if it has one (WebView2 runs Tauri's scripts in frames too): none may succeed.
window.addEventListener("message", async (e) => {
  if (e.source !== parent || !Array.isArray(e.data?.nativeInvoke)) return;
  const inv = window.__TAURI_INTERNALS__?.invoke;
  const calls = {};
  await Promise.all(
    e.data.nativeInvoke.map(async (cmd) => {
      try {
        if (typeof inv !== "function") throw new Error("absent");
        const v = await Promise.race([
          inv(cmd, {}),
          new Promise((_, reject) => setTimeout(() => reject(new Error("timed out")), 5000)),
        ]);
        calls[cmd] = { ok: true, detail: short(JSON.stringify(v) ?? v) };
      } catch (err) {
        calls[cmd] = { ok: false, detail: short(err) };
      }
    }),
  );
  parent.postMessage({ nativeInvoked: { internals: typeof inv === "function", calls } }, "*");
});

// And to leave its site, one address at a time: the native test then asks where this frame is,
// and only a document still here can answer.
window.addEventListener("message", (e) => {
  if (e.source !== parent) return;
  if (typeof e.data?.nativeOffsite === "string") {
    const url = e.data.nativeOffsite;
    const tried = { url };
    try {
      location.href = url;
    } catch (err) {
      tried.error = short(err);
    }
    parent.postMessage({ nativeOffsiteTried: tried }, "*");
  }
  if (e.data?.nativeHere === true) parent.postMessage({ nativeHere: location.href }, "*");
});

const report = JSON.stringify(results);
document.getElementById("report").textContent = JSON.stringify(results, null, 2);
await pluxy.call("config.set", { values: { report } });
document.getElementById("state").textContent = "reported";
