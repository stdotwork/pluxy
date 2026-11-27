// The focused window: no frame and no navigation, the window's ground with pluxy's name at the top
// and one card centred on it. Signing in (the panel), the desktop app's first launch and Setup's
// first run use it, so the way into pluxy looks the same wherever it starts.

import { type ComponentChildren, createContext } from "preact";

/** True inside a page the shell shows in the focused window instead of the frame. */
export const FocusedContext = createContext(false);

/** pluxy's name with its mark, as the focused window's bar shows it. */
export function FocusBrand() {
  return (
    <span class="focus-brand" data-part="focus-brand">
      <span class="focus-mark" aria-hidden="true" />
      <span>pluxy</span>
    </span>
  );
}

export function Focus(props: {
  /** The main element's data-part, for a screen tests and themes name: "focus" by default. */
  part?: string;
  /** What the bar holds after pluxy's name, at its end: Setup's Finish later. */
  bar?: ComponentChildren;
  /** Between the bar and the card: Setup's steps. */
  above?: ComponentChildren;
  /** The card's width: narrow for one question, wide for a step that holds a list. */
  width?: "narrow" | "wide";
  /** Quiet lines under the card. */
  below?: ComponentChildren;
  children: ComponentChildren;
}) {
  return (
    <main class="focus" data-part={props.part ?? "focus"}>
      <header class="focus-bar">
        <FocusBrand />
        {props.bar && <div class="focus-bar-end">{props.bar}</div>}
      </header>
      <div class="focus-stage" data-width={props.width ?? "narrow"}>
        {props.above}
        <div class="focus-card" data-part="focus-card">
          {props.children}
        </div>
        {props.below && <div class="focus-below">{props.below}</div>}
      </div>
    </main>
  );
}

/** The bare ground of the focused window, shown for the moment before its card is known. */
export function FocusGround() {
  return (
    <main class="focus" data-part="focus" aria-busy="true">
      <header class="focus-bar">
        <FocusBrand />
      </header>
    </main>
  );
}
