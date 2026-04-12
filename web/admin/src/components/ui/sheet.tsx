import * as React from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { X } from 'lucide-react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '../../lib/utils';

export const Sheet = Dialog.Root;
export const SheetTrigger = Dialog.Trigger;
export const SheetClose = Dialog.Close;

export function SheetPortal(props: Dialog.DialogPortalProps) {
  return <Dialog.Portal {...props} />;
}

export const SheetOverlay = React.forwardRef<
  React.ElementRef<typeof Dialog.Overlay>,
  React.ComponentPropsWithoutRef<typeof Dialog.Overlay>
>(({ className, ...props }, ref) => (
  <Dialog.Overlay
    ref={ref}
    className={cn('fixed inset-0 z-50 bg-[rgba(3,9,15,0.72)] backdrop-blur-sm', className)}
    {...props}
  />
));
SheetOverlay.displayName = Dialog.Overlay.displayName;

const sheetVariants = cva(
  'fixed z-50 flex flex-col gap-4 bg-[var(--surface-panel)] p-6 shadow-[0_24px_80px_rgba(0,0,0,0.42)] transition ease-out data-[state=closed]:duration-200 data-[state=open]:duration-300',
  {
    variants: {
      side: {
        top: 'inset-x-4 top-4 rounded-[28px] border border-[color:var(--border-strong)] data-[state=closed]:-translate-y-4 data-[state=open]:translate-y-0',
        bottom:
          'inset-x-4 bottom-4 rounded-[28px] border border-[color:var(--border-strong)] data-[state=closed]:translate-y-4 data-[state=open]:translate-y-0',
        left: 'inset-y-4 left-4 w-[min(88vw,21rem)] rounded-[32px] border border-[color:var(--border-strong)] data-[state=closed]:-translate-x-4 data-[state=open]:translate-x-0',
        right:
          'inset-y-4 right-4 w-[min(88vw,21rem)] rounded-[32px] border border-[color:var(--border-strong)] data-[state=closed]:translate-x-4 data-[state=open]:translate-x-0',
      },
    },
    defaultVariants: {
      side: 'right',
    },
  },
);

export const SheetContent = React.forwardRef<
  React.ElementRef<typeof Dialog.Content>,
  React.ComponentPropsWithoutRef<typeof Dialog.Content> & VariantProps<typeof sheetVariants>
>(({ side = 'right', className, children, ...props }, ref) => (
  <SheetPortal>
    <SheetOverlay />
    <Dialog.Content ref={ref} className={cn(sheetVariants({ side }), className)} {...props}>
      {children}
      <Dialog.Close className="absolute right-4 top-4 inline-flex size-9 items-center justify-center rounded-full border border-[color:var(--border-subtle)] text-[var(--muted-foreground)] transition-colors hover:bg-[var(--surface-muted)] hover:text-[var(--foreground)]">
        <X className="size-4" />
        <span className="sr-only">Close</span>
      </Dialog.Close>
    </Dialog.Content>
  </SheetPortal>
));
SheetContent.displayName = Dialog.Content.displayName;

export function SheetHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('flex flex-col gap-1.5 text-left', className)} {...props} />;
}

export function SheetTitle({ className, ...props }: React.ComponentPropsWithoutRef<typeof Dialog.Title>) {
  return <Dialog.Title className={cn('text-lg font-semibold text-[var(--foreground)]', className)} {...props} />;
}

export function SheetDescription({ className, ...props }: React.ComponentPropsWithoutRef<typeof Dialog.Description>) {
  return <Dialog.Description className={cn('text-sm text-[var(--muted-foreground)]', className)} {...props} />;
}
