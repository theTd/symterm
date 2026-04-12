import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { adminAPI } from '../lib/api';
import { renderRoute } from '../test/render';
import { AuditPage } from './AuditPage';

vi.mock('../lib/api', () => ({
  adminAPI: {
    audit: vi.fn(),
  },
}));

const mockedAdminAPI = vi.mocked(adminAPI, { deep: true });

describe('AuditPage', () => {
  it('shows a filtered empty state and keeps filter inputs in sync', async () => {
    mockedAdminAPI.audit.mockResolvedValue({
      ok: true,
      data: [],
      meta: {
        total: 0,
        page_size: 20,
      },
    });
    const user = userEvent.setup();

    renderRoute(<AuditPage />, { path: '/audit', route: '/audit?actor=alice' });

    expect(await screen.findByText('No audit records match the current filters.')).toBeInTheDocument();
    const actionInput = screen.getByPlaceholderText('action');
    await user.type(actionInput, 'token.issue');
    expect(actionInput).toHaveValue('token.issue');
  });

  it('translates backend audit action and result values', async () => {
    mockedAdminAPI.audit.mockResolvedValue({
      ok: true,
      data: [
        {
          timestamp: '2026-04-12T08:00:00Z',
          action: 'issue_managed_token',
          actor: 'alice',
          target: 'alice:managed-1',
          result: 'error:token quota exceeded',
        },
      ],
      meta: {
        total: 1,
        page_size: 20,
      },
    });

    renderRoute(<AuditPage />, { path: '/audit', route: '/audit' });

    expect(await screen.findByText('issue managed token')).toBeInTheDocument();
    expect(await screen.findByText('error: token quota exceeded')).toBeInTheDocument();
  });
});
