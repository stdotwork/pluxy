// The dev switcher shown while a design direction is set: A, B, C, or back to today's panel. Each
// entry is a link that loads the panel again in that direction, on the page it is on.

import { useEffect, useState } from "preact/hooks";
import { DESIGNS, type Design, designAddress } from "./index";

export function Switcher(props: { current: Design }) {
  // The links keep the page: they are made again whenever the address's hash changes.
  const [href, setHref] = useState(location.href);
  useEffect(() => {
    const follow = () => setHref(location.href);
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);
  return (
    <nav class="design-switcher" aria-label="Design direction">
      {DESIGNS.map((d) => (
        <a
          key={d}
          href={designAddress(href, d)}
          aria-current={d === props.current ? "true" : undefined}
          title={`Design direction ${d.toUpperCase()}`}
        >
          {d.toUpperCase()}
        </a>
      ))}
      <a href={designAddress(href, "off")} title="Back to today's panel">
        current
      </a>
    </nav>
  );
}
