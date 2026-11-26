// A disposable user profile for a native run: a HOME and XDG folders under one root, and the
// cleared environment the app runs in (no display or session bus: the run's own session adds them).

import { mkdirSync } from "node:fs";
import { join } from "node:path";

export function makeProfile(root: string, runtime = join(root, "runtime")) {
  const dirs = {
    home: join(root, "home"),
    config: join(root, "config"),
    data: join(root, "data"),
    cache: join(root, "cache"),
    state: join(root, "state"),
    runtime,
  };
  for (const d of Object.values(dirs)) mkdirSync(d, { recursive: true, mode: 0o700 });
  const env: Record<string, string> = {
    PATH: process.env.PATH ?? "/usr/bin:/bin",
    LANG: "C.UTF-8",
    HOME: dirs.home,
    XDG_CONFIG_HOME: dirs.config,
    XDG_DATA_HOME: dirs.data,
    XDG_CACHE_HOME: dirs.cache,
    XDG_STATE_HOME: dirs.state,
    XDG_RUNTIME_DIR: dirs.runtime,
    WEBKIT_DISABLE_DMABUF_RENDERER: "1",
    LIBGL_ALWAYS_SOFTWARE: "1",
    NO_AT_BRIDGE: "1",
    GTK_USE_PORTAL: "0",
    // The FUSE-free way to run an AppImage, which the session's smoke AppImage uses.
    APPIMAGE_EXTRACT_AND_RUN: "1",
  };
  return { dirs, env };
}
