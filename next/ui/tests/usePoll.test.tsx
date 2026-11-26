// usePoll's behaviour: ticks while active and visible, never overlaps a pending call, stops when
// inactive, hidden or unmounted, and catches up once when becoming visible after longer than ms.
import { afterEach, expect, jest, test } from "bun:test";
import { useState } from "preact/hooks";
import { act } from "preact/test-utils";
import { usePoll } from "../kit/ui";
import { mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

async function advance(ms: number) {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
  await settle();
}

test("ticks every interval while active and visible", async () => {
  jest.useFakeTimers();
  let calls = 0;
  function Probe() {
    usePoll(() => {
      calls++;
    }, 5_000);
    return null;
  }
  await mountIn(<Probe />);
  expect(calls).toBe(0);

  await advance(4_999);
  expect(calls).toBe(0);

  await advance(1);
  expect(calls).toBe(1);

  await advance(10_000);
  expect(calls).toBe(3);
});

test("does not start a new call while the previous one is still running", async () => {
  jest.useFakeTimers();
  let calls = 0;
  let finish!: () => void;
  function Probe() {
    usePoll(
      () =>
        new Promise<void>((resolve) => {
          calls++;
          finish = resolve;
        }),
      1_000,
    );
    return null;
  }
  await mountIn(<Probe />);

  await advance(1_000);
  expect(calls).toBe(1);

  await advance(3_000);
  expect(calls).toBe(1);

  finish();
  await settle();

  await advance(1_000);
  expect(calls).toBe(2);
});

test("stops ticking when inactive, hidden or unmounted", async () => {
  jest.useFakeTimers();
  let calls = 0;
  let setActive!: (next: boolean) => void;
  function Probe() {
    const [active, update] = useState(true);
    setActive = update;
    usePoll(
      () => {
        calls++;
      },
      1_000,
      active,
    );
    return null;
  }
  const mounted = await mountIn(<Probe />);

  await advance(1_000);
  expect(calls).toBe(1);

  // Inactive stops polling.
  await act(async () => setActive(false));
  await advance(3_000);
  expect(calls).toBe(1);

  // Active again resumes polling.
  await act(async () => setActive(true));
  await advance(1_000);
  expect(calls).toBe(2);

  // Hidden stops polling.
  mounted.document.visibilityState = "hidden";
  mounted.document.dispatch("visibilitychange");
  await advance(3_000);
  expect(calls).toBe(2);

  // Unmount stops polling and removes its listener.
  await mounted.unmount();
  mounted.document.visibilityState = "visible";
  mounted.document.dispatch("visibilitychange");
  await advance(5_000);
  expect(calls).toBe(2);
  expect(mounted.document.count("visibilitychange")).toBe(0);
});

test("catches up once on becoming visible after longer than the interval, not sooner", async () => {
  jest.useFakeTimers();
  let calls = 0;
  function Probe() {
    usePoll(() => {
      calls++;
    }, 5_000);
    return null;
  }
  const mounted = await mountIn(<Probe />);

  // Hidden for less than the interval: no immediate catch-up.
  mounted.document.visibilityState = "hidden";
  mounted.document.dispatch("visibilitychange");
  await advance(2_000);
  mounted.document.visibilityState = "visible";
  mounted.document.dispatch("visibilitychange");
  expect(calls).toBe(0);

  // Still ticks at the interval mark.
  await advance(3_000);
  expect(calls).toBe(1);

  // Hidden for longer than the interval: catches up once straight away on becoming visible.
  mounted.document.visibilityState = "hidden";
  mounted.document.dispatch("visibilitychange");
  await advance(12_000);
  expect(calls).toBe(1);

  mounted.document.visibilityState = "visible";
  mounted.document.dispatch("visibilitychange");
  await settle();
  expect(calls).toBe(2);

  // Next tick is a full interval after the catch-up, not immediately.
  await advance(4_999);
  expect(calls).toBe(2);
  await advance(1);
  expect(calls).toBe(3);
});
