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

/** Masks text, with or without a value. */
export interface Masker {
  (text: string): string;
  (text: string | undefined): string | undefined;
}

/**
 * A masker for one set of identities (such as the labels of the accounts the text is about),
 * built once for many texts: e-mail addresses and URL credentials anywhere in text, then the
 * identities, also in their file-name forms, in one pass. Addresses go first: an identity masked
 * inside one would leave the rest of the address unmatched, and shown.
 */
export function textMasker(identities: readonly string[]): Masker {
  const replacements = new Map<string, string>();
  for (const identity of identities) {
    const replacement = maskIdentifier(identity);
    for (const variant of identityVariants(identity)) {
      const key = variant.toLowerCase();
      if (!replacements.has(key)) replacements.set(key, replacement);
    }
  }
  const keys = [...replacements.keys()].sort((a, b) => b.length - a.length);
  const known = keys.length > 0 ? new RegExp(keys.map(escapeRegExp).join("|"), "gi") : undefined;
  return ((text: string | undefined) => {
    if (!text) return text;
    const masked = text
      .replace(URL_USERINFO_PATTERN, `$1${MASK}@`)
      .replace(EMAIL_PATTERN, (email) => maskEmail(email));
    // A function: a replacement string would expand `$&`, `$'` and the like in the identity.
    return known ? masked.replace(known, (m) => replacements.get(m.toLowerCase()) ?? MASK) : masked;
  }) as Masker;
}

/** textMasker's masking of one text. */
export function maskSensitiveText(text: string, identities: readonly string[] = []): string {
  return textMasker(identities)(text);
}

/**
 * What names an account's owner, for masking text about it: its label (usually its e-mail
 * address); for a "project (email)" label, as core gives an imported Vertex key, each part; and a
 * Vertex project in its settings, which core also puts in its ID (vertex-<project>).
 */
export function accountIdentities(a: { label?: string; settings?: Record<string, unknown> }): string[] {
  const out: string[] = [];
  const label = a.label?.trim();
  if (label) {
    out.push(label);
    const open = label.indexOf(" (");
    if (open > 0 && label.endsWith(")")) out.push(label.slice(0, open), label.slice(open + 2, -1));
  }
  const vertex = a.settings?.vertex;
  const project =
    typeof vertex === "object" && vertex !== null ? (vertex as { project?: unknown }).project : undefined;
  if (typeof project === "string" && project.trim() && !out.includes(project.trim()))
    out.push(project.trim());
  return out;
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
  /**
   * text's masking for many texts about the same identities, built once. Without identities
   * (useAccountLabels has not loaded them), while privacy mode is on, it hides each text whole.
   */
  masker(identities: readonly (string | undefined)[] | undefined): Masker;
}

const unchanged = ((text: string | undefined) => text) as Masker;
const hidden = ((text: string | undefined) => (text ? MASK : text)) as Masker;
const known = (identities: readonly (string | undefined)[]) => identities.filter((i): i is string => !!i);

export function privacyMasks(on: boolean): Privacy {
  return {
    on,
    id: ((value: string | undefined) => (on && value ? maskIdentifier(value) : value)) as Privacy["id"],
    text: ((value: string | undefined, identities: readonly (string | undefined)[] = []) =>
      on && value ? maskSensitiveText(value, known(identities)) : value) as Privacy["text"],
    masker: (identities) => (!on ? unchanged : identities ? textMasker(known(identities)) : hidden),
  };
}

export const PrivacyContext = createContext<Privacy>(privacyMasks(false));

/** The masks for the current privacy mode; the page draws again when it changes. */
export function usePrivacy(): Privacy {
  return useContext(PrivacyContext);
}

/**
 * The identities of core's accounts while privacy mode is on (none while it is off), for text
 * that names accounts only by ID or shows what a request carried, such as usage records and logs.
 * Undefined while privacy mode is on and they have not loaded (or could not be): pass it to
 * masker, which then hides each text whole. They load again whenever version changes, such as
 * the data the page shows, so accounts added since are known.
 */
const NONE: string[] = [];

export function useAccountLabels(version?: unknown): string[] | undefined {
  const { api } = useKit();
  const { on } = usePrivacy();
  const [labels, setLabels] = useState<string[]>();
  useEffect(() => {
    if (!on) {
      setLabels(undefined);
      return;
    }
    let live = true;
    // A failed load keeps what loaded before, or none: masker then hides each text whole.
    api.accounts().then(
      (r) => {
        if (!live || r.status !== 200 || !r.body) return;
        const next = r.body.accounts.flatMap(accountIdentities);
        // The same identities keep the same array, so what was masked with them stays.
        setLabels((old) => (old && old.join("\n") === next.join("\n") ? old : next));
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [on, api, version]);
  return on ? labels : NONE;
}
