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
let queue = Promise.resolve();

// In order, one at a time: the native side passes each on as it arrives.
function send(type, data) {
  queue = queue.then(() =>
    fetch("/send", { method: "POST", body: JSON.stringify({ type, data }) }).catch(() => {}),
  );
}

function closePort() {
  port?.close();
  port = undefined;
}

// The app's answers, delivered by the native side.
Object.defineProperty(window, "__pluxyDeliver", {
  value: (m) => {
    const win = frame.contentWindow;
    if (!win || typeof m !== "object" || m === null) return;
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
const { url, title } = await r.json();
frame.title = title;
frame.sandbox = "allow-scripts";
frame.referrerPolicy = "no-referrer";
frame.allow = "";
frame.src = url;
document.body.append(frame);
