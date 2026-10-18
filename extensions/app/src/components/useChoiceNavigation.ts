import { useLayoutEffect, useRef, type KeyboardEvent } from 'react';
import { handleHorizontalTabKey } from './tabKeyboardNavigation';

export function useChoiceNavigation<T extends string>(value: T, values: readonly T[], onChange: (value: T) => void, verticalKeys = false, activation: 'automatic' | 'manual' = 'automatic') {
  const buttons = useRef(new Map<T, HTMLButtonElement>());
  const pending = useRef<{ next: T; previous: T; disabled: boolean } | null>(null);
  useLayoutEffect(() => {
    const clearOutsideRequest = (event: PointerEvent) => {
      if (!pending.current || !(event.target instanceof Element)) return;
      const group = buttons.current.values().next().value?.parentElement;
      if (!group?.contains(event.target) && !event.target.closest('[aria-modal="true"]')) pending.current = null;
    };
    document.addEventListener('pointerdown', clearOutsideRequest, true);
    return () => document.removeEventListener('pointerdown', clearOutsideRequest, true);
  }, []);
  useLayoutEffect(() => {
    const request = pending.current;
    if (!request) return;
    const selected = buttons.current.get(value);
    if (selected?.disabled) {
      request.disabled = true;
      return;
    }
    if (request.next === value || (request.disabled && request.previous === value)) {
      const frame = window.requestAnimationFrame(() => {
        if (pending.current !== request) return;
        const active = document.activeElement;
        if ((!active || active === document.body || selected?.parentElement?.contains(active))
          && !active?.closest('[aria-modal="true"]')) selected?.focus();
        pending.current = null;
      });
      return () => window.cancelAnimationFrame(frame);
    } else if (request.previous !== value) pending.current = null;
  });
  const choose = (next: T) => {
    pending.current = null;
    onChange(next);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, current: T) => {
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const rtl = getComputedStyle(event.currentTarget).direction === 'rtl';
    const key = verticalKeys && event.key === 'ArrowDown' ? 'ArrowRight'
      : verticalKeys && event.key === 'ArrowUp' ? 'ArrowLeft'
      : rtl && event.key === 'ArrowRight' ? 'ArrowLeft'
      : rtl && event.key === 'ArrowLeft' ? 'ArrowRight' : event.key;
    handleHorizontalTabKey(event, values, current, next => {
      if (activation === 'manual') {
        buttons.current.get(next)?.focus();
        return;
      }
      pending.current = { next, previous: value, disabled: false };
      if (next === value) buttons.current.get(next)?.focus();
      onChange(next);
    }, key);
  };
  return { buttons, choose, onKeyDown };
}
