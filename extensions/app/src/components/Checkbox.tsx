import { useEffect, useId, useRef, type ChangeEventHandler, type InputHTMLAttributes, type ReactNode } from 'react';

export type CheckboxProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'role' | 'defaultChecked' | 'checked' | 'onChange'> & {
  checked: boolean;
  onChange: ChangeEventHandler<HTMLInputElement>;
  label?: ReactNode;
  indeterminate?: boolean;
  invalid?: boolean;
};

export function Checkbox({ checked, onChange, label, indeterminate = false, invalid, id, ...props }: CheckboxProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate;
  });
  const control = <input {...props} ref={ref} id={inputId} type="checkbox" checked={checked}
    aria-invalid={invalid || props['aria-invalid']} onChange={event => {
      onChange(event);
      event.currentTarget.indeterminate = indeterminate;
    }} />;
  return label == null ? control : <label className="binary-control-label" htmlFor={inputId}>{control}<span>{label}</span></label>;
}
