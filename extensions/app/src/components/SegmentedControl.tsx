import type { HTMLAttributes, ReactNode } from 'react';
import { useChoiceNavigation } from './useChoiceNavigation';

type Segment<T extends string> = {
  value: T;
  label: ReactNode;
  disabled?: boolean;
  title?: string;
  'aria-label'?: string;
};

type SegmentedControlProps<T extends string> = Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> & {
  items: readonly Segment<T>[];
  value: T;
  onChange: (value: T) => void;
  disabled?: boolean;
  buttonClassName?: string;
  selectedClassName?: string;
};

export function SegmentedControl<T extends string>({ items, value, onChange, disabled, buttonClassName = '', selectedClassName = 'active', ...props }: SegmentedControlProps<T>) {
  const available = items.filter(item => !disabled && !item.disabled).map(item => item.value);
  const stop = available.includes(value) ? value : available[0];
  const navigation = useChoiceNavigation(value, available, onChange, true);
  return <div {...props} role="radiogroup">
    {items.map(item => <button key={item.value} type="button" role="radio" aria-checked={value === item.value}
      aria-label={item['aria-label']} title={item.title} data-value={item.value}
      tabIndex={stop === item.value ? 0 : -1} className={[buttonClassName, value === item.value ? selectedClassName : ''].filter(Boolean).join(' ')}
      disabled={disabled || item.disabled} ref={element => { if (element) navigation.buttons.current.set(item.value, element); else navigation.buttons.current.delete(item.value); }}
      onClick={() => navigation.choose(item.value)} onKeyDown={event => navigation.onKeyDown(event, item.value)}>{item.label}</button>)}
  </div>;
}
