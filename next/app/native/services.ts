// The desktop services each native run's private session starts (session.ts), and the packages
// they come from (Ubuntu 24.04 names, the runner CI pins). A run that finds one missing fails
// at setup, naming the package: it never skips what the services are there to check.

import { existsSync } from "node:fs";

export interface Service {
  /** What it is, in the words of a failure message. */
  what: string;
  /** The Ubuntu package that ships it. */
  package: string;
  /** The program the session starts. */
  file: string;
}

/** The portal frontend; it starts the document portal and the permission store on demand. */
export const portalFrontend: Service = {
  what: "the portal frontend",
  package: "xdg-desktop-portal",
  file: "/usr/libexec/xdg-desktop-portal",
};
export const documentPortal: Service = {
  what: "the document portal",
  package: "xdg-desktop-portal",
  file: "/usr/libexec/xdg-document-portal",
};
export const gtkBackend: Service = {
  what: "the gtk portal backend",
  package: "xdg-desktop-portal-gtk",
  file: "/usr/libexec/xdg-desktop-portal-gtk",
};
/** Its service owns org.kde.StatusNotifierWatcher, the host a tray icon registers with. */
export const trayWatcher: Service = {
  what: "the tray watcher service",
  package: "ayatana-indicator-application",
  file: "/usr/libexec/ayatana-indicator-application/ayatana-indicator-application-service",
};

/** Builds the disposable AppImage the session runs (appimage.ts); unsquashfs unpacks the
 * AppImages the release gate checks. */
const squashfs = { package: "squashfs-tools" };

export const busNames = {
  desktop: "org.freedesktop.portal.Desktop",
  documents: "org.freedesktop.portal.Documents",
  permissionStore: "org.freedesktop.impl.portal.PermissionStore",
  gtkBackend: "org.freedesktop.impl.portal.desktop.gtk",
  watcher: "org.kde.StatusNotifierWatcher",
};

/** What is missing from this machine, one line each, before anything is built. */
export function missingPrerequisites(): string[] {
  const missing: string[] = [];
  for (const s of [portalFrontend, documentPortal, gtkBackend, trayWatcher]) {
    if (!existsSync(s.file)) missing.push(`${s.what}: ${s.file} is not there (package ${s.package})`);
  }
  for (const tool of ["mksquashfs", "unsquashfs"]) {
    if (!Bun.which(tool)) missing.push(`${tool}: not on PATH (package ${squashfs.package})`);
  }
  if (!Bun.which("xdotool")) missing.push("xdotool: not on PATH (package xdotool)");
  // Makes the fake release feed's certificate authority (feed.ts).
  if (!Bun.which("openssl")) missing.push("openssl: not on PATH (package openssl)");
  return missing;
}
