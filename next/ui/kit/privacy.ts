// Privacy mode: the person hides account e-mail addresses and identifiers while sharing their
// screen. It changes only how the UI shows them: pages keep the real values for requests,
// search, filters, copying and downloads, and pass a value through these masks only where it is
// shown (text, title, aria-label). Keys never go through them: they are not privacy fields and
// keep their own handling, in both states. Extension pages are told through the theme event; the
// kit cannot hide what an extension's own page shows.

import { createContext } from "preact";
import { useContext, useEffect, useState } from "preact/hooks";
import type { Host } from "./host";
import { useKit } from "./ui";

const MASK = "***";
// Each pattern starts only where its first run of characters starts (the lookbehind), so a long
// run without a match is read once, not again from each of its positions.
const EMAIL_PATTERN =
  /(?<![A-Za-z0-9._%+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/g;
// Login credentials in a proxy or upstream URL.
const URL_USERINFO_PATTERN = /(?<![A-Za-z0-9+.-])([A-Za-z][A-Za-z0-9+.-]*:\/\/)[^\s/@]+@/g;

const keepPrefix = (value: string, visible: number) => value.slice(0, Math.min(visible, value.length));

function maskOpaque(text: string): string {
  if (text.length <= 4) return MASK;
  const visible = text.length > 10 ? 2 : 1;
  return `${text.slice(0, visible)}${MASK}${text.slice(-visible)}`;
}

export function maskEmail(email: string): string {
  const at = email.lastIndexOf("@");
  if (at < 0) return maskIdentifier(email);
  // No local part ("@alice"): maskIdentifier would hand this value straight back here.
  if (at === 0) return maskOpaque(email.trim());
  const local = email.slice(0, at);
  const domain = email.slice(at + 1);
  const dot = domain.lastIndexOf(".");
  const host = dot > 0 ? domain.slice(0, dot) : domain;
  const suffix = dot > 0 ? domain.slice(dot) : "";
  return `${keepPrefix(local, local.length > 4 ? 2 : 1)}${MASK}@${keepPrefix(host, 1)}${MASK}${suffix}`;
}

/** Masks an identifier such as an account's label, e-mail address or project. */
export function maskIdentifier(value: string): string {
  const text = value.trim();
  if (!text) return value;
  if (text.includes("@")) return maskEmail(text);
  return maskOpaque(text);
}

// Names often hold an address with `@` and `.` replaced (alice_example_com), which the e-mail
// pattern alone misses; core's own rule for account IDs it makes from imported file names
// (next/internal/legacy AccountID) turns every character an ID cannot hold into "-".
function identityVariants(identity: string): string[] {
  const value = identity.trim();
  if (value.length < 3) return [];
  const variants = new Set([value]);
  if (value.includes("@")) {
    variants.add(value.replace(/[@.]/g, "_"));
    variants.add(value.replace(/[@.]/g, "-"));
    variants.add(value.replace("@", "_"));
  }
  variants.add(value.replace(/[^A-Za-z0-9._-]/g, "-"));
  return [...variants].sort((a, b) => b.length - a.length);
}

const escapeRegExp = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/**
 * Masks e-mail addresses and URL credentials anywhere in text, then the given identities (such
 * as the labels of the accounts the text is about), also in their file-name forms. Addresses go
 * first: an identity masked inside one would leave the rest of the address unmatched, and shown.
 */
export function maskSensitiveText(text: string, identities: readonly string[] = []): string {
  if (!text) return text;
  let masked = text
    .replace(URL_USERINFO_PATTERN, `$1${MASK}@`)
    .replace(EMAIL_PATTERN, (email) => maskEmail(email));
  for (const identity of identities) {
    const replacement = maskIdentifier(identity);
    for (const variant of identityVariants(identity)) {
      // A function: a replacement string would expand `$&`, `$'` and the like in the identity.
      masked = masked.replace(new RegExp(escapeRegExp(variant), "gi"), () => replacement);
    }
  }
  return masked;
}

/** Whether privacy mode is on, kept where the host build keeps it. */
export class PrivacyState {
  private listeners = new Set<() => void>();

  /** save keeps a change; it may fail, and the choice then lasts until the window closes. */
  constructor(
    private value: boolean,
    private readonly save: (on: boolean) => unknown,
  ) {}

  get on(): boolean {
    return this.value;
  }

  set(on: boolean) {
    if (on === this.value) return;
    this.value = on;
    try {
      Promise.resolve(this.save(on)).catch(() => console.error("privacy mode: the choice was not saved"));
    } catch {
      console.error("privacy mode: the choice was not saved");
    }
    for (const fn of this.listeners) fn();
  }

  subscribe(fn: () => void) {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }
}

const PREF = "privacy";

/** Privacy mode kept in the host's prefs: the browser panel's way. */
export function prefsPrivacy(host: Host): PrivacyState {
  return new PrivacyState(host.prefs.get(PREF) === "on", (on) => host.prefs.set(PREF, on ? "on" : null));
}

/** The masks as a page uses them: each passes values through unchanged while privacy mode is off. */
export interface Privacy {
  on: boolean;
  /** An account's label or another identifier, shown on its own. */
  id(value: string): string;
  id(value: string | undefined): string | undefined;
  /** A name or free text: e-mail addresses, URL credentials and the given identities masked. */
  text(value: string, identities?: readonly (string | undefined)[]): string;
  text(value: string | undefined, identities?: readonly (string | undefined)[]): string | undefined;
}

export function privacyMasks(on: boolean): Privacy {
  return {
    on,
    id: ((value: string | undefined) => (on && value ? maskIdentifier(value) : value)) as Privacy["id"],
    text: ((value: string | undefined, identities: readonly (string | undefined)[] = []) =>
      on && value
        ? maskSensitiveText(
            value,
            identities.filter((i): i is string => !!i),
          )
        : value) as Privacy["text"],
  };
}

export const PrivacyContext = createContext<Privacy>(privacyMasks(false));

/** The masks for the current privacy mode; the page draws again when it changes. */
export function usePrivacy(): Privacy {
  return useContext(PrivacyContext);
}

/**
 * The labels of core's accounts while privacy mode is on (none while it is off), for text that
 * names accounts only by ID or shows what a request carried, such as usage records and logs.
 */
export function useAccountLabels(): string[] {
  const { api } = useKit();
  const { on } = usePrivacy();
  const [labels, setLabels] = useState<string[]>([]);
  useEffect(() => {
    if (!on) return;
    let live = true;
    api.accounts().then(
      (r) => {
        if (live && r.body) setLabels(r.body.accounts.flatMap((a) => (a.label ? [a.label] : [])));
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [on, api]);
  return on ? labels : [];
}
