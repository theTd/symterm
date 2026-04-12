import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { App } from './app';
import { I18nProvider } from './i18n';
import { adminAPI, type BootstrapPayload } from './lib/api';

vi.mock('./lib/api', async () => {
  const actual = await vi.importActual<typeof import('./lib/api')>('./lib/api');
  return {
    ...actual,
    adminAPI: {
      ...actual.adminAPI,
      bootstrap: vi.fn(),
      overview: vi.fn(),
    },
  };
});

vi.mock('./lib/ws', () => ({
  createAdminWebSocket: vi.fn(() => ({
    close: vi.fn(),
    setCursor: vi.fn(),
  })),
}));

const mockedAdminAPI = vi.mocked(adminAPI, { deep: true });

describe('App', () => {
  it('redirects the root route to overview after bootstrap', async () => {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });

    mockedAdminAPI.bootstrap.mockResolvedValue({
      actor: 'operator',
      daemon: {
        version: 'dev',
        started_at: '2026-04-12T08:00:00Z',
        listen_addr: '127.0.0.1:7001',
        admin_socket_path: '/tmp/admin.sock',
        admin_web_addr: '127.0.0.1:6042',
      },
      api_base: '/admin/api/v1',
      websocket_path: '/admin/ws',
    } satisfies BootstrapPayload);
    mockedAdminAPI.overview.mockResolvedValue({
      daemon: {
        version: 'dev',
        started_at: '2026-04-12T08:00:00Z',
        listen_addr: '127.0.0.1:7001',
        admin_socket_path: '/tmp/admin.sock',
        admin_web_addr: '127.0.0.1:6042',
      },
      active_session_count: 0,
      closed_session_count: 0,
      disabled_user_count: 0,
      needs_confirmation_count: 0,
      recent_events: [],
      recent_audit: [],
    } as never);

    render(
      <I18nProvider initialLocale="en">
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={['/']}>
            <App queryClient={queryClient} />
          </MemoryRouter>
        </QueryClientProvider>
      </I18nProvider>,
    );

    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
    expect(await screen.findByText('No recent daemon events')).toBeInTheDocument();
  });
});
