// Starts the panel in a design direction: loads the direction, links its stylesheets, then renders
// it with the switcher beside it. Loaded only while a direction is set.

import { render } from "preact";
import { type Design, type DesignProps, loadDesign } from "./index";
import { linkStylesheets } from "./stylesheet";
import { Switcher } from "./switcher";
import switcherStyles from "./switcher.css" with { type: "file" };

export async function mountDesign(design: Design, props: DesignProps): Promise<void> {
  const module = await loadDesign(design);
  await linkStylesheets([...(module.styles ?? []), switcherStyles]);
  // A direction's CSS is scoped by this attribute, so it styles nothing in another direction.
  document.documentElement.dataset.design = design;
  const App = module.default;
  render(
    <>
      <App {...props} />
      <Switcher current={design} />
    </>,
    document.body,
  );
}
