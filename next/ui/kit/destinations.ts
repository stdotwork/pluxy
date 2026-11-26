// Where a page sits in the frame. Pages are one flat list (core's, the host build's and the
// extensions' arrive separately and are joined): each names its destination, and optionally the
// page it sits under. The functions here turn that list into what the frame draws around a page.

import type { VNode } from "preact";

/** The navigation's entries, in the order it shows them. Settings sits at the navigation's foot. */
export const DESTINATIONS = ["home", "accounts", "activity", "tools", "settings"] as const;
export type Destination = (typeof DESTINATIONS)[number];

/**
 * The labelled groups a destination's topics are listed in, in order: pluxy's own, this
 * device's (the desktop app's), and the pages extensions bring.
 */
export const TOPIC_SECTIONS = ["core", "device", "extensions"] as const;
export type TopicSection = (typeof TOPIC_SECTIONS)[number];

/**
 * A page. Extension pages are pages too: their render mounts the extension's isolated document,
 * so the shell and the theme hooks treat them like any other.
 */
export interface PageDef {
  /**
   * Stable ID: the address is #/<id>, and the root element carries data-page="<id>". An
   * extension page's ID is ext/<publisher>/<name>/<page>.
   */
  id: string;
  title: string;
  /** The navigation entry the page belongs to. A destination with no page is not shown. */
  dest: Destination;
  /**
   * How the page is listed inside its destination. "tab": one of the tabs in the header strip.
   * "topic": one of the topics listed beside the page. Left out: the page is reached from the
   * navigation entry itself (when it is the destination's first page) or from another page.
   */
  kind?: "tab" | "topic";
  /** The ID of the page this one sits under: the header strip shows the way back to it. */
  under?: string;
  /**
   * Other addresses the page answers besides its ID: an exact old ID (such as a page it replaced),
   * or a prefix ending in "/" that the page answers for whatever follows it. Where several pages
   * match an address, the longest match wins (shell.tsx, currentId).
   */
  addresses?: string[];
  /** The page's name in its tab or topic entry, when the title does not fit there. */
  label?: string;
  /** The group a topic is listed in. Default: "core". */
  section?: TopicSection;
  /** The tooltip of the page's tab or topic entry, a few words on what it is for. Default: the title. */
  tip?: string;
  render: () => VNode;
}

/** The page a destination's navigation entry opens: its first page that sits under no other. */
export function destinationHome(pages: PageDef[], dest: Destination): PageDef | undefined {
  return pages.find((p) => p.dest === dest && p.under === undefined);
}

/** What the frame draws around one page. */
export interface Placement {
  dest: Destination;
  /** The pages above this one, outermost first. Empty for a page that sits under no other. */
  above: PageDef[];
  /** The destination's tabs, when this page is one of at least two. */
  tabs: PageDef[];
  /** The destination's topics by section, when it has any; sections with none are left out. */
  topics: { section: TopicSection; pages: PageDef[] }[];
  /** The tab or topic that is current: this page, or the one it sits under. */
  current?: string;
}

export function place(pages: PageDef[], id: string): Placement | undefined {
  const page = pages.find((p) => p.id === id);
  if (!page) return undefined;
  const above: PageDef[] = [];
  for (let at = page; at.under !== undefined; ) {
    const parent = pages.find((p) => p.id === at.under);
    // A page that names a missing parent, or one above itself, is shown as sitting under none.
    if (!parent || parent === page || above.includes(parent)) break;
    above.unshift(parent);
    at = parent;
  }
  const inDest = pages.filter((p) => p.dest === page.dest);
  const allTabs = inDest.filter((p) => p.kind === "tab");
  const tabs = page.kind === "tab" && allTabs.length > 1 ? allTabs : [];
  const listed = inDest.filter((p) => p.kind === "topic");
  const topics = TOPIC_SECTIONS.map((section) => ({
    section,
    pages: listed.filter((p) => (p.section ?? "core") === section),
  })).filter((g) => g.pages.length > 0);
  const current = [page, ...above.slice().reverse()].find((p) => p.kind !== undefined)?.id;
  return { dest: page.dest, above, tabs, topics, current };
}
