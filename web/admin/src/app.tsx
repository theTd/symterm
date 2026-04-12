import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { QueryClient } from '@tanstack/react-query';
import { Navigate, Route, Routes } from 'react-router-dom';
import { AppFrame } from './components/AppFrame';
import { LanguageSwitcher } from './components/LanguageSwitcher';
import { Notice } from './components/StateView';
import { Card, CardContent } from './components/ui/card';
import { useI18n } from './i18n';
import { adminAPI } from './lib/api';
import { createAdminWebSocket } from './lib/ws';
import { AuditPage } from './pages/AuditPage';
import { OverviewPage } from './pages/OverviewPage';
import { SessionsPage } from './pages/SessionsPage';
import { SystemPage } from './pages/SystemPage';
import { UsersPage } from './pages/UsersPage';

export function App(props: { queryClient: QueryClient }) {
  const [connection, setConnection] = useState<'connecting' | 'live' | 'offline' | 'resync'>('connecting');
  const [connectionDetailKey, setConnectionDetailKey] = useState<'opening' | 'connecting' | 'cursorExpired' | 'disconnected'>(
    'opening',
  );
  const wsRef = useRef<ReturnType<typeof createAdminWebSocket> | null>(null);
  const { messages } = useI18n();

  const bootstrap = useQuery({
    queryKey: ['bootstrap'],
    queryFn: adminAPI.bootstrap,
  });

  useEffect(() => {
    wsRef.current?.close();
    wsRef.current = null;
    if (!bootstrap.data) {
      return;
    }
    wsRef.current = createAdminWebSocket(0, {
      onConnecting: () => {
        setConnection('connecting');
        setConnectionDetailKey('connecting');
      },
      onOpen: () => setConnection('live'),
      onEvent: async (event) => {
        if (event.kind === 'daemon_updated') {
          await props.queryClient.invalidateQueries({ queryKey: ['bootstrap'] });
        }
        if (event.kind.startsWith('session_')) {
          await props.queryClient.invalidateQueries({ queryKey: ['sessions'] });
          await props.queryClient.invalidateQueries({ queryKey: ['session'] });
        }
        if (event.kind.startsWith('user_') || event.kind.startsWith('token_')) {
          await props.queryClient.invalidateQueries({ queryKey: ['users'] });
          await props.queryClient.invalidateQueries({ queryKey: ['user'] });
        }
        if (event.kind === 'audit_appended') {
          await props.queryClient.invalidateQueries({ queryKey: ['audit'] });
        }
        await props.queryClient.invalidateQueries({ queryKey: ['overview'] });
      },
      onCursorExpired: async () => {
        setConnection('resync');
        setConnectionDetailKey('cursorExpired');
        await props.queryClient.invalidateQueries();
      },
      onAuthError: async () => {
        setConnection('offline');
        setConnectionDetailKey('disconnected');
        await props.queryClient.invalidateQueries({ queryKey: ['bootstrap'] });
      },
      onClose: () => {
        setConnection('offline');
        setConnectionDetailKey('disconnected');
      },
    });
    return () => wsRef.current?.close();
  }, [bootstrap.data, props.queryClient]);

  if (bootstrap.isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center px-4">
        <Card className="w-full max-w-xl border-[color:var(--border-strong)] bg-[var(--surface-shell)]">
          <CardContent className="space-y-4 p-8 text-center">
            <div className="inline-flex rounded-full border border-[color:var(--accent)]/24 bg-[var(--accent)]/10 px-3 py-1 text-[0.7rem] font-semibold uppercase tracking-[0.24em] text-[var(--accent)]">
              symterm
            </div>
            <h1 className="text-3xl font-semibold tracking-[-0.04em] text-[var(--foreground)]">{messages.frame.title}</h1>
            <p className="text-sm text-[var(--muted-foreground)]">{messages.app.bootstrapping}</p>
          </CardContent>
        </Card>
      </div>
    );
  }
  if (bootstrap.isError || !bootstrap.data) {
    return (
      <div className="flex min-h-screen items-center justify-center px-4">
        <Card className="w-full max-w-2xl border-[color:var(--border-strong)] bg-[var(--surface-shell)]">
          <CardContent className="space-y-6 p-8">
            <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="space-y-3">
                <div className="inline-flex rounded-full border border-[color:var(--accent)]/24 bg-[var(--accent)]/10 px-3 py-1 text-[0.7rem] font-semibold uppercase tracking-[0.24em] text-[var(--accent)]">
                  symterm
                </div>
                <h1 className="text-3xl font-semibold tracking-[-0.04em] text-[var(--foreground)]">{messages.frame.title}</h1>
                <p className="max-w-xl text-sm leading-7 text-[var(--muted-foreground)]">{messages.frame.description}</p>
              </div>
              <LanguageSwitcher />
            </div>
            <Notice tone="error" title={messages.app.bootstrapFailed}>
              {String(bootstrap.error)}
            </Notice>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <Routes>
      <Route
        path="/"
        element={
          <AppFrame
            actor={bootstrap.data.actor}
            connection={connection}
            connectionDetail={connection === 'live' ? undefined : messages.app.connectionDetails[connectionDetailKey]}
          />
        }
      >
        <Route index element={<Navigate to="/overview" replace />} />
        <Route path="overview" element={<OverviewPage />} />
        <Route path="sessions" element={<SessionsPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="system" element={<SystemPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/overview" replace />} />
    </Routes>
  );
}
