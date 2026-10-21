import { afterEach, describe, expect, it, spyOn } from 'bun:test';
import { startActivePolling } from '../src/services/activePolling';

const interval = spyOn(globalThis, 'setInterval');
const clear = spyOn(globalThis, 'clearInterval');
afterEach(() => { interval.mockRestore(); clear.mockRestore(); });

describe('window activity polling', () => {
  it('creates no hidden timer, refreshes once on resume, and cleans up on hide/dispose', () => {
    const ticks = new Map<number, () => void>();
    let id = 0;
    interval.mockImplementation(((tick: () => void) => { ticks.set(++id, tick); return id; }) as typeof setInterval);
    clear.mockImplementation(((key: number) => { ticks.delete(key); }) as typeof clearInterval);
    let active = false;
    const listeners = new Set<() => void>();
    const source = {
      active: () => active,
      subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
    };
    let requests = 0;
    let pauses = 0;
    const stop = startActivePolling(source, () => requests++, 10_000, () => pauses++);
    expect(ticks.size).toBe(0);
    expect(requests).toBe(0);
    expect(pauses).toBe(1);
    active = true;
    listeners.forEach(listener => listener());
    expect(requests).toBe(1);
    expect(ticks.size).toBe(1);
    const staleTick = [...ticks.values()][0];
    staleTick();
    expect(requests).toBe(2);
    active = false;
    listeners.forEach(listener => listener());
    staleTick(); // Even an already queued timer callback must not refresh while hidden.
    expect(requests).toBe(2);
    expect(ticks.size).toBe(0);
    active = true;
    listeners.forEach(listener => listener());
    expect(requests).toBe(3);
    stop();
    expect(ticks.size).toBe(0);
    expect(listeners.size).toBe(0);
  });
});
