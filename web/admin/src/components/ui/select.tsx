import * as React from 'react';
import { ChevronDown } from 'lucide-react';
import { cn } from '../../lib/utils';

export const Select = React.forwardRef<HTMLSelectElement, React.ComponentProps<'select'>>(({ className, children, ...props }, ref) => (
  <div className="relative">
    <select
      ref={ref}
      className={cn(
        'flex h-11 w-full appearance-none rounded-2xl border border-[color:var(--border-strong)] bg-[var(--surface-field)] px-4 py-2 pr-11 text-sm text-[var(--foreground)] shadow-[inset_0_1px_0_rgba(255,255,255,0.03)] transition-[border-color,background-color,box-shadow] outline-none hover:border-[color:var(--border-contrast)] focus-visible:border-[color:var(--ring)] focus-visible:ring-3 focus-visible:ring-[color:var(--ring)]/20 disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    >
      {children}
    </select>
    <ChevronDown className="pointer-events-none absolute right-4 top-1/2 size-4 -translate-y-1/2 text-[var(--subtle-foreground)]" />
  </div>
));
Select.displayName = 'Select';
