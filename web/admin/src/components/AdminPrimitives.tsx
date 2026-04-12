import type { HTMLAttributes, ReactNode } from 'react';
import { ArrowUpRight, Dot } from 'lucide-react';
import { cn } from '../lib/utils';
import { Badge } from './ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './ui/card';

export function PageIntro(props: {
  eyebrow?: string;
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <Card className="surface-ring overflow-hidden border-[color:var(--border-strong)] bg-[linear-gradient(135deg,rgba(9,24,36,0.92),rgba(5,15,23,0.88))]">
      <CardContent className="flex flex-col gap-5 p-6 lg:flex-row lg:items-end lg:justify-between lg:p-8">
        <div className="space-y-4">
          {props.eyebrow ? (
            <Badge variant="accent" className="rounded-full px-3 py-1.5">
              {props.eyebrow}
            </Badge>
          ) : null}
          <div className="space-y-3">
            <h1 className="max-w-[12ch] text-[clamp(2rem,4vw,3.4rem)] font-semibold leading-[0.92] tracking-[-0.05em] text-balance">
              {props.title}
            </h1>
            <p className="max-w-[66ch] text-sm leading-7 text-[var(--muted-foreground)]">{props.description}</p>
          </div>
        </div>
        {props.actions ? <div className="flex flex-wrap items-center gap-3">{props.actions}</div> : null}
      </CardContent>
    </Card>
  );
}

export function SectionCard(props: {
  title: string;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  contentClassName?: string;
}) {
  return (
    <Card className={cn('border-[color:var(--border-subtle)] bg-[var(--surface-panel)]', props.className)}>
      <CardHeader className="flex flex-col gap-4 pb-0 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1.5">
          <CardTitle>{props.title}</CardTitle>
          {props.description ? <CardDescription>{props.description}</CardDescription> : null}
        </div>
        {props.actions ? <div className="flex shrink-0 items-center gap-3">{props.actions}</div> : null}
      </CardHeader>
      <CardContent className={cn('pt-6', props.contentClassName)}>{props.children}</CardContent>
    </Card>
  );
}

export function MetricTile(props: {
  label: string;
  value: ReactNode;
  detail?: ReactNode;
  accent?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn('border-[color:var(--border-subtle)] bg-[linear-gradient(180deg,rgba(13,32,46,0.86),rgba(9,24,36,0.88))]', props.className)}>
      <CardContent className="space-y-5 p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-2">
            <div className="text-[0.72rem] font-semibold uppercase tracking-[0.24em] text-[var(--subtle-foreground)]">{props.label}</div>
            <div className="text-4xl font-semibold tracking-[-0.06em] text-[var(--foreground)]">{props.value}</div>
          </div>
          {props.accent ? <div>{props.accent}</div> : null}
        </div>
        {props.detail ? <div className="text-sm text-[var(--muted-foreground)]">{props.detail}</div> : null}
      </CardContent>
    </Card>
  );
}

export function FilterGrid({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('grid gap-3 md:grid-cols-2 xl:grid-cols-4', className)} {...props} />;
}

export function ActionCluster({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('flex flex-wrap items-center gap-3', className)} {...props} />;
}

export function DefinitionList(props: { items: Array<{ label: string; value: ReactNode }>; columns?: 1 | 2; className?: string }) {
  return (
    <dl className={cn('grid gap-4', props.columns === 1 ? 'grid-cols-1' : 'sm:grid-cols-2', props.className)}>
      {props.items.map((item) => (
        <div key={item.label} className="rounded-[22px] border border-[color:var(--border-subtle)] bg-[var(--surface-card)]/70 p-4">
          <dt className="text-[0.72rem] font-semibold uppercase tracking-[0.2em] text-[var(--subtle-foreground)]">{item.label}</dt>
          <dd className="mt-2 break-all text-sm leading-6 text-[var(--foreground)]">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

export function StackItem(props: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        'flex flex-col gap-4 rounded-[24px] border border-[color:var(--border-subtle)] bg-[var(--surface-card)]/75 p-4 sm:flex-row sm:items-start sm:justify-between',
        props.className,
      )}
    >
      <div className="min-w-0 space-y-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm font-semibold text-[var(--foreground)]">{props.title}</div>
        {props.description ? <div className="text-sm leading-6 text-[var(--muted-foreground)]">{props.description}</div> : null}
      </div>
      <div className="flex shrink-0 items-center gap-3">{props.meta}{props.action}</div>
    </div>
  );
}

export function InlineMeta(props: { children: ReactNode }) {
  return <span className="inline-flex items-center gap-1 text-xs uppercase tracking-[0.18em] text-[var(--subtle-foreground)]">{props.children}</span>;
}

export function SignalLink(props: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 text-xs font-medium uppercase tracking-[0.18em] text-[var(--subtle-foreground)]">
      {props.children}
      <ArrowUpRight className="size-3.5" />
    </span>
  );
}

export function DotDivider() {
  return <Dot className="size-4 text-[var(--subtle-foreground)]" />;
}
