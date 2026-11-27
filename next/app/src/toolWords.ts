// What the tool pages show of what the native side tells them. Two things happen here, each in one
// place: a native error's words become the ones a person reads (nativeReply: protocol error codes
// are dropped or turned into plain sentences, and the app's native side and its tools name pluxy
// "core", a key a "client key" and the app's key store "the vault"; no page says those words), and,
// with privacy mode on, paths, names and free text are masked as they are shown (useShown). Every
// native call the tool pages make goes through nativeReply.

import type { Reply } from "../../ui/kit/host";
import { usePrivacy } from "../../ui/kit/privacy";
import { shownPath } from "../../ui/kit/save";
import { reply } from "./host";

/** The native side's words and the ones a person reads, in the order they are replaced. */
const WORDS: [RegExp, string][] = [
  [/\bthe core at\b/gi, "pluxy at"],
  [/\b(?:the |a )?core's\b/gi, "pluxy's"],
  [/\banother core\b/gi, "another pluxy"],
  [/\b(?:the |a )?core\b/gi, "pluxy"],
  [/\bclient keys\b/gi, "keys"],
  [/\bclient key\b/gi, "key"],
  [/\bthis device's vault\b/gi, "the app"],
  [/\b(?:the |its )?vault\b/gi, "the app"],
  [/\bnative confirmation\b/gi, "confirmation window"],
];

/** Extension protocol error codes (`next/protocol/README.md`, "Errors", plus `incompatible`) and
 * the plain sentence shown if an add-on gives no message of its own. */
const PROTOCOL_CODES: Record<string, string> = {
  invalid_request: "The add-on rejected the request.",
  auth: "The sign-in was not accepted.",
  permission: "The add-on does not have permission to do that.",
  account_closed: "The account is closed.",
  not_found: "What the add-on looked for could not be found.",
  rate_limit: "Too many requests. Try again in a moment.",
  quota: "The usage limit has been reached.",
  unsupported: "This add-on does not support that.",
  upstream: "The service returned an error.",
  unavailable: "The add-on is not available right now.",
  cancelled: "The action was cancelled.",
  internal: "The add-on ran into an unexpected problem.",
  extension_crashed: "The add-on stopped unexpectedly.",
  extension_protocol: "The add-on sent an unexpected reply.",
  extension_unavailable: "The add-on is not available.",
  extension_timeout: "The add-on took too long to answer.",
  incompatible: "This add-on is not compatible with this version of pluxy.",
};

/** Removes a leading protocol error code (`code: message`), or turns a bare code into a plain
 * sentence; bare `"cancelled"` stays as-is so callers that check for `"cancelled"` still see it. */
function withoutProtocolCode(message: string): string {
  const trimmed = message.trim();
  const colon = trimmed.indexOf(":");
  if (colon > 0) {
    const code = trimmed.slice(0, colon).trim().toLowerCase();
    const fallback = PROTOCOL_CODES[code];
    if (fallback) {
      const rest = trimmed.slice(colon + 1).trim();
      if (
        rest &&
        rest.toLowerCase() !== code &&
        !(rest.toLowerCase() in PROTOCOL_CODES) &&
        /[\p{L}\p{N}]/u.test(rest)
      ) {
        return rest;
      }
      return fallback;
    }
  }
  const lower = trimmed.toLowerCase();
  if (lower !== "cancelled" && lower in PROTOCOL_CODES) {
    return PROTOCOL_CODES[lower] ?? trimmed;
  }
  return message;
}

/** A native error in the words a person reads. */
export const plainError = (message: string) =>
  WORDS.reduce((m, [from, to]) => m.replace(from, to), withoutProtocolCode(message));

/** A reply whose error, if any, is in the words a person reads. */
export function plainReply<T>(r: Reply<T>): Reply<T> {
  return r.error ? { ...r, error: { ...r.error, message: plainError(r.error.message) } } : r;
}

/** A native command's reply for a tool page: host.ts's reply, its error in plain words. */
export const nativeReply = <T>(cmd: string, args?: Record<string, unknown>): Promise<Reply<T>> =>
  reply<T>(cmd, args).then(plainReply);

/** A home folder's user name in a path (/home/<user>, /Users/<user>, C:\Users\<user>). */
const HOME_USER = /([\\/](?:home|Users|users)[\\/])([^\\/\s"'`]+)/g;
/** An absolute path in free text: from a slash, a drive or ~, two or more parts. */
const PATH_IN_TEXT = /(?<![\w.:/\\-])(?:[A-Za-z]:|~)?(?:[\\/][^\s\\/"'`,;()]+){2,}[\\/]?/g;

/** A path as privacy mode shows it: the home folder's user name hidden, then only its last two
 * parts (the kit's shownPath). */
export function privatePath(path: string, on: boolean): string {
  if (!on) return path;
  return shownPath(
    path.replace(HOME_USER, (_, root: string) => `${root}***`),
    true,
  );
}

/** The masks a tool page uses for what it shows: path for a path, text for free text from the
 * native side or a tool (its paths, e-mail addresses and URL credentials masked), id for a name
 * such as a WSL user or an account ID. Each passes a value through unchanged with privacy mode off. */
export function useShown() {
  const p = usePrivacy();
  const path = (x: string) => privatePath(x, p.on);
  function text(x: string): string;
  function text(x: string | undefined): string | undefined;
  function text(x: string | undefined) {
    if (!p.on || !x) return x;
    return p.text(x).replace(PATH_IN_TEXT, (m) => privatePath(m, true));
  }
  return { on: p.on, path, text, id: p.id };
}
