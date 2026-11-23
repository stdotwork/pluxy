// What a direction shows until it is built: today's panel, unchanged, so the switcher can be
// walked through all three. A direction replaces its app.tsx and stops using this.

import { Shell } from "../shell";
import type { DesignProps } from "./index";

export function Placeholder(props: DesignProps) {
  return <Shell {...props} extensionPages safeAddress />;
}
