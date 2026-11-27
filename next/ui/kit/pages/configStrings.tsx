import { strings } from "../strings";

/** The Config page's words, shared by its two tabs. */
export const configText = strings("config", {
  title: "Config",
  tip: "Network, logging, keys and pluxy's settings file",
  settings: "Settings",
  source: "Settings file",
  tryAgain: "Try again",
  cancel: "Cancel",

  // Settings
  network: "Network",
  logging: "Logging",
  keys: "Keys",
  proxy: "Outbound proxy",
  proxyAbout: "Send pluxy's requests to providers through this proxy. Leave empty to connect directly.",
  proxySaved:
    "Saved with a password. It stays while the address stays as it is; a new address needs its own user name and password.",
  proxyNeedsLogin:
    "The saved password belongs to the old address. Type the new address with its user name and password, such as socks5://user:pass@host:1080. For a proxy with no password, empty the field and save first.",
  noAnswerRow: "pluxy did not answer, so this may or may not be saved. Press Enter to send it again.",
  proxyPlaceholder: "socks5://user:pass@host:1080",
  routing: "Account order, retries and cooldowns",
  routingAbout: "These are set in Order & fallback.",
  openRouting: "Open Order & fallback",
  logToFile: "Write pluxy's log to a file",
  logToFileAbout: "Needed to read pluxy's log on the Logs page.",
  size: "Largest log size (MB)",
  sizeAbout: "Older log files are deleted past this size.",
  kept: "Failed requests to keep",
  keptAbout: "How many failed requests keep their full log. 0 keeps all.",
  usage: "Keep a record of requests",
  usageAbout: "Home and Activity show requests and tokens from this record.",
  requestLog: "Log every request",
  requestLogAbout:
    "Keeps a full log of every request, not only failed ones. Turn it on only while looking into a problem: the logs grow fast.",
  requestLogNote:
    "While it is on, the Failed requests tab on Logs lists none: open a request's log from its trace ID.",
  clientKeys: "Keys for coding tools",
  clientKeysAbout: "Made and removed in Tools.",
  openTools: "Open Tools",
  fixFileFirst: "Fix the settings file first.",
  loadingSettings: "Loading settings…",
  settingsFailed: "Could not read pluxy's settings",

  // Both tabs
  fileBroken:
    "pluxy's settings file has a mistake, so it cannot be shown or changed here: {error}. pluxy keeps working with the settings it read before. Fix the file by hand on the computer pluxy runs on, then press Try again.",
  notInUse:
    "pluxy is not using the latest version of its settings file: {error}. It keeps working with the version before.",

  // Settings file
  loadingFile: "Loading the settings file…",
  fileFailed: "Could not read the settings file",
  hint: "Passwords and keys show as placeholders. Leave a placeholder as it is to keep what it stands for.",
  editor: "Settings file",
  find: "Find in file",
  previous: "Previous",
  next: "Next",
  matches: "{current} of {count}",
  noMatches: "No matches",
  found: "{count} found",
  check: "Check for mistakes",
  noMistakes: "No mistakes found.",
  mistake: "There is a mistake: {error}",
  mistakeEmpty: "There is a mistake in the file.",
  reviewSave: "Review and save",
  refresh: "Refresh",
  discardEdits: "Discard edits",
  review: "Review changes",
  removed: "Removed",
  added: "Added",
  save: "Save",
  saved: "Saved.",
  conflict: "The file changed while you were editing. Copy your edits, press Refresh, and make them again.",
  refreshQuestion: "Discard your edits and refresh the file?",
  leaveQuestion: "You have edits that are not saved. Leave and lose them?",
  leave: "Leave",
  stay: "Stay",
  accountNotFound: "No entry matches this account. Look for it by its provider's name.",
  noNewline: "No line break at the end of the file",
});

/** Core's 422 for a settings file that does not parse starts with this; the page says it in its own words. */
const UNPARSED = /^the config file does not parse, so it cannot be shown with its secrets hidden: /;

/** Core's error for a file that does not parse, without core's own lead-in, and without a closing full stop. */
export const fileError = (error: string) => error.replace(UNPARSED, "").trim().replace(/\.$/, "");
