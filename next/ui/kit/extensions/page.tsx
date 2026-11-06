// An extension page in the panel: the extension's own document in a sandboxed frame from the
// pages site, talking to the panel only through the channel (extension-pages/README.md).

import { useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Api, UIExtension, UIPage } from "../api";
import { THEME_VERSION } from "../theme/contract";
import type { Resolved } from "../theme/engine";
import { Button, Notice, PageHead, useKit } from "../ui";
import { type Channel, isHello, type Outcome, pickVersion, serve } from "./channel";
import { pageProblem } from "./site";

/** The theme as a page gets it: tokens and layout, never custom CSS. */
export function themeSnapshot(r: Resolved) {
  return {
    theme_version: THEME_VERSION,
    appearance: r.appearance,
    layout: { ...r.layout },
    tokens: { ...r.tokens },
  };
}

/** Turns a management reply into a channel outcome, never passing on more than the view. */
export async function settingsOutcome(
  call: ReturnType<Api["settings"]>,
  unauthorized: () => void,
): Promise<Outcome> {
  const r = await call;
  if (r.status === 200 && r.body) return { result: { values: r.body.values, set: r.body.set } };
  if (r.status === 401) unauthorized();
  if (r.status === 422) return { error: { code: "invalid_config", message: r.error?.message ?? "refused" } };
  return {
    error: {
      code: "unavailable",
      message: r.status === 0 ? "core could not be reached" : `core answered ${r.status}`,
    },
  };
}

type Phase = "loading" | "connected" | "closed";

/** One extension page, as core lists it. */
export interface ExtensionPageProps {
  ext: UIExtension;
  page: UIPage;
  pagesOrigin?: string;
  pagesError?: string;
}

export function ExtensionPage(props: ExtensionPageProps) {
  const { api, themes, unauthorized } = useKit();
  const { ext, page } = props;
  const frame = useRef<HTMLIFrameElement>(null);
  const [mount, setMount] = useState(0); // a new frame, and a new channel, each time it changes
  const [phase, setPhase] = useState<Phase>("loading");
  const problem = props.pagesError ?? pageProblem(page.url, props.pagesOrigin, location.hostname);

  const permissions = ext.permissions.join(" ");
  // A layout effect: the listener is there before the frame's document can say hello.
  useLayoutEffect(() => {
    if (problem) return;
    let channel: Channel | undefined;
    let hello = false;
    let loads = 0;
    setPhase("loading");

    const close = () => {
      channel?.close();
      channel = undefined;
    };
    const onMessage = (e: MessageEvent) => {
      const win = frame.current?.contentWindow;
      // Only a hello, only from the frame made for this page, and only once: a second document
      // in the frame gets nothing.
      if (!win || e.source !== win || e.origin !== "null" || !isHello(e.data)) return;
      if (hello || loads > 1) return;
      hello = true;
      if (pickVersion(e.data) === undefined) {
        win.postMessage({ pluxy: "host", type: "refused", reason: "no channel version in common" }, "*");
        return;
      }
      const ports = new MessageChannel();
      channel = serve(
        ports.port1,
        { extension: ext.id, permissions: permissions ? permissions.split(" ") : [] },
        {
          theme: () => themeSnapshot(themes.resolved),
          configGet: () => settingsOutcome(api.settings(ext.id), unauthorized),
          configSet: (values) => settingsOutcome(api.changeSettings(ext.id, values), unauthorized),
        },
      );
      // The page's origin is opaque, so the target is "*"; this holds no key and no address.
      win.postMessage(
        {
          pluxy: "host",
          v: 0,
          type: "welcome",
          extension: ext.id,
          page: page.id,
          theme: themeSnapshot(themes.resolved),
        },
        "*",
        [ports.port2],
      );
      setPhase("connected");
    };
    const onLoad = () => {
      loads++;
      if (loads > 1) {
        close();
        setPhase("closed");
      }
    };
    window.addEventListener("message", onMessage);
    const el = frame.current;
    el?.addEventListener("load", onLoad);
    const unsubscribe = themes.subscribe(() => channel?.event("theme", themeSnapshot(themes.resolved)));
    return () => {
      window.removeEventListener("message", onMessage);
      el?.removeEventListener("load", onLoad);
      unsubscribe();
      close();
    };
  }, [mount, problem, ext.id, permissions, page.id, page.url, api, themes, unauthorized]);

  return (
    <>
      <PageHead title={page.title} />
      {problem ? (
        <Notice tone="warn">This extension page cannot be shown: {problem}.</Notice>
      ) : (
        <>
          {phase === "closed" && (
            <div class="actions">
              <Notice tone="warn">
                The page loaded another document, so the panel closed its connection to it.
              </Notice>
              <Button variant="primary" onClick={() => setMount((n) => n + 1)}>
                Load the page again
              </Button>
            </div>
          )}
          <iframe
            key={`${mount} ${page.url}`}
            ref={frame}
            class="extension-frame"
            data-part="extension-frame"
            title={page.title}
            src={page.url}
            sandbox="allow-scripts"
            referrerpolicy="no-referrer"
            allow=""
          />
        </>
      )}
    </>
  );
}
