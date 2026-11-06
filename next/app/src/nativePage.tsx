// An extension page in the desktop app. The page never runs in this webview: the native side
// opens a webview of its own for it, with no app commands granted, over this page's area. That
// webview holds a small app document (page/) that shows the extension's page in a sandboxed
// frame, as the panel does, and passes the page's channel messages to this webview through the
// native side. This webview answers them with the kit's channel, for the extension core lists
// the page under: the same checks, permissions and limits as in the panel.

import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Channel, isHello, pickVersion, serve } from "../../ui/kit/extensions/channel";
import { type ExtensionPageProps, settingsOutcome, themeSnapshot } from "../../ui/kit/extensions/page";
import { pageProblem } from "../../ui/kit/extensions/site";
import { Button, Notice, PageHead, useKit } from "../../ui/kit/ui";
import { type PageMessage, PageRouter } from "./pageEvents";

const router = new PageRouter();
let listening: Promise<unknown> | undefined;

/** One listener for every page webview, in place before any page webview opens. */
function listenOnce(): Promise<unknown> {
  listening ??= listen<PageMessage>("pluxy-page", (e) => router.deliver(e.payload)).catch((e) => {
    listening = undefined;
    throw e;
  });
  return listening;
}

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

function rectOf(el: Element): Rect {
  const r = el.getBoundingClientRect();
  return { x: r.left, y: r.top, width: Math.max(r.width, 1), height: Math.max(r.height, 1) };
}

type Phase = "loading" | "connected" | "closed" | "failed";

export function NativeExtensionPage(props: ExtensionPageProps) {
  const { api, themes, unauthorized } = useKit();
  const { ext, page } = props;
  const area = useRef<HTMLElement>(null);
  const [mount, setMount] = useState(0); // a new webview, and a new channel, each time it changes
  const [phase, setPhase] = useState<Phase>("loading");
  const [error, setError] = useState<string>();
  const problem = props.pagesError ?? pageProblem(page.url, props.pagesOrigin);

  const permissions = ext.permissions.join(" ");
  useLayoutEffect(() => {
    const el = area.current;
    if (problem || !el) return;
    let live = true;
    let label: string | undefined;
    let channel: Channel | undefined;
    let ports: MessageChannel | undefined;
    let hello = false;
    let stopListening = () => {};
    setPhase("loading");
    setError(undefined);

    const send = (msg: unknown) => {
      if (label) invoke("page_send", { label, msg }).catch(() => {});
    };
    const close = () => {
      channel?.close();
      ports?.port2.close();
      channel = undefined;
      ports = undefined;
    };
    const onMessage = (m: PageMessage) => {
      if (m.type === "closed") {
        close();
        setPhase("closed");
        return;
      }
      if (m.type === "call") {
        ports?.port2.postMessage(m.data);
        return;
      }
      // One hello per webview: a second document there gets nothing.
      if (m.type !== "hello" || hello || !isHello(m.data)) return;
      hello = true;
      if (pickVersion(m.data) === undefined) {
        send({
          type: "refused",
          data: { pluxy: "host", type: "refused", reason: "no channel version in common" },
        });
        return;
      }
      ports = new MessageChannel();
      channel = serve(
        ports.port1,
        { extension: ext.id, permissions: permissions ? permissions.split(" ") : [] },
        {
          theme: () => themeSnapshot(themes.resolved),
          configGet: () => settingsOutcome(api.settings(ext.id), unauthorized),
          configSet: (values) => settingsOutcome(api.changeSettings(ext.id, values), unauthorized),
        },
      );
      ports.port2.onmessage = (e) => send({ type: "message", data: e.data });
      // Holds no key and no address: the same welcome the panel sends.
      send({
        type: "welcome",
        data: {
          pluxy: "host",
          v: 0,
          type: "welcome",
          extension: ext.id,
          page: page.id,
          theme: themeSnapshot(themes.resolved),
        },
      });
      setPhase("connected");
    };

    const place = () => {
      if (label && area.current) invoke("page_place", { label, rect: rectOf(area.current) }).catch(() => {});
    };
    const resize = new ResizeObserver(place);
    listenOnce()
      .then(() =>
        invoke<string>("page_open", {
          extension: ext.id,
          page: page.id,
          title: page.title,
          url: page.url,
          pagesOrigin: props.pagesOrigin,
          rect: rectOf(el),
        }),
      )
      .then(
        (l) => {
          if (!live) {
            router.drop(l);
            invoke("page_close", { label: l }).catch(() => {});
            return;
          }
          label = l;
          stopListening = router.claim(l, onMessage);
          resize.observe(el);
          window.addEventListener("resize", place);
          window.addEventListener("scroll", place, true);
        },
        (e) => {
          if (!live) return;
          setPhase("failed");
          setError(String(e));
        },
      );
    const unsubscribe = themes.subscribe(() => channel?.event("theme", themeSnapshot(themes.resolved)));
    return () => {
      live = false;
      resize.disconnect();
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      unsubscribe();
      stopListening();
      close();
      if (label) invoke("page_close", { label }).catch(() => {});
    };
  }, [
    mount,
    problem,
    ext.id,
    permissions,
    page.id,
    page.title,
    page.url,
    props.pagesOrigin,
    api,
    themes,
    unauthorized,
  ]);

  return (
    <>
      <PageHead title={page.title} />
      {problem ? (
        <Notice tone="warn">This extension page cannot be shown: {problem}.</Notice>
      ) : (
        <>
          {phase === "failed" && <Notice tone="danger">The page could not be opened: {error}</Notice>}
          {phase === "closed" && (
            <div class="actions">
              <Notice tone="warn">
                The page loaded another document, so the app closed its connection to it.
              </Notice>
              <Button variant="primary" onClick={() => setMount((n) => n + 1)}>
                Load the page again
              </Button>
            </div>
          )}
          <section
            key={`${mount} ${page.url}`}
            ref={area}
            class="extension-frame"
            data-part="extension-frame"
            data-phase={phase}
            aria-label={page.title}
          />
        </>
      )}
    </>
  );
}
