import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { EmptyState, Notice, TableSkeleton } from '../components/StateView';
import { ActionCluster, DefinitionList, PageIntro, SectionCard, StackItem } from '../components/AdminPrimitives';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Table, TableBody, TableCell, TableContainer, TableHead, TableHeaderCell, TableRow } from '../components/ui/table';
import { Textarea } from '../components/ui/textarea';
import { auditResultTone, userStatusTone } from '../lib/presentation';
import { translateAuditAction, translateAuditResult, translateTokenSource, translateUserStatus, useI18n } from '../i18n';
import { adminAPI } from '../lib/api';
import { stableSort } from '../lib/sort';

type FeedbackCode = 'usernameRequired' | 'userCreated' | 'userDisabled' | 'tokenIssued' | 'entrypointUpdated' | 'tokenRevoked';

export function UsersPage() {
  const { messages, formatDateTime } = useI18n();
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const selectedUser = params.get('user');
  const [newUser, setNewUser] = useState('');
  const [note, setNote] = useState('');
  const [entrypoint, setEntrypoint] = useState('[]');
  const [tokenDescription, setTokenDescription] = useState('');
  const [issuedSecret, setIssuedSecret] = useState('');
  const [feedback, setFeedback] = useState<
    { tone: 'success' | 'error'; code: FeedbackCode; username?: string; tokenID?: string } | null
  >(null);

  const users = useQuery({
    queryKey: ['users'],
    queryFn: adminAPI.users,
  });
  const detail = useQuery({
    queryKey: ['user', selectedUser],
    queryFn: () => adminAPI.user(selectedUser!),
    enabled: Boolean(selectedUser),
  });

  useEffect(() => {
    if (detail.data?.user.default_entrypoint) {
      setEntrypoint(JSON.stringify(detail.data.user.default_entrypoint));
    }
  }, [detail.data?.user.default_entrypoint]);

  const refreshAll = async () => {
    await queryClient.invalidateQueries({ queryKey: ['users'] });
    await queryClient.invalidateQueries({ queryKey: ['user'] });
    await queryClient.invalidateQueries({ queryKey: ['audit'] });
  };

  const createUser = useMutation({
    mutationFn: () => adminAPI.createUser(newUser, note),
    onSuccess: async (user) => {
      setFeedback({
        tone: 'success',
        code: 'userCreated',
        username: user.username,
      });
      setNewUser('');
      setNote('');
      const next = new URLSearchParams(params);
      next.set('user', user.username);
      setParams(next);
      await refreshAll();
    },
  });

  const disableUser = useMutation({
    mutationFn: (username: string) => adminAPI.disableUser(username),
    onSuccess: async (_, username) => {
      setFeedback({
        tone: 'success',
        code: 'userDisabled',
        username,
      });
      await refreshAll();
    },
  });

  const issueToken = useMutation({
    mutationFn: (username: string) => adminAPI.issueToken(username, tokenDescription),
    onSuccess: async (token) => {
      setIssuedSecret(token.PlainSecret);
      setTokenDescription('');
      setFeedback({
        tone: 'success',
        code: 'tokenIssued',
        username: token.Record.username || selectedUser || '',
      });
      await refreshAll();
    },
  });

  const saveEntrypoint = useMutation({
    mutationFn: async (username: string) => {
      const parsed = JSON.parse(entrypoint) as string[];
      if (!Array.isArray(parsed) || parsed.some((item) => typeof item !== 'string')) {
        throw new Error(messages.users.entrypointValidationError);
      }
      return adminAPI.setEntrypoint(username, parsed);
    },
    onSuccess: async (_, username) => {
      setFeedback({
        tone: 'success',
        code: 'entrypointUpdated',
        username,
      });
      await refreshAll();
    },
  });

  const revokeToken = useMutation({
    mutationFn: (tokenID: string) => adminAPI.revokeToken(tokenID),
    onSuccess: async (token) => {
      setFeedback({
        tone: 'success',
        code: 'tokenRevoked',
        tokenID: token.token_id,
      });
      await refreshAll();
    },
  });

  const sortedUsers = stableSort(users.data?.items ?? [], (left, right) => {
    if (left.disabled !== right.disabled) {
      return Number(left.disabled) - Number(right.disabled);
    }
    return left.username.localeCompare(right.username);
  });
  const detailTokens = detail.data?.tokens ?? [];
  const detailRelatedAudit = detail.data?.related_audit ?? [];
  const feedbackTitle =
    feedback?.code === 'usernameRequired'
      ? messages.users.usernameRequiredTitle
      : feedback?.code === 'userCreated'
        ? messages.users.userCreatedTitle
        : feedback?.code === 'userDisabled'
          ? messages.users.userDisabledTitle
          : feedback?.code === 'tokenIssued'
            ? messages.users.tokenIssuedTitle
            : feedback?.code === 'entrypointUpdated'
              ? messages.users.entrypointUpdatedTitle
              : feedback?.code === 'tokenRevoked'
                ? messages.users.tokenRevokedTitle
                : '';
  const feedbackBody =
    feedback?.code === 'usernameRequired'
      ? messages.users.usernameRequiredBody
      : feedback?.code === 'userCreated'
        ? messages.users.userCreatedBody(feedback.username || '')
        : feedback?.code === 'userDisabled'
          ? messages.users.userDisabledBody(feedback.username || '')
          : feedback?.code === 'tokenIssued'
            ? messages.users.tokenIssuedBody(feedback.username || '')
            : feedback?.code === 'entrypointUpdated'
              ? messages.users.entrypointUpdatedBody(feedback.username || '')
              : feedback?.code === 'tokenRevoked'
                ? messages.users.tokenRevokedBody(feedback.tokenID || '')
                : '';

  return (
    <div className="space-y-5">
      <PageIntro eyebrow={messages.frame.nav.users} title={messages.users.title} description={messages.users.description} />
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.08fr)_minmax(400px,0.92fr)]">
        <div className="space-y-5">
          <SectionCard title={messages.users.createUser} description={messages.users.description}>
            <div className="space-y-4">
              {feedback ? <Notice tone={feedback.tone} title={feedbackTitle}>{feedbackBody}</Notice> : null}
              {createUser.isError ? (
                <Notice tone="error" title={messages.users.createUserFailedTitle}>
                  {String(createUser.error)}
                </Notice>
              ) : null}
              <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
                <Input
                  placeholder={messages.users.placeholders.username}
                  value={newUser}
                  onChange={(event) => setNewUser(event.target.value)}
                />
                <Input placeholder={messages.users.placeholders.note} value={note} onChange={(event) => setNote(event.target.value)} />
                <Button
                  className="w-full md:w-auto"
                  onClick={() => {
                    if (!newUser.trim()) {
                      setFeedback({
                        tone: 'error',
                        code: 'usernameRequired',
                      });
                      return;
                    }
                    setFeedback(null);
                    createUser.mutate();
                  }}
                  disabled={createUser.isPending}
                >
                  {createUser.isPending ? messages.users.creatingUser : messages.users.createUser}
                </Button>
              </div>
            </div>
          </SectionCard>
          <SectionCard
            title={messages.users.title}
            description={messages.users.description}
            actions={
              <ActionCluster>
                <Badge variant="neutral">{sortedUsers.length}</Badge>
                <Badge variant="info">{selectedUser || messages.common.anonymous}</Badge>
              </ActionCluster>
            }
          >
            <div className="space-y-4">
              {users.isLoading ? <TableSkeleton columns={4} rows={6} label={messages.users.loadingUsersLabel} /> : null}
              {users.isError ? (
                <Notice tone="error" title={messages.users.unableToLoadUsersTitle}>
                  {String(users.error)}
                </Notice>
              ) : null}
              {users.data ? (
                sortedUsers.length === 0 ? (
                  <EmptyState title={messages.users.noUsersTitle}>{messages.users.noUsersBody}</EmptyState>
                ) : (
                  <TableContainer>
                    <Table>
                      <TableHead>
                        <tr>
                          <TableHeaderCell>{messages.users.headers.user}</TableHeaderCell>
                          <TableHeaderCell>{messages.users.headers.status}</TableHeaderCell>
                          <TableHeaderCell>{messages.users.headers.tokens}</TableHeaderCell>
                          <TableHeaderCell>{messages.users.headers.entrypoint}</TableHeaderCell>
                        </tr>
                      </TableHead>
                      <TableBody>
                        {sortedUsers.map((item) => (
                          <TableRow
                            key={item.username}
                            data-selected={selectedUser === item.username}
                            className="cursor-pointer"
                            onClick={() => setParams({ user: item.username })}
                          >
                            <TableCell className="font-medium">{item.username}</TableCell>
                            <TableCell>
                              <Badge variant={userStatusTone(item.disabled)}>{translateUserStatus(messages, item.disabled)}</Badge>
                            </TableCell>
                            <TableCell>{item.token_ids?.length ?? 0}</TableCell>
                            <TableCell className="max-w-[18rem] truncate">{item.default_entrypoint?.join(' ') || '-'}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </TableContainer>
                )
              ) : null}
            </div>
          </SectionCard>
        </div>
        <SectionCard
          title={messages.users.detailTitle}
          description={messages.users.detailDescription}
          className="xl:sticky xl:top-28"
          actions={
            selectedUser ? (
              <Button
                variant="danger"
                onClick={() => {
                  if (window.confirm(messages.users.disableConfirm(selectedUser))) {
                    disableUser.mutate(selectedUser);
                  }
                }}
                disabled={disableUser.isPending}
              >
                {disableUser.isPending ? messages.users.disabling : messages.users.disable}
              </Button>
            ) : undefined
          }
        >
          <div className="space-y-5">
            {!selectedUser ? <EmptyState compact title={messages.users.selectUser} /> : null}
            {disableUser.isError ? (
              <Notice tone="error" title={messages.users.disableUserFailedTitle}>
                {String(disableUser.error)}
              </Notice>
            ) : null}
            {issueToken.isError ? (
              <Notice tone="error" title={messages.users.issueTokenFailedTitle}>
                {String(issueToken.error)}
              </Notice>
            ) : null}
            {saveEntrypoint.isError ? (
              <Notice tone="error" title={messages.users.saveEntrypointFailedTitle}>
                {String(saveEntrypoint.error)}
              </Notice>
            ) : null}
            {revokeToken.isError ? (
              <Notice tone="error" title={messages.users.revokeTokenFailedTitle}>
                {String(revokeToken.error)}
              </Notice>
            ) : null}
            {detail.isLoading ? <TableSkeleton columns={2} rows={6} label={messages.users.loadingDetailLabel} /> : null}
            {detail.isError ? (
              <Notice tone="error" title={messages.users.unableToLoadDetailTitle}>
                {String(detail.error)}
              </Notice>
            ) : null}
            {detail.data ? (
              <>
                <DefinitionList
                  items={[
                    { label: messages.users.fields.note, value: detail.data.user.note || '-' },
                    { label: messages.users.fields.updated, value: formatDateTime(detail.data.user.updated_at) },
                    { label: messages.users.fields.tokens, value: detailTokens.length },
                    {
                      label: messages.users.headers.status,
                      value: <Badge variant={userStatusTone(detail.data.user.disabled)}>{translateUserStatus(messages, detail.data.user.disabled)}</Badge>,
                    },
                  ]}
                />
                <SectionCard title={messages.users.issueToken} className="border-[color:var(--border-subtle)] bg-[var(--surface-card)]/70" contentClassName="pt-4">
                  <div className="space-y-3">
                    <Input
                      placeholder={messages.users.placeholders.tokenDescription}
                      value={tokenDescription}
                      onChange={(event) => setTokenDescription(event.target.value)}
                    />
                    <Button
                      onClick={() => {
                        setFeedback(null);
                        if (selectedUser) {
                          issueToken.mutate(selectedUser);
                        }
                      }}
                      disabled={issueToken.isPending}
                    >
                      {issueToken.isPending ? messages.users.issuingToken : messages.users.issueToken}
                    </Button>
                    {issuedSecret ? (
                      <Notice tone="success" title={messages.users.tokenIssuedTitle}>
                        {messages.users.issuedTokenSecret(issuedSecret)}
                      </Notice>
                    ) : null}
                  </div>
                </SectionCard>
                <SectionCard title={messages.users.saveEntrypoint} className="border-[color:var(--border-subtle)] bg-[var(--surface-card)]/70" contentClassName="pt-4">
                  <div className="space-y-3">
                    <Textarea
                      value={entrypoint}
                      onChange={(event) => setEntrypoint(event.target.value)}
                      placeholder={messages.users.placeholders.entrypoint}
                    />
                    <Button
                      variant="secondary"
                      onClick={() => {
                        setFeedback(null);
                        if (selectedUser) {
                          saveEntrypoint.mutate(selectedUser);
                        }
                      }}
                      disabled={saveEntrypoint.isPending}
                    >
                      {saveEntrypoint.isPending ? messages.users.savingEntrypoint : messages.users.saveEntrypoint}
                    </Button>
                  </div>
                </SectionCard>
                <div className="space-y-3">
                  <div className="text-sm font-semibold">{messages.users.tokens}</div>
                  {detailTokens.length === 0 ? (
                    <EmptyState compact title={messages.users.noTokensTitle}>{messages.users.noTokensBody}</EmptyState>
                  ) : (
                    <div className="space-y-3">
                      {detailTokens.map((token) => (
                        <StackItem
                          key={token.token_id}
                          title={
                            <>
                              <span>{token.token_id}</span>
                              <Badge variant="info">{translateTokenSource(messages, token.source)}</Badge>
                            </>
                          }
                          description={token.description || translateTokenSource(messages, token.source)}
                          meta={
                            <ActionCluster>
                              {token.last_used_at ? <Badge variant="neutral">{formatDateTime(token.last_used_at)}</Badge> : null}
                              <Button
                                variant="danger"
                                size="sm"
                                onClick={() => {
                                  if (window.confirm(messages.users.revokeConfirm(token.token_id))) {
                                    setFeedback(null);
                                    revokeToken.mutate(token.token_id);
                                  }
                                }}
                                disabled={revokeToken.isPending}
                              >
                                {messages.users.revoke}
                              </Button>
                            </ActionCluster>
                          }
                        />
                      ))}
                    </div>
                  )}
                </div>
                <div className="space-y-3">
                  <div className="text-sm font-semibold">{messages.users.relatedAudit}</div>
                  {detailRelatedAudit.length === 0 ? (
                    <EmptyState compact title={messages.users.noRelatedAuditTitle}>{messages.users.noRelatedAuditBody}</EmptyState>
                  ) : (
                    <div className="space-y-3">
                      {detailRelatedAudit.map((item) => (
                        <StackItem
                          key={`${item.timestamp}-${item.action}`}
                          title={translateAuditAction(messages, item.action)}
                          description={item.target}
                          meta={<Badge variant={auditResultTone(item.result)}>{translateAuditResult(messages, item.result)}</Badge>}
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
