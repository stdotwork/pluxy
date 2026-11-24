// Times as the kit shows them. A page calls when(value) and gets a <time> that reads at a glance
// ("5m ago", "yesterday 14:02", "in 2h 5m"), carries the exact local time as its title and keeps
// itself current; the words follow docs/terms.md.

import type { VNode } from "preact";
import { useEffect, useState } from "preact/hooks";
import { strings } from "./strings";

const t = strings("time", {
  justNow: "just now",
  minutesAgo: "{count}m ago",
  hoursAgo: "{count}h ago",
  yesterday: "yesterday {time}",
  in: "in {duration}",
  seconds: "{count}s",
  minutes: "{count}m",
  hours: "{count}h",
  hoursMinutes: "{hours}h {minutes}m",
  days: "{count}d",
  daysHours: "{days}d {hours}h",
});

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;

/**
 * A time from core: seconds since 1970 as a number, or a date string. Nothing for a missing,
 * zero or unreadable one.
 */
export function parseTime(value?: string | number): Date | undefined {
  if (value === undefined || value === 0 || value === "") return undefined;
  const d = typeof value === "number" ? new Date(value * 1000) : new Date(value);
  return Number.isNaN(d.getTime()) ? undefined : d;
}

/** The exact local date and time, with seconds. */
export function fullTime(d: Date, locale?: string): string {
  return d.toLocaleString(locale);
}

/**
 * A length of time in at most two units, rounded up so a countdown never says less than is left:
 * 45s, 5m, 2h 5m, 3d 4h.
 */
export function shortDuration(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds));
  if (s < MINUTE) return t("seconds", { count: s });
  const m = Math.ceil(s / MINUTE);
  if (m < 60) return t("minutes", { count: m });
  if (m < 24 * 60) {
    const [hours, minutes] = [Math.floor(m / 60), m % 60];
    return minutes ? t("hoursMinutes", { hours, minutes }) : t("hours", { count: hours });
  }
  const h = Math.ceil(s / HOUR);
  const [days, hours] = [Math.floor(h / 24), h % 24];
  return hours ? t("daysHours", { days, hours }) : t("days", { count: days });
}

/** A countdown longer than this many days says nothing at a glance: the date shows instead. */
export const LONGEST_COUNTDOWN_DAYS = 99;

/** For a past time, hours ago are counted up to this many hours back into the day before. */
const HOURS_AGO_INTO_YESTERDAY = 6;

const startOfDay = (ms: number, daysBack = 0) => {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() - daysBack).getTime();
};

const clock = (d: Date, locale?: string) =>
  d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });

/** The day without the time: "Nov 3", with the year when it is not this year. */
function shortDate(d: Date, now: number, locale?: string): string {
  const year = d.getFullYear() === new Date(now).getFullYear() ? undefined : "numeric";
  return d.toLocaleDateString(locale, { month: "short", day: "numeric", year });
}

/**
 * A time as its distance from now. A past one: "just now" under a minute, "5m ago" under an
 * hour, "3h ago" for the rest of today (and the last few hours of yesterday), "yesterday 14:02",
 * then the date. A coming one: "in 2h 5m", or the date when it is too far off to count down to.
 */
export function relativeTime(d: Date, now: number, locale?: string): string {
  const ago = (now - d.getTime()) / 1000;
  if (ago <= -1) {
    return -ago > LONGEST_COUNTDOWN_DAYS * DAY
      ? shortDate(d, now, locale)
      : t("in", { duration: shortDuration(-ago) });
  }
  if (ago < MINUTE) return t("justNow");
  if (ago < HOUR) return t("minutesAgo", { count: Math.floor(ago / MINUTE) });
  if (d.getTime() >= startOfDay(now) || ago < HOURS_AGO_INTO_YESTERDAY * HOUR) {
    return t("hoursAgo", { count: Math.floor(ago / HOUR) });
  }
  if (d.getTime() >= startOfDay(now, 1)) return t("yesterday", { time: clock(d, locale) });
  return shortDate(d, now, locale);
}

/**
 * The exact time in little room, for lists a person scans by time: the time with seconds for
 * today, and the short date before it on any other day.
 */
export function compactTime(d: Date, now: number, locale?: string): string {
  if (d.getTime() >= startOfDay(now) && d.getTime() < startOfDay(now, -1))
    return d.toLocaleTimeString(locale);
  const year = d.getFullYear() === new Date(now).getFullYear() ? undefined : "numeric";
  return d.toLocaleString(locale, {
    month: "short",
    day: "numeric",
    year,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

/** The time for programs: a date string as core wrote it (it may be finer than a Date holds). */
const machineTime = (value: string | number, d: Date) =>
  typeof value === "string" ? value : d.toISOString();

export type TimeMode = "relative" | "exact";

const text = (d: Date, mode: TimeMode, now: number) =>
  mode === "exact" ? compactTime(d, now) : relativeTime(d, now);

/** A time as text, as it reads now; it does not keep itself current. For a title or a sentence. */
export function whenText(value?: string | number, mode: TimeMode = "relative"): string | undefined {
  const d = parseTime(value);
  return d && text(d, mode, Date.now());
}

// One timer for every time on the page, running only while one is shown.
const shown = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | undefined;
const tick = () => {
  if (typeof document !== "undefined" && document.hidden) return;
  for (const update of shown) update();
};

function eachSecond(update: () => void): () => void {
  shown.add(update);
  if (shown.size === 1) {
    timer = setInterval(tick, 1000);
    document.addEventListener("visibilitychange", tick);
  }
  return () => {
    shown.delete(update);
    if (shown.size > 0) return;
    clearInterval(timer);
    document.removeEventListener("visibilitychange", tick);
  };
}

/**
 * A time that reads at a glance and stays current: only its own text is drawn again, and only
 * when it changes. mode "exact" is the compact exact time, for lists scanned by time. The title
 * is the exact local time.
 */
export function Time(props: { value: string | number; mode?: TimeMode }) {
  const d = parseTime(props.value);
  const at = d?.getTime();
  const mode = props.mode ?? "relative";
  const read = () => (at === undefined ? "" : text(new Date(at), mode, Date.now()));
  const [, setShown] = useState(read);
  useEffect(() => {
    if (at === undefined) return;
    return eachSecond(() => setShown(read()));
  }, [at, mode]);
  if (!d) return null;
  return (
    <time dateTime={machineTime(props.value, d)} title={fullTime(d)} data-part="time">
      {read()}
    </time>
  );
}

/** A time for a page to show (see Time), or nothing for a missing or unreadable one. */
export function when(value?: string | number, mode?: TimeMode): VNode | undefined {
  return parseTime(value) ? <Time value={value as string | number} mode={mode} /> : undefined;
}
