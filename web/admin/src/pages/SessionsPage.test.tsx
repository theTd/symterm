import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { SessionDetailResponse, SessionSnapshot } from '../lib/api';
import { adminAPI } from '../lib/api';
import { renderRoute } from '../test/render';
import { SessionsPage } from './SessionsPage';

vi.mock('../lib/api', () => ({
  adminAPI: {
    sessions: vi.fn(),
    session: vi.fn(),
    terminateSession: vi.fn(),
  },
}));

const mockedAdminAPI = vi.mocked(adminAPI, { deep: true });

function makeSession(sessionID: string, overrides: Partial<SessionSnapshot> = {}): SessionSnapshot {
  return {
    session_id: sessionID,
    client_id: `${sessionID}-client`,
    project_id: 'demo',
    workspace_root: '/workspace/demo',
    workspace_digest: 'digest',
    principal: {
      username: 'alice',
      user_disabled: false,
      token_id: 'token-1',
      token_source: 'managed',
      authenticated_at: '2026-04-12T08:00:00Z',
    },
    connected_at: '2026-04-12T08:00:00Z',
    last_activity_at: '2026-04-12T08:00:00Z',
    close_reason: '',
    role: 'owner',
    project_state: 'active',
    sync_epoch: 2,
    needs_confirmation: false,
    control_bytes_in: 1,
    control_bytes_out: 2,
    stdio_bytes_in: 3,
    stdio_bytes_out: 4,
    ownerfs_bytes_in: 5,
    ownerfs_bytes_out: 6,
    attached_command_count: 1,
    ...overrides,
  };
}

function setupSessionsStore(initialItems: SessionSnapshot[], initialDetails: Record<string, SessionDetailResponse>) {
  const store = {
    items: initialItems.map((item) => ({ ...item, principal: { ...item.principal } })),
    details: Object.fromEntries(
      Object.entries(initialDetails).map(([id, detail]) => [
        id,
        {
          session: { ...detail.session, principal: { ...detail.session.principal } },
          related_audit: detail.related_audit.map((item) => ({ ...item })),
        },
      ]),
    ),
  };

  mockedAdminAPI.sessions.mockImplementation(async () => ({
    items: store.items.map((item) => ({ ...item, principal: { ...item.principal } })),
  }));
  mockedAdminAPI.session.mockImplementation(async (id: string) => ({
    session: { ...store.details[id].session, principal: { ...store.details[id].session.principal } },
    related_audit: store.details[id].related_audit.map((item) => ({ ...item })),
  }));
  mockedAdminAPI.terminateSession.mockImplementation(async (id: string) => {
    store.items = store.items.map((item) => (item.session_id === id ? { ...item, close_reason: 'terminated' } : item));
    store.details[id].session.close_reason = 'terminated';
    return { status: 'ok', message: 'terminated' };
  });
}

describe('SessionsPage', () => {
  it('renders a stable activity-sorted table and terminates a selected session', async () => {
    setupSessionsStore(
      [
        makeSession('sess-1', { last_activity_at: '2026-04-12T08:00:00Z' }),
        makeSession('sess-2', { last_activity_at: '2026-04-12T09:00:00Z', principal: { username: 'bob', user_disabled: false, token_id: 'token-2', token_source: 'managed', authenticated_at: '2026-04-12T08:00:00Z' } }),
      ],
      {
        'sess-1': {
          session: makeSession('sess-1'),
          related_audit: [{ timestamp: '2026-04-12T08:10:00Z', action: 'session.opened', actor: 'alice', target: 'sess-1', result: 'ok' }],
        },
      },
    );
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);

    renderRoute(<SessionsPage />, { path: '/sessions', route: '/sessions?session=sess-1' });

    expect(await screen.findAllByText('sess-1')).not.toHaveLength(0);
    const rows = screen.getAllByRole('row');
    expect(rows[1]).toHaveTextContent('sess-2');
    expect(rows[2]).toHaveTextContent('sess-1');

    await user.click(screen.getByRole('button', { name: 'Terminate' }));
    expect(await screen.findByText('Session terminated')).toBeInTheDocument();
    expect(await screen.findByText('closed: terminated')).toBeInTheDocument();
  });

  it('shows a no-results state when filters remove every session', async () => {
    mockedAdminAPI.sessions.mockResolvedValue({ items: [] });
    mockedAdminAPI.session.mockResolvedValue({
      session: makeSession('unused'),
      related_audit: [],
    });

    renderRoute(<SessionsPage />, { path: '/sessions', route: '/sessions?username=nobody' });

    expect(await screen.findByText('No sessions match the current filters.')).toBeInTheDocument();
  });
});
