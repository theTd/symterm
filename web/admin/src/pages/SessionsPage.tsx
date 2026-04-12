import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { EmptyState, Notice, TableSkeleton } from '../components/StateView';
import { ActionCluster, DefinitionList, FilterGrid, PageIntro, SectionCard, StackItem } from '../components/AdminPrimitives';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableHeaderCell, TableRow } from '../components/ui/table';
import { projectStateTone, auditResultTone } from '../lib/presentation';
import { translateAuditAction, translateAuditResult, translateProjectState, translateRole, useI18n } from '../i18n';
import { adminAPI } from '../lib/api';
import { stableSort } from '../lib/sort';

const FILTER_KEYS = ['search', 'username', 'role', 'project_state', 'include_closed'] as const;

export function SessionsPage() {
  const { messages, formatDateTime } = useI18n();
  const [params, setParams] = useSearchParams();
  const queryClient = useQueryClient();
  const sessionID = params.get('session');
  const hasFilters = FILTER_KEYS.some((key) => Boolean(params.get(key)));
  const listParams = new URLSearchParams();
  FILTER_KEYS.forEach((key) => {
    const value = params.get(key);
    if (value) {
      listParams.set(key, value);
    }
  });

  const sessions = useQuery({
    queryKey: ['sessions', listParams.toString()],
    queryFn: () => adminAPI.sessions(listParams),
  });
  const detail = useQuery({
    queryKey: ['session', sessionID],
    queryFn: () => adminAPI.session(sessionID!),
    enabled: Boolean(sessionID),
  });

  const terminate = useMutation({
    mutationFn: (id: string) => adminAPI.terminateSession(id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['sessions'] });
      await queryClient.invalidateQueries({ queryKey: ['session'] });
    },
  });

  const sortedItems = stableSort(sessions.data?.items ?? [], (left, right) => {
    const byActivity = new Date(right.last_activity_at).getTime() - new Date(left.last_activity_at).getTime();
    if (byActivity !== 0) {
      return byActivity;
    }
    return left.session_id.localeCompare(right.session_id);
  });

  const updateFilter = (key: string, value: string, options?: { keepPage?: boolean }) => {
    const next = new URLSearchParams(params);
    if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    if (!options?.keepPage && key !== 'session') {
      next.delete('page');
    }
    setParams(next, { replace: true });
  };

  const detailRelatedAudit = detail.data?.related_audit ?? [];

  return (
    <div className="space-y-5">
      <PageIntro eyebrow={messages.frame.nav.sessions} title={messages.sessions.title} description={messages.sessions.description} />
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.22fr)_minmax(360px,0.78fr)]">
        <SectionCard
          title={messages.sessions.title}
          description={messages.sessions.description}
          actions={
            <ActionCluster>
              <Badge variant="neutral">{sortedItems.length}</Badge>
            </ActionCluster>
          }
        >
          <div className="space-y-5">
            <FilterGrid>
              <Input
                placeholder={messages.sessions.placeholders.search}
                value={params.get('search') || ''}
                onChange={(event) => updateFilter('search', event.target.value)}
              />
              <Input
                placeholder={messages.sessions.placeholders.username}
                value={params.get('username') || ''}
                onChange={(event) => updateFilter('username', event.target.value)}
              />
              <Select value={params.get('role') || ''} onChange={(event) => updateFilter('role', event.target.value)}>
                <option value="">{messages.sessions.filters.allRoles}</option>
                <option value="owner">{messages.sessions.roles.owner}</option>
                <option value="follower">{messages.sessions.roles.follower}</option>
              </Select>
              <Select value={params.get('project_state') || ''} onChange={(event) => updateFilter('project_state', event.target.value)}>
                <option value="">{messages.sessions.filters.allStates}</option>
                <option value="initializing">{messages.sessions.states.initializing}</option>
                <option value="syncing">{messages.sessions.states.syncing}</option>
                <option value="active">{messages.sessions.states.active}</option>
                <option value="needs-confirmation">{messages.sessions.states.needsConfirmation}</option>
                <option value="terminating">{messages.sessions.states.terminating}</option>
                <option value="terminated">{messages.sessions.states.terminated}</option>
              </Select>
            </FilterGrid>
            <label className="inline-flex items-center gap-3 rounded-full border border-[color:var(--border-subtle)] bg-[var(--surface-card)] px-4 py-2 text-sm text-[var(--muted-foreground)]">
              <input
                type="checkbox"
                checked={params.get('include_closed') === 'true'}
                onChange={(event) => updateFilter('include_closed', event.target.checked ? 'true' : '')}
              />
              {messages.sessions.filters.includeClosed}
            </label>
            {terminate.isSuccess ? (
              <Notice tone="success" title={messages.sessions.terminateSuccessTitle}>
                {messages.sessions.terminateSuccessBody}
              </Notice>
            ) : null}
            {terminate.isError ? (
              <Notice tone="error" title={messages.sessions.terminateFailedTitle}>
                {String(terminate.error)}
              </Notice>
            ) : null}
            {sessions.isLoading ? <TableSkeleton columns={6} rows={6} label={messages.sessions.loadingLabel} /> : null}
            {sessions.isError ? (
              <Notice tone="error" title={messages.sessions.unableToLoadTitle}>
                {String(sessions.error)}
              </Notice>
            ) : null}
            {sessions.data ? (
              sortedItems.length === 0 ? (
                <EmptyState title={hasFilters ? messages.sessions.emptyFilteredTitle : messages.sessions.emptyDefaultTitle}>
                  {messages.sessions.emptyBody}
                </EmptyState>
              ) : (
                <TableContainer>
                  <Table>
                    <TableHead>
                      <tr>
                        <TableHeaderCell>{messages.sessions.headers.session}</TableHeaderCell>
                        <TableHeaderCell>{messages.sessions.headers.user}</TableHeaderCell>
                        <TableHeaderCell>{messages.sessions.headers.project}</TableHeaderCell>
                        <TableHeaderCell>{messages.sessions.headers.role}</TableHeaderCell>
                        <TableHeaderCell>{messages.sessions.headers.state}</TableHeaderCell>
                        <TableHeaderCell>{messages.sessions.headers.lastActivity}</TableHeaderCell>
                      </tr>
                    </TableHead>
                    <TableBody>
                      {sortedItems.map((item) => {
                        const stateLabel = item.close_reason
                          ? messages.sessions.closedWithReason(item.close_reason)
                          : translateProjectState(messages, item.project_state);

                        return (
                          <TableRow
                            key={item.session_id}
                            data-selected={sessionID === item.session_id}
                            className="cursor-pointer"
                            onClick={() => updateFilter('session', item.session_id, { keepPage: true })}
                          >
                            <TableCell className="font-medium">{item.session_id}</TableCell>
                            <TableCell>{item.principal.username}</TableCell>
                            <TableCell>{item.project_id}</TableCell>
                            <TableCell>
                              <Badge variant="info">{translateRole(messages, item.role)}</Badge>
                            </TableCell>
                            <TableCell>
                              <Badge variant={projectStateTone(item.project_state, item.close_reason)}>{stateLabel}</Badge>
                            </TableCell>
                            <TableCell className="whitespace-nowrap text-[var(--muted-foreground)]">
                              {formatDateTime(item.last_activity_at)}
                            </TableCell>
                          </TableRow>
                        );
                      })}
                    </TableBody>
                  </Table>
                </TableContainer>
              )
            ) : null}
          </div>
        </SectionCard>
        <SectionCard
          title={messages.sessions.detailTitle}
          description={messages.sessions.detailDescription}
          className="xl:sticky xl:top-28"
          actions={
            sessionID ? (
              <Button
                variant="danger"
                onClick={() => {
                  if (window.confirm(messages.sessions.terminateConfirm(sessionID))) {
                    terminate.mutate(sessionID);
                  }
                }}
                disabled={terminate.isPending}
              >
                {terminate.isPending ? messages.sessions.terminating : messages.sessions.terminate}
              </Button>
            ) : undefined
          }
        >
          <div className="space-y-5">
            {!sessionID ? <EmptyState compact title={messages.sessions.selectSession} /> : null}
            {detail.isLoading ? <TableSkeleton columns={2} rows={4} label={messages.sessions.loadingDetailLabel} /> : null}
            {detail.isError ? (
              <Notice tone="error" title={messages.sessions.unableToLoadDetailTitle}>
                {String(detail.error)}
              </Notice>
            ) : null}
            {detail.data ? (
              <>
                <DefinitionList
                  items={[
                    { label: messages.sessions.fields.workspace, value: detail.data.session.workspace_root || '-' },
                    { label: messages.sessions.fields.digest, value: detail.data.session.workspace_digest || '-' },
                    {
                      label: messages.sessions.fields.traffic,
                      value: messages.sessions.trafficSummary(
                        detail.data.session.control_bytes_in,
                        detail.data.session.control_bytes_out,
                        detail.data.session.stdio_bytes_in,
                        detail.data.session.stdio_bytes_out,
                      ),
                    },
                    { label: messages.sessions.headers.lastActivity, value: formatDateTime(detail.data.session.last_activity_at) },
                  ]}
                />
                <div className="space-y-3">
                  <div className="text-sm font-semibold">{messages.sessions.relatedAudit}</div>
                  {detailRelatedAudit.length === 0 ? (
                    <EmptyState compact title={messages.sessions.noRelatedAuditTitle}>
                      {messages.sessions.noRelatedAuditBody}
                    </EmptyState>
                  ) : (
                    <div className="space-y-3">
                      {detailRelatedAudit.map((item) => (
                        <StackItem
                          key={`${item.timestamp}-${item.action}`}
                          title={translateAuditAction(messages, item.action)}
                          description={item.target}
                          meta={
                            <div className="flex items-center gap-2">
                              <Badge variant={auditResultTone(item.result)}>{translateAuditResult(messages, item.result)}</Badge>
                            </div>
                          }
                        />
                      ))}
                    </div>
                  )}
                </div>
              </>
            ) : null}
          </div>
        </SectionCard>
      </div>
    </div>
  );
}
