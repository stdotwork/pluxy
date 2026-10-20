import type { ComponentPropsWithRef } from 'react';

export type SelectProps = ComponentPropsWithRef<'select'>;

export function Select({ className, ...props }: SelectProps) {
  return <select {...props} className={['select-control', className].filter(Boolean).join(' ')} />;
}
