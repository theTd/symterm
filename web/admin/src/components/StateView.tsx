import type { ReactNode } from 'react';
import { AlertTriangle, CheckCircle2, CircleSlash, Info } from 'lucide-react';
import { cn } from '../lib/utils';
import { Card, CardContent } from './ui/card';
import { Skeleton } from './ui/skeleton';

type NoticeTone = 'info' | 'success' | 'warning' | 'error';

const toneMap: Record<
  NoticeTone,
  {
    icon: typeof Info;
    frame: string;
    iconFrame: string;
  }
> = {
  info: {
    icon: Info,
    frame: 'border-[color:var(--info)]/28 bg-[var(--info)]/10',
    iconFrame: 'bg-[var(--info)]/16 text-[var(--info)]',
  },
  success: {
    icon: CheckCircle2,
    frame: 'border-[color:var(--success)]/28 bg-[var(--success)]/10',
    iconFrame: 'bg-[var(--success)]/16 text-[var(--success)]',
  },
  warning: {
    icon: AlertTriangle,
    frame: 'border-[color:var(--warning)]/28 bg-[var(--warning)]/10',
    iconFrame: 'bg-[var(--warning)]/16 text-[var(--warning)]',
  },
  error: {
    icon: AlertTriangle,
    frame: 'border-[color:var(--danger)]/30 bg-[var(--danger)]/10',
    iconFrame: 'bg-[var(--danger)]/16 text-[var(--danger-foreground)]',
  },
};

export function Notice(props: {
  tone: NoticeTone;
  title: string;
  children?: ReactNode;
  compact?: boolean;
}) {
  const tone = toneMap[props.tone];
  const Icon = tone.icon;

  return (
    <Card className={cn('border', tone.frame)}>
      <CardContent className={cn('flex gap-3 p-4', props.compact && 'items-center py-3')}>
        <div className={cn('flex size-10 shrink-0 items-center justify-center rounded-2xl', tone.iconFrame)}>
          <Icon className="size-4" />
        </div>
        <div className="space-y-1.5">
          <div className="text-sm font-semibold text-[var(--foreground)]">{props.title}</div>
          {props.children ? <p className="text-sm leading-6 text-[var(--muted-foreground)]">{props.children}</p> : null}
        </div>
      </CardContent>
    </Card>
  );
}

export function EmptyState(props: { title: string; children?: ReactNode; compact?: boolean }) {
  return (
    <Card className="border-dashed border-[color:var(--border-strong)] bg-[var(--surface-card)]/65">
      <CardContent className={cn('flex flex-col items-start gap-3 p-6', props.compact && 'p-5')}>
        <div className="flex size-11 items-center justify-center rounded-2xl bg-[var(--surface-muted)] text-[var(--subtle-foreground)]">
          <CircleSlash className="size-5" />
        </div>
        <div className="space-y-1">
          <div className="text-sm font-semibold text-[var(--foreground)]">{props.title}</div>
          {props.children ? <p className="text-sm leading-6 text-[var(--muted-foreground)]">{props.children}</p> : null}
        </div>
      </CardContent>
    </Card>
  );
}

export function TableSkeleton(props: { rows?: number; columns?: number; label?: string }) {
  const rows = props.rows ?? 5;
  const columns = props.columns ?? 4;

  return (
    <div className="space-y-3" aria-label={props.label ?? 'Loading table'}>
      {Array.from({ length: rows }, (_, rowIndex) => (
        <div
          key={rowIndex}
          className="grid gap-3 rounded-[24px] border border-[color:var(--border-subtle)] bg-[var(--surface-card)]/75 p-4"
          style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
        >
          {Array.from({ length: columns }, (_, columnIndex) => (
            <Skeleton key={columnIndex} className="h-4 w-full" />
          ))}
        </div>
      ))}
    </div>
  );
}
