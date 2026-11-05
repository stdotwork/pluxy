const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const base = process.env.BASE_URL || 'http://127.0.0.1:1421';
const widths = [1704, 900, 560, 480, 380];
const themes = ['light', 'dark', 'graphite-dark', 'warm-sand-light', 'sakura-light'];
const distance = (a, b) => Math.max(...a.slice(0, 3).map((value, index) => Math.abs(value - b[index])));

async function measure(page, width, theme) {
  await page.setViewportSize({ width, height: 1100 });
  const applied = await page.evaluate(async theme => {
    const mod = await import('/src/theme.ts');
    mod.initializeTheme().setPreference(theme);
    await document.fonts.ready;
    await new Promise(resolve => setTimeout(resolve, 250));
    return { theme: document.documentElement.dataset.theme, resolved: mod.initializeTheme().getResolvedThemeId() };
  }, theme);
  await page.mouse.move(0, 0);
  const sample = await page.evaluate(() => {
    const box = element => {
      const r = element.getBoundingClientRect();
      const s = getComputedStyle(element);
      return { x: r.x + scrollX, y: r.y + scrollY, right: r.right + scrollX, bottom: r.bottom + scrollY, width: r.width, height: r.height,
        border: [s.borderTopWidth, s.borderRightWidth, s.borderBottomWidth, s.borderLeftWidth].map(parseFloat),
        background: s.backgroundColor, radius: s.borderRadius, shadow: s.boxShadow, transform: s.transform, outline: s.outline };
    };
    const grid = document.querySelector('.usage-stat-grid');
    const cards = [...grid.querySelectorAll('.usage-stat-card')].map(box);
    const cols = getComputedStyle(grid).gridTemplateColumns.split(' ').length;
    const points = {};
    const faceChecks = {};
    const facePoint = (name, point, index) => { points[name] = point; faceChecks[name] = index; };
    cards.forEach((card, i) => {
      points[`face${i}`] = [card.right - 8, card.y + 5];
      if (i % cols === 0) facePoint(`rowStart${i}`, [card.x + 0.5, card.y + card.height / 2], i);
      if (i && Math.abs(card.y - cards[i - 1].y) < 0.1) {
        points[`vertical${i}`] = [(cards[i - 1].right + card.x) / 2, card.y + 5];
        facePoint(`beforeVertical${i}`, [cards[i - 1].right - 0.5, card.y + 5], i - 1);
        facePoint(`afterVertical${i}`, [card.x + 0.5, card.y + 5], i);
      }
      const above = cards.slice(0, i).findLast(previous => Math.abs(previous.x - card.x) < 0.1 && previous.bottom < card.y);
      if (above) {
        points[`horizontal${i}`] = [card.right - 8, (above.bottom + card.y) / 2];
        facePoint(`beforeHorizontal${i}`, [card.right - 8, above.bottom - 0.5], cards.indexOf(above));
        facePoint(`afterHorizontal${i}`, [card.right - 8, card.y + 0.5], i);
      }
    });
    for (const name of ['trend', 'health']) {
      const card = box(document.querySelector(`.usage-${name}-panel`));
      points[name] = [card.right - 24, card.y + 5];
    }
    return { origin: location.origin, width: innerWidth, cols, grid: box(grid), cards, points, faceChecks };
  });
  const screenshot = await page.screenshot({ fullPage: true });
  sample.pixels = await page.evaluate(async ({ png, points }) => {
    const image = new Image();
    image.src = `data:image/png;base64,${png}`;
    await image.decode();
    const canvas = document.createElement('canvas');
    canvas.width = image.width;
    canvas.height = image.height;
    const context = canvas.getContext('2d');
    context.drawImage(image, 0, 0);
    return Object.fromEntries(Object.entries(points).map(([key, [x, y]]) => [key, [...context.getImageData(Math.floor(x * devicePixelRatio), Math.floor(y * devicePixelRatio), 1, 1).data]]));
  }, { png: screenshot.toString('base64'), points: sample.points });
  if (process.env.SHOTS_DIR) fs.writeFileSync(`${process.env.SHOTS_DIR}/${width}-${theme}.png`, screenshot);
  return { ...sample, ...applied };
}

function verify(sample, width, theme) {
  assert.equal(sample.width, width);
  assert.equal(sample.resolved, theme);
  assert.equal(sample.theme, theme.endsWith('dark') ? 'dark' : 'light');
  assert.equal(sample.cols, width >= 900 ? 6 : width >= 560 ? 3 : 2);
  const near = (a, b, label) => assert.ok(Math.abs(a - b) < 0.05, `${label}: ${a} versus ${b}`);
  for (const [i, card] of sample.cards.entries()) {
    if (i % sample.cols) near(card.x - sample.cards[i - 1].right, 1, `card ${i} horizontal gap`);
    else near(card.x, sample.grid.x + sample.grid.border[3], `card ${i} row start`);
    if (i >= sample.cols) near(card.y - sample.cards[i - sample.cols].bottom, 1, `card ${i} vertical gap`);
    else near(card.y, sample.grid.y + sample.grid.border[0], `card ${i} first row`);
    if (i % sample.cols === sample.cols - 1) near(card.right, sample.grid.right - sample.grid.border[1], `card ${i} right edge`);
    if (i >= sample.cards.length - sample.cols) near(card.bottom, sample.grid.bottom - sample.grid.border[2], `card ${i} bottom edge`);
    const panelDistance = Math.min(...['trend', 'health'].map(panel => distance(sample.pixels[`face${i}`], sample.pixels[panel])));
    assert.ok(panelDistance <= 4, `card ${i} face matches adjacent panels: distance ${panelDistance}, pixel ${sample.pixels[`face${i}`]}`);
  }
  for (const [name, index] of Object.entries(sample.faceChecks)) {
    const delta = distance(sample.pixels[name], sample.pixels[`face${index}`]);
    assert.ok(delta <= 4, `${name} remains card face: distance ${delta}, pixel ${sample.pixels[name]}`);
  }
  for (const [name, pixel] of Object.entries(sample.pixels)) {
    if (!/^(vertical|horizontal)/.test(name)) continue;
    assert.ok(distance(pixel, sample.pixels[`face${name.match(/\d+$/)[0]}`]) >= 12, `${name} visible contrast: ${pixel}`);
  }
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  const samples = [];
  const failures = [];
  try {
    const page = await browser.newPage({ viewport: { width: 1704, height: 1100 }, deviceScaleFactor: 2 });
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    await page.goto(base + '/?mock=running&mockDelay=5');
    await page.locator('.app-shell').waitFor();
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('app:navigate', { detail: 'usage-records' })));
    await page.locator('.usage-stat-grid').waitFor();
    for (const width of widths) for (const theme of themes) {
      const sample = await measure(page, width, theme);
      samples.push(sample);
      try { verify(sample, width, theme); }
      catch (error) { failures.push(`${width}/${theme}: ${error.message}`); }
    }
    if (process.env.RESULT_PATH) fs.writeFileSync(process.env.RESULT_PATH, JSON.stringify({ samples, failures }, null, 2));
    assert.deepEqual(failures, []);
    console.log(`PASS: ${samples.length} real viewport/theme geometry and pixel samples`);
  } finally { await browser.close(); }
}

module.exports = { measure, verify, widths, themes, distance };
if (require.main === module) main().catch(error => { console.error(error); process.exitCode = 1; });
