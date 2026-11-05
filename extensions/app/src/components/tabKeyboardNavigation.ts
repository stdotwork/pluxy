import type { KeyboardEvent } from 'react';

const horizontalKeys = new Set(['ArrowLeft', 'ArrowRight', 'Home', 'End']);

export function handleHorizontalTabKey<T extends string>(
  event: KeyboardEvent<HTMLButtonElement>,
  tabs: readonly T[],
  current: T,
  activate: (tab: T) => void,
  key = event.key,
) {
  if (!horizontalKeys.has(key) || tabs.length === 0) return;
  const index = Math.max(0, tabs.indexOf(current));
  const nextIndex = key === 'Home'
    ? 0
    : key === 'End'
      ? tabs.length - 1
      : key === 'ArrowRight'
        ? (index + 1) % tabs.length
        : (index - 1 + tabs.length) % tabs.length;
  const next = tabs[nextIndex];
  event.preventDefault();
  activate(next);
}
