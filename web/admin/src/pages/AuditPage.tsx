import { useQuery } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { EmptyState, Notice, TableSkeleton } from '../components/StateView';
import { ActionCluster, FilterGrid, PageIntro, SectionCard } from '../components/AdminPrimitives';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableHeaderCell, TableRow } from '../components/ui/table';
import { auditResultTone } from '../lib/presentation';
import { translateAuditAction, translateAuditResult, useI18n } from '../i18n';
import { adminAPI } from '../lib/api';
import { stableSort } from '../lib/sort';

export function AuditPage() {
  const { messages, formatDateTime } = useI18n();
  const [params, setParams] = useSearchParams();
  const query = new URLSearchParams(params);
  if (!query.get('page_size')) {
    query.set('page_size', '20');
  }
  if (!query.get('page')) {
    query.set('page', '1');
  }

  const audit = useQuery({
    queryKey: ['audit', query.toString()],
    queryFn: () => adminAPI.audit(query),
  });

  const update = (key: string, value: string, options?: { keepPage?: boolean }) => {
    const next = new URLSearchParams(params);
    if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    if (!options?.keepPage && key !== 'page') {
      next.set('page', '1');
    }
    setParams(next, { replace: true });
  };

  const page = Number(query.get('page') || '1');
  const total = Number(audit.data?.meta?.total || 0);
  const pageSize = Number(audit.data?.meta?.page_size || query.get('page_size') || 20);
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const hasFilters = ['actor', 'action', 'target', 'result'].some((key) => Boolean(params.get(key)));
  const items = stableSort(
    audit.data?.data ?? [],
    (left, right) => new Date(right.timestamp).getTime() - new Date(left.timestamp).getTime(),
  );

  return (
    <div className="space-y-5">
      <PageIntro eyebrow={messages.frame.nav.audit} title={messages.audit.title} description={messages.audit.description} />
      <SectionCard
        title={messages.audit.title}
        description={messages.audit.description}
        actions={
          <ActionCluster>
            <Badge variant="neutral">{total}</Badge>
            <Badge variant={hasFilters ? 'accent' : 'neutral'}>{messages.common.pageOf(page, totalPages)}</Badge>
          </ActionCluster>
        }
      >
        <div className="space-y-5">
          <FilterGrid>
            <Input
              placeholder={messages.audit.placeholders.actor}
              value={params.get('actor') || ''}
              onChange={(event) => update('actor', event.target.value)}
            />
            <Input
              placeholder={messages.audit.placeholders.action}
              value={params.get('action') || ''}
              onChange={(event) => update('action', event.target.value)}
            />
            <Input
              placeholder={messages.audit.placeholders.target}
              value={params.get('target') || ''}
              onChange={(event) => update('target', event.target.value)}
            />
            <Input
              placeholder={messages.audit.placeholders.result}
              value={params.get('result') || ''}
              onChange={(event) => update('result', event.target.value)}
            />
          </FilterGrid>
          {audit.isLoading ? <TableSkeleton columns={5} rows={8} label={messages.audit.loadingLabel} /> : null}
          {audit.isError ? (
            <Notice tone="error" title={messages.audit.unableToLoadTitle}>
              {String(audit.error)}
            </Notice>
          ) : null}
          {audit.data ? (
            items.length === 0 ? (
              <EmptyState title={hasFilters ? messages.audit.emptyFilteredTitle : messages.audit.emptyDefaultTitle}>
                {messages.audit.emptyBody}
              </EmptyState>
            ) : (
              <>
                <TableContainer>
                  <Table>
                    <TableHead>
                      <tr>
                        <TableHeaderCell>{messages.audit.headers.time}</TableHeaderCell>
                        <TableHeaderCell>{messages.audit.headers.action}</TableHeaderCell>
                        <TableHeaderCell>{messages.audit.headers.actor}</TableHeaderCell>
                        <TableHeaderCell>{messages.audit.headers.target}</TableHeaderCell>
                        <TableHeaderCell>{messages.audit.headers.result}</TableHeaderCell>
                      </tr>
                    </TableHead>
                    <TableBody>
                      {items.map((item) => (
                        <TableRow key={`${item.timestamp}-${item.action}-${item.target}`}>
                          <TableCell className="whitespace-nowrap text-[var(--muted-foreground)]">{formatDateTime(item.timestamp)}</TableCell>
                          <TableCell>{translateAuditAction(messages, item.action)}</TableCell>
                          <TableCell>{item.actor}</TableCell>
                          <TableCell className="max-w-[20rem] truncate">{item.target}</TableCell>
                          <TableCell>
                            <Badge variant={auditResultTone(item.result)}>{translateAuditResult(messages, item.result)}</Badge>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </TableContainer>
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <div className="text-sm text-[var(--muted-foreground)]">{messages.common.pageOf(page, totalPages)}</div>
                  <ActionCluster>
                    <Button
                      variant="secondary"
                      onClick={() => update('page', String(Math.max(1, page - 1)), { keepPage: true })}
                      disabled={page <= 1}
                    >
                      {messages.common.previous}
                    </Button>
                    <Button
                      variant="secondary"
                      onClick={() => update('page', String(Math.min(totalPages, page + 1)), { keepPage: true })}
                      disabled={page >= totalPages}
                    >
                      {messages.common.next}
                    </Button>
                  </ActionCluster>
                </div>
              </>
            )
          ) : null}
        </div>
      </SectionCard>
    </div>
  );
}
