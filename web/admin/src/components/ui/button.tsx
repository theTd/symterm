import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '../../lib/utils';

const buttonVariants = cva(
  'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-full border text-sm font-medium transition-[transform,border-color,background-color,color,box-shadow] outline-none disabled:pointer-events-none disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:size-4 shrink-0 focus-visible:ring-2 focus-visible:ring-[color:var(--ring)]/65 focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--surface-canvas)] active:translate-y-px',
  {
    variants: {
      variant: {
        default:
          'border-transparent bg-[var(--accent)] text-[var(--accent-foreground)] shadow-[0_10px_30px_rgba(65,184,131,0.22)] hover:bg-[var(--accent-strong)]',
        secondary:
          'border-[color:var(--border-subtle)] bg-[var(--surface-muted)] text-[var(--foreground)] hover:border-[color:var(--border-strong)] hover:bg-[var(--surface-card)]',
        ghost:
          'border-transparent bg-transparent text-[var(--muted-foreground)] hover:bg-[var(--surface-muted)] hover:text-[var(--foreground)]',
        danger:
          'border-transparent bg-[var(--danger)]/14 text-[var(--danger-foreground)] hover:bg-[var(--danger)]/22',
        outline:
          'border-[color:var(--border-strong)] bg-[var(--surface-panel)] text-[var(--foreground)] hover:bg-[var(--surface-muted)]',
      },
      size: {
        default: 'h-11 px-5',
        sm: 'h-9 px-4 text-xs',
        lg: 'h-12 px-6',
        icon: 'size-10 rounded-full',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
);

export type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  };

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : 'button';
    return <Comp ref={ref} className={cn(buttonVariants({ variant, size, className }))} {...props} />;
  },
);
Button.displayName = 'Button';

export { Button };
