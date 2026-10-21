import { beforeEach, describe, expect, mock, test } from 'bun:test';
import { buildPluginResourceRoute } from '../src/services/pluginResources';

// Minimal hook runtime: enough useState/useEffect to drive the hook without a DOM.
type Effect = { deps: unknown[] | undefined; run: () => void | (() => void); cleanup?: void | (() => void) };
let slots: unknown[] = [];
let effects: Effect[] = [];
let cursor = 0;
let dirty = false;

mock.module('react', () => ({
  useState: (initial: unknown) => {
    const i = cursor++;
    if (!(i in slots)) slots[i] = typeof initial === 'function' ? (initial as () => unknown)() : initial;
    return [slots[i], (next: unknown) => { slots[i] = typeof next === 'function' ? (next as (v: unknown) => unknown)(slots[i]) : next; dirty = true; }];
  },
  useEffect: (run: Effect['run'], deps?: unknown[]) => {
    const i = cursor++;
    const prev = effects[i];
    if (prev && deps && prev.deps && deps.every((d, j) => Object.is(d, prev.deps![j]))) return;
    prev?.cleanup && (prev.cleanup as () => void)();
    effects[i] = { deps, run };
  },
}));

let store = new Map<string, string>();
let listCalls: { resolve: (v: unknown) => void; reject: (e: unknown) => void }[] = [];
mock.module('../src/services/plugins', () => ({
  getPluginSupport: async () => true,
  pluginsApi: { list: () => new Promise((resolve, reject) => { listCalls.push({ resolve, reject }); }) },
}));

const { usePluginNavEntries } = await import('../src/usePluginNavEntries');

const listing = (id: string) => ({
  pluginsEnabled: true,
  plugins: [{ id, effectiveEnabled: true, logo: '', metadata: null, menus: [{ path: '/p', menu: id, description: 'd' }] }],
});
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

let args: [boolean, string | null] = [true, 'local'];
function render() {
  let result: ReturnType<typeof usePluginNavEntries>;
  do {
    dirty = false;
    cursor = 0;
    result = usePluginNavEntries(...args);
    for (const effect of effects) {
      if (effect && !('started' in effect)) { Object.assign(effect, { started: true }); effect.cleanup = effect.run(); }
    }
  } while (dirty);
  return result;
}
const setArgs = (coreReady: boolean, target: string | null) => { args = [coreReady, target]; return render(); };
const cacheKey = (target: string) => `cpa-gui-plugin-nav-entries:${target}`;
const cachedEntry = (id: string) => ({ pluginID: id, menuIndex: 0, label: id, description: 'd', route: buildPluginResourceRoute(id, 0) });

beforeEach(() => {
  slots = []; effects = []; cursor = 0; listCalls = []; args = [true, 'local']; store = new Map();
  (globalThis as { window?: unknown }).window = {
    localStorage: { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v); } },
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  };
});

describe('usePluginNavEntries', () => {
  test('target change drops the previous target entries and ignores its stale response', async () => {
    store.set(cacheKey('local'), JSON.stringify([cachedEntry('alpha')]));
    expect(render().entries.map((e) => e.pluginID)).toEqual(['alpha']);

    await flush();
    expect(setArgs(true, 'https://remote').entries).toEqual([]);
    await flush();
    listCalls[0].resolve(listing('stale-local'));
    await flush();
    expect(render().entries).toEqual([]);
    expect(store.get(cacheKey('local'))).toBe(JSON.stringify([cachedEntry('alpha')]));

    listCalls[1].resolve(listing('beta'));
    await flush();
    const result = render();
    expect(result.entries.map((e) => e.pluginID)).toEqual(['beta']);
    expect(result.fetched).toBe(true);
  });

  test('list failure clears entries and the target cache', async () => {
    store.set(cacheKey('https://remote'), JSON.stringify([cachedEntry('alpha')]));
    args = [true, 'https://remote'];
    expect(render().entries).toHaveLength(1);
    await flush();
    listCalls[0].reject(new Error('unreachable'));
    await flush();
    const result = render();
    expect(result.entries).toEqual([]);
    expect(result.fetched).toBe(false);
    expect(store.get(cacheKey('https://remote'))).toBe('[]');
  });

  test('core not ready hides entries without wiping that target cache', () => {
    store.set(cacheKey('local'), JSON.stringify([cachedEntry('alpha')]));
    render();
    expect(setArgs(false, 'local').entries).toEqual([]);
    expect(store.get(cacheKey('local'))).toBe(JSON.stringify([cachedEntry('alpha')]));
  });
});
