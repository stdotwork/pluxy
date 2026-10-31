import { expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

// Isolate browser globals and the module-level theme state from other tests.
test('wallpaper persistence failures preserve the previous selection and image', () => {
  const result = Bun.spawnSync([process.execPath, '-e', `
    import { strict as assert } from 'node:assert';
    const imageKey = 'easy-cli-proxy-api.anime-backdrop-image.v1';
    const presetKey = 'easy-cli-proxy-api.anime-backdrop-preset.v1';
    const saved = new Map();
    let failingKey;
    globalThis.document = { querySelector: () => null };
    globalThis.window = { localStorage: {
      getItem: key => saved.get(key) ?? null,
      setItem(key, value) {
        if (key === failingKey) throw new DOMException('Full', 'QuotaExceededError');
        saved.set(key, value);
      },
      removeItem(key) {
        if (key === failingKey) throw new DOMException('Denied', 'SecurityError');
        saved.delete(key);
      },
    }};
    const theme = await import('./src/theme.ts');
    const original = 'data:image/webp;base64,AAAA';
    const replacement = 'data:image/webp;base64,BBBB';
    assert.equal(theme.setCustomAnimeBackdropImage(original), true);
    assert.equal(saved.get(imageKey), original);
    assert.equal(saved.get(presetKey), 'custom');
    const previous = theme.getAnimeBackdropInfo();
    for (const key of [imageKey, presetKey]) {
      failingKey = key;
      for (const value of [replacement, null]) {
        assert.equal(theme.setCustomAnimeBackdropImage(value), false);
        assert.deepEqual(theme.getAnimeBackdropInfo(), previous);
        assert.equal(saved.get(imageKey), original);
        assert.equal(saved.get(presetKey), 'custom');
      }
    }
    assert.equal(theme.setAnimeBackdropPreset('none'), false);
    assert.deepEqual(theme.getAnimeBackdropInfo(), previous);
    failingKey = undefined;
    assert.equal(theme.setCustomAnimeBackdropImage(replacement), true);
    assert.equal(saved.get(imageKey), replacement);
    assert.equal(theme.setCustomAnimeBackdropImage(null), true);
    assert.equal(saved.has(imageKey), false);
    assert.equal(saved.get(presetKey), 'starlight');
    assert.deepEqual(theme.getAnimeBackdropInfo(), {preset: 'starlight', hasCustomImage: false});
    assert.equal(theme.setAnimeBackdropPreset('none'), true);
    assert.equal(saved.get(presetKey), 'none');
  `], { cwd: fileURLToPath(new URL('..', import.meta.url)) });
  expect(result.stderr.toString()).toBe('');
  expect(result.exitCode).toBe(0);
});
