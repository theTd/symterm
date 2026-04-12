import type { HTMLAttributes } from 'react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '../../lib/utils';

const badgeVariants = cva(
  'inline-flex items-center rounded-full border px-3 py-1 text-[0.68rem] font-semibold uppercase tracking-[0.22em]',
  {
    variants: {
      variant: {
        neutral: 'border-[color:var(--border-subtle)] bg-[var(--surface-muted)] text-[var(--muted-foreground)]',
        accent: 'border-transparent bg-[var(--accent)]/14 text-[var(--accent)]',
        success: 'border-transparent bg-[var(--success)]/16 text-[var(--success)]',
        warning: 'border-transparent bg-[var(--warning)]/16 text-[var(--warning)]',
        danger: 'border-transparent bg-[var(--danger)]/16 text-[var(--danger-foreground)]',
        info: 'border-transparent bg-[var(--info)]/16 text-[var(--info)]',
      },
    },
    defaultVariants: {
      variant: 'neutral',
    },
  },
);

type BadgeProps = HTMLAttributes<HTMLSpanElement> & VariantProps<typeof badgeVariants>;

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant, className }))} {...props} />;
}
