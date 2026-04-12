import * as React from 'react';
import { cn } from '../../lib/utils';

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.ComponentProps<'textarea'>>(
  ({ className, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn(
        'flex min-h-28 w-full rounded-[24px] border border-[color:var(--border-strong)] bg-[var(--surface-field)] px-4 py-3 text-sm text-[var(--foreground)] shadow-[inset_0_1px_0_rgba(255,255,255,0.03)] transition-[border-color,background-color,box-shadow] outline-none placeholder:text-[var(--muted-foreground)]/80 hover:border-[color:var(--border-contrast)] focus-visible:border-[color:var(--ring)] focus-visible:ring-3 focus-visible:ring-[color:var(--ring)]/20 disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  ),
);
Textarea.displayName = 'Textarea';
