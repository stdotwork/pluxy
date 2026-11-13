// The app's document in an extension page's webview. That webview has no app commands: the
// native side serves this document to it (pluxy-page://), and its /send address is the one way
// out, tagged by the native side with the webview it came from. This document shows the
// extension's page in a sandboxed frame from the pages site (for a package installed on this
// device, from the app's pluxy-app: scheme, which serves only that page's release), does the
// panel's side of the page's hello, and passes the page's channel messages to and from the
// app's window, which answers them (src/nativePage.tsx). It holds no key and no address but
// the page's own.

const frame = document.createElement("iframe");
let hello = false;
let loads = 0;
let port;
const queue = [];
let pumping = false;
let generation;
let nativeFrameNavigation = false;

// One FIFO preserves mutation/read order. Only calls consume its bounded capacity;
// the single hello and closed lifecycle messages always reach native admission.
function send(type, data) {
  if (type === "call" && queue.filter((m) => m.type === "call").length >= 8) {
    if (typeof data?.id === "string")
      port?.postMessage({ v: 0, id: data.id, error: { code: "rate_limited", message: "too many calls" } });
    return;
  }
  queue.push({ generation, type, data: data ?? null });
  void pump();
}
async function pump() {
  if (pumping) return;
  pumping = true;
  while (queue.length) {
    const message = queue[0];
    try {
      const r = await fetch("/send", { method: "POST", body: JSON.stringify(message) });
      if (!r.ok) {
        const refusal = await r.json();
        if (message.type === "call") {
          const id = typeof refusal.id === "string" ? refusal.id : message.data?.id;
          if (typeof id === "string")
            port?.postMessage({
              ...refusal,
              v: 0,
              id,
              error: {
                code: r.status === 413 ? "too_large" : (refusal.error?.code ?? "bad_request"),
                message: refusal.error?.message ?? "native page transport refused the call",
              },
            });
        }
        if (message.type === "hello")
          frame.contentWindow?.postMessage(
            refusal.type === "refused"
              ? refusal
              : { pluxy: "host", type: "refused", reason: "native page transport refused the hello" },
            "*",
          );
      }
    } catch {
      if (message.type === "call" && typeof message.data?.id === "string")
        port?.postMessage({
          v: 0,
          id: message.data.id,
          error: { code: "unavailable", message: "page transport failed" },
        });
      if (message.type === "hello")
        frame.contentWindow?.postMessage(
          { pluxy: "host", type: "refused", reason: "page transport failed" },
          "*",
        );
    } finally {
      queue.shift();
    }
  }
  pumping = false;
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
      if (!port) return false;
      port.postMessage(m.data);
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
  if (loads === 2 && !nativeFrameNavigation) {
    closePort();
    send("closed");
  }
});

const r = await fetch("/frame");
const { url, title, generation: documentGeneration, native_frame_navigation } = await r.json();
nativeFrameNavigation = native_frame_navigation === true;
generation = documentGeneration;
frame.title = title;
frame.sandbox = "allow-scripts";
frame.referrerPolicy = "no-referrer";
frame.allow = "";
frame.src = url;
document.body.append(frame);
