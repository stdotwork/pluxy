// The app's document in an extension page's webview. That webview has no app commands: the
// native side serves this document to it (pluxy-page://), and its /send address is the one way
// out, tagged by the native side with the webview it came from. This document shows the
// extension's page in a sandboxed frame from the pages site, does the panel's side of the
// page's hello, and passes the page's channel messages to and from the app's window, which
// answers them (src/nativePage.tsx). It holds no key and no address but the page's own.

const frame = document.createElement("iframe");
let hello = false;
let loads = 0;
let port;
let sending = 0;
let generation;

// Native admission is authoritative, including direct requests from the frame. Bound this
// wrapper's own fetches too so it cannot retain an unbounded promise queue.
async function send(type, data) {
  if (sending >= 8) {
    if (type === "call" && typeof data?.id === "string")
      port?.postMessage({ v: 0, id: data.id, error: { code: "rate_limited", message: "too many calls" } });
    return;
  }
  sending++;
  try {
    const r = await fetch("/send", {
      method: "POST",
      body: JSON.stringify({ generation, type, data: data ?? null }),
    });
    if (!r.ok && type === "call") {
      const refusal = await r.json();
      if (typeof refusal.id === "string") port?.postMessage(refusal);
    }
  } catch {
    if (type === "call" && typeof data?.id === "string")
      port?.postMessage({
        v: 0,
        id: data.id,
        error: { code: "unavailable", message: "page transport failed" },
      });
  } finally {
    sending--;
  }
}

function closePort() {
  port?.close();
  port = undefined;
}

// The app's answers, delivered by the native side.
Object.defineProperty(window, "__pluxyDeliver", {
  value: (m, documentGeneration) => {
    if (documentGeneration !== generation) return false;
    const win = frame.contentWindow;
    if (!win || typeof m !== "object" || m === null) return false;
    if (m.type === "welcome" && !port) {
      const ch = new MessageChannel();
      port = ch.port1;
      port.onmessage = (e) => send("call", e.data);
      // The page's origin is opaque, so the target is "*"; the welcome holds no key.
      win.postMessage(m.data, "*", [ch.port2]);
    } else if (m.type === "refused") {
      win.postMessage(m.data, "*");
    } else if (m.type === "message") {
      port?.postMessage(m.data);
    }
    return true;
  },
});

window.addEventListener("message", (e) => {
  // Only a hello, only from the frame, and only once: a second document gets nothing.
  if (e.source !== frame.contentWindow || e.origin !== "null") return;
  if (hello || loads > 1) return;
  const d = e.data;
  if (typeof d !== "object" || d === null || d.pluxy !== "page" || d.type !== "hello") return;
  hello = true;
  send("hello", d);
});

frame.addEventListener("load", () => {
  loads++;
  if (loads > 1) {
    closePort();
    send("closed");
  }
});

const r = await fetch("/frame");
const { url, title, generation: documentGeneration } = await r.json();
generation = documentGeneration;
frame.title = title;
frame.sandbox = "allow-scripts";
frame.referrerPolicy = "no-referrer";
frame.allow = "";
frame.src = url;
document.body.append(frame);
