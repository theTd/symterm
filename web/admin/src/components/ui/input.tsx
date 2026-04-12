import * as React from 'react';
import { cn } from '../../lib/utils';

const inputClassName =
  'flex h-11 w-full rounded-2xl border border-[color:var(--border-strong)] bg-[var(--surface-field)] px-4 py-2 text-sm text-[var(--foreground)] shadow-[inset_0_1px_0_rgba(255,255,255,0.03)] transition-[border-color,background-color,box-shadow] outline-none placeholder:text-[var(--muted-foreground)]/80 hover:border-[color:var(--border-contrast)] focus-visible:border-[color:var(--ring)] focus-visible:ring-3 focus-visible:ring-[color:var(--ring)]/20 disabled:cursor-not-allowed disabled:opacity-50';

export const Input = React.forwardRef<HTMLInputElement, React.ComponentProps<'input'>>(({ className, ...props }, ref) => (
  <input ref={ref} className={cn(inputClassName, className)} {...props} />
));
Input.displayName = 'Input';
