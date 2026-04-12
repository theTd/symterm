import { screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { adminAPI } from '../lib/api';
import { renderRoute } from '../test/render';
import { OverviewPage } from './OverviewPage';

vi.mock('../lib/api', () => ({
  adminAPI: {
    overview: vi.fn(),
  },
}));

const mockedAdminAPI = vi.mocked(adminAPI, { deep: true });

describe('OverviewPage', () => {
  it('renders empty states when overview payload is empty', async () => {
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
    });

    renderRoute(<OverviewPage />, { path: '/', route: '/' });

    expect(await screen.findByText('No recent daemon events')).toBeInTheDocument();
    expect(await screen.findByText('No audit records yet')).toBeInTheDocument();
  });

  it('translates backend audit values in the overview table', async () => {
    mockedAdminAPI.overview.mockResolvedValue({
      daemon: {
        version: 'dev',
        started_at: '2026-04-12T08:00:00Z',
        listen_addr: '127.0.0.1:7001',
        admin_socket_path: '/tmp/admin.sock',
        admin_web_addr: '127.0.0.1:6042',
      },
      active_session_count: 1,
      closed_session_count: 0,
      disabled_user_count: 0,
      needs_confirmation_count: 0,
      recent_events: [],
      recent_audit: [
        {
          timestamp: '2026-04-12T09:00:00Z',
          action: 'create_user',
          actor: 'admin',
          target: 'alice',
          result: 'ok',
        },
      ],
    });

    renderRoute(<OverviewPage />, { path: '/', route: '/' });

    expect(await screen.findByText('create user')).toBeInTheDocument();
    expect(await screen.findByText('ok')).toBeInTheDocument();
  });

  it('renders Chinese copy when the locale is zh-CN', async () => {
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
    });

    renderRoute(<OverviewPage />, { path: '/', route: '/', locale: 'zh-CN' });

    expect(await screen.findByText('暂无最近的守护进程事件')).toBeInTheDocument();
    expect(await screen.findByText('暂无审计记录')).toBeInTheDocument();
  });
});
