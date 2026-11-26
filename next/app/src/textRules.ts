// What the tool add-ons check in the text the pages send (next/app-tools/*-setup), in the same
// terms, so a page holds Connect for exactly the text its add-on would refuse. The add-ons are
// Rust: str::trim trims Unicode White_Space, char::is_whitespace is White_Space, char::is_control
// is the Cc category, and chars().count() counts code points.

/** s trimmed as the add-ons trim it (Rust's str::trim: White_Space at both ends). */
export const trimText = (s: string) => s.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");

/** How many characters s has, as the add-ons count them (code points). */
export const length = (s: string) => [...s].length;

/** Whether s holds a control character (Cc). */
export const hasControl = (s: string) => /\p{Cc}/u.test(s);

/** Whether s holds white space or a control character. */
export const hasSpaceOrControl = (s: string) => /[\p{White_Space}\p{Cc}]/u.test(s);
