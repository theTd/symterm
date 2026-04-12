import { Activity, History, ShieldAlert, UserRoundX } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { EmptyState, Notice, TableSkeleton } from '../components/StateView';
import { ActionCluster, MetricTile, PageIntro, SectionCard, StackItem } from '../components/AdminPrimitives';
import { Badge } from '../components/ui/badge';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableHeaderCell, TableRow } from '../components/ui/table';
import { eventTone, auditResultTone } from '../lib/presentation';
import { translateAuditAction, translateAuditResult, useI18n } from '../i18n';
import { adminAPI } from '../lib/api';
import { stableSort } from '../lib/sort';

export function OverviewPage() {
  const { messages, formatDateTime } = useI18n();
  const overview = useQuery({
    queryKey: ['overview'],
    queryFn: adminAPI.overview,
  });

  if (overview.isLoading) {
    return (
      <div className="space-y-5">
        <PageIntro title={messages.frame.nav.overview} description={messages.frame.description} />
        <div className="grid gap-5 xl:grid-cols-4">
          {Array.from({ length: 4 }, (_, index) => (
            <MetricTile key={index} label="..." value="--" className="animate-pulse" />
          ))}
        </div>
        <div className="grid gap-5 xl:grid-cols-[1.1fr_0.9fr]">
          <SectionCard title={messages.overview.recentEvents}>
            <TableSkeleton columns={2} rows={4} label={messages.overview.loadingEvents} />
          </SectionCard>
          <SectionCard title={messages.overview.recentAudit}>
            <TableSkeleton columns={3} rows={4} label={messages.overview.loadingEvents} />
          </SectionCard>
        </div>
      </div>
    );
  }
  if (overview.isError || !overview.data) {
    return <Notice tone="error" title={messages.app.bootstrapFailed}>{String(overview.error)}</Notice>;
  }

  const data = overview.data;
  const recentEvents = stableSort(data.recent_events ?? [], (left, right) => right.cursor - left.cursor);
  const recentAudit = stableSort(
    data.recent_audit ?? [],
    (left, right) => new Date(right.timestamp).getTime() - new Date(left.timestamp).getTime(),
  );

  return (
    <div className="space-y-5">
      <PageIntro
        eyebrow={messages.frame.nav.overview}
        title={messages.frame.title}
        description={messages.frame.description}
        actions={
          <ActionCluster>
            <Badge variant="neutral">{data.daemon.version || messages.system.dev}</Badge>
            <Badge variant="info">{formatDateTime(data.daemon.started_at)}</Badge>
          </ActionCluster>
        }
      />
      <section className="grid gap-5 md:grid-cols-2 xl:grid-cols-4">
        <MetricTile label={messages.overview.activeSessions} value={data.active_session_count} accent={<Activity className="size-5 text-[var(--accent)]" />} />
        <MetricTile label={messages.overview.closedSessions} value={data.closed_session_count} accent={<History className="size-5 text-[var(--info)]" />} />
        <MetricTile
          label={messages.overview.needsConfirmation}
          value={data.needs_confirmation_count}
          accent={<ShieldAlert className="size-5 text-[var(--warning)]" />}
        />
        <MetricTile label={messages.overview.disabledUsers} value={data.disabled_user_count} accent={<UserRoundX className="size-5 text-[var(--danger)]" />} />
      </section>
      <div className="grid gap-5 xl:grid-cols-[1.04fr_0.96fr]">
        <SectionCard title={messages.overview.recentEvents} description={messages.overview.noRecentEventsBody}>
          <div className="space-y-3">
            {recentEvents.length === 0 ? (
              <EmptyState title={messages.overview.noRecentEventsTitle}>{messages.overview.noRecentEventsBody}</EmptyState>
            ) : (
              recentEvents.map((event) => (
                <StackItem
                  key={event.cursor}
                  title={
                    <>
                      <Badge variant={eventTone(event.kind)}>{event.kind}</Badge>
                    </>
                  }
                  description={event.audit?.target || event.session?.project_id || event.user?.username || data.daemon.listen_addr || '-'}
                  meta={<div className="text-xs uppercase tracking-[0.18em] text-[var(--subtle-foreground)]">{messages.overview.cursorLabel(event.cursor)}</div>}
                />
              ))
            )}
          </div>
        </SectionCard>
        <SectionCard title={messages.overview.recentAudit} description={messages.overview.noAuditBody}>
          {recentAudit.length === 0 ? (
            <EmptyState title={messages.overview.noAuditTitle}>{messages.overview.noAuditBody}</EmptyState>
          ) : (
            <TableContainer>
              <Table>
                <TableHead>
                  <tr>
                    <TableHeaderCell>{messages.overview.headers.time}</TableHeaderCell>
                    <TableHeaderCell>{messages.overview.headers.action}</TableHeaderCell>
                    <TableHeaderCell>{messages.overview.headers.actor}</TableHeaderCell>
                    <TableHeaderCell>{messages.overview.headers.target}</TableHeaderCell>
                    <TableHeaderCell>{messages.overview.headers.result}</TableHeaderCell>
                  </tr>
                </TableHead>
                <TableBody>
                  {recentAudit.map((item) => (
                    <TableRow key={`${item.timestamp}-${item.action}-${item.target}`}>
                      <TableCell className="whitespace-nowrap text-[var(--muted-foreground)]">{formatDateTime(item.timestamp)}</TableCell>
                      <TableCell>{translateAuditAction(messages, item.action)}</TableCell>
                      <TableCell>{item.actor}</TableCell>
                      <TableCell className="max-w-[16rem] truncate">{item.target}</TableCell>
                      <TableCell>
                        <Badge variant={auditResultTone(item.result)}>{translateAuditResult(messages, item.result)}</Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </SectionCard>
      </div>
    </div>
  );
}
