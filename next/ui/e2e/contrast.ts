// Measures the contrast of text as the browser draws it, for the colour specs: the colour of the
// text, its opacity, and the background colour layers behind it.
import type { Locator } from "@playwright/test";

export interface Measured {
  ratio: number;
  fg: string;
  bg: string;
}

/**
 * The contrast ratio (WCAG 2) of the text of each element the locator matches: the element's
 * computed colour, taking the opacity of it and its ancestors into account, against the
 * background layers behind it. Layers with transparency are composited over the ones below.
 */
export async function measureContrast(locator: Locator): Promise<Measured[]> {
  return locator.evaluateAll((elements) => {
    type Rgba = [number, number, number, number];
    const canvas = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
    const parse = (value: string): Rgba => {
      const nums = (value.match(/-?[\d.]+(?:e-?\d+)?%?/g) ?? []).map((n) => Number.parseFloat(n));
      if (value.startsWith("color(srgb ")) {
        const [r = 0, g = 0, b = 0, a = 1] = nums;
        return [r * 255, g * 255, b * 255, a];
      }
      if (value.startsWith("rgb")) {
        const [r = 0, g = 0, b = 0, a = 1] = nums;
        return [r, g, b, a];
      }
      // Browsers report a colour mid-transition in the space they mix it in, such as oklab().
      if (!canvas || !CSS.supports("color", value)) throw new Error(`cannot read the colour ${value}`);
      canvas.clearRect(0, 0, 1, 1);
      canvas.fillStyle = value;
      canvas.fillRect(0, 0, 1, 1);
      const [r = 0, g = 0, b = 0, a = 0] = canvas.getImageData(0, 0, 1, 1).data;
      return [r, g, b, a / 255];
    };
    const over = (top: Rgba, below: Rgba): Rgba => {
      const a = top[3] + below[3] * (1 - top[3]);
      if (a === 0) return [0, 0, 0, 0];
      const mix = (i: 0 | 1 | 2) => (top[i] * top[3] + below[i] * below[3] * (1 - top[3])) / a;
      return [mix(0), mix(1), mix(2), a];
    };
    const lum = ([r, g, b]: Rgba) => {
      const lin = (c: number) => {
        const s = c / 255;
        return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
    };
    const css = (c: Rgba) => `rgb(${Math.round(c[0])} ${Math.round(c[1])} ${Math.round(c[2])})`;
    return elements.map((element) => {
      // Background layers, from the element up to the root, then the page's white canvas.
      const layers: Rgba[] = [];
      let opacity = 1;
      for (let el: Element | null = element; el; el = el.parentElement) {
        const style = getComputedStyle(el);
        opacity *= Number.parseFloat(style.opacity);
        const bg = parse(style.backgroundColor);
        if (bg[3] > 0) layers.push(bg);
      }
      let bg: Rgba = [255, 255, 255, 1];
      for (const layer of layers.reverse()) bg = over(layer, bg);
      const text = parse(getComputedStyle(element).color);
      // The element's own opacity applies to its text and to its own background alike; the layers
      // above already hold the backgrounds, so composite the text with the opacity of its chain.
      const fg = over([text[0], text[1], text[2], text[3] * opacity], bg);
      const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a) as [number, number];
      return { ratio: (hi + 0.05) / (lo + 0.05), fg: css(fg), bg: css(bg) };
    });
  });
}
