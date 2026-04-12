import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { UserDetailResponse, UserRecord, UserTokenRecord } from '../lib/api';
import { adminAPI } from '../lib/api';
import { renderRoute } from '../test/render';
import { UsersPage } from './UsersPage';

vi.mock('../lib/api', () => ({
  adminAPI: {
    users: vi.fn(),
    user: vi.fn(),
    createUser: vi.fn(),
    disableUser: vi.fn(),
    issueToken: vi.fn(),
    setEntrypoint: vi.fn(),
    revokeToken: vi.fn(),
  },
}));

const mockedAdminAPI = vi.mocked(adminAPI, { deep: true });

function makeUser(username: string, overrides: Partial<UserRecord> = {}): UserRecord {
  return {
    username,
    disabled: false,
    created_at: '2026-04-12T08:00:00Z',
    updated_at: '2026-04-12T08:00:00Z',
    default_entrypoint: ['bash'],
    token_ids: [],
    note: '',
    ...overrides,
  };
}

function makeToken(tokenID: string, username: string, overrides: Partial<UserTokenRecord> = {}): UserTokenRecord {
  return {
    token_id: tokenID,
    username,
    created_at: '2026-04-12T08:00:00Z',
    source: 'managed',
    ...overrides,
  };
}

function cloneDetail(detail: UserDetailResponse): UserDetailResponse {
  return {
    user: { ...detail.user, default_entrypoint: [...detail.user.default_entrypoint], token_ids: [...detail.user.token_ids] },
    tokens: detail.tokens.map((token) => ({ ...token })),
    related_audit: detail.related_audit.map((item) => ({ ...item })),
  };
}

function setupUsersStore(initialUsers: UserRecord[], initialDetails: Record<string, UserDetailResponse>) {
  const store = {
    users: initialUsers.map((user) => ({ ...user, default_entrypoint: [...user.default_entrypoint], token_ids: [...user.token_ids] })),
    details: Object.fromEntries(Object.entries(initialDetails).map(([username, detail]) => [username, cloneDetail(detail)])),
  };

  mockedAdminAPI.users.mockImplementation(async () => ({
    items: store.users.map((user) => ({ ...user, default_entrypoint: [...user.default_entrypoint], token_ids: [...user.token_ids] })),
  }));
  mockedAdminAPI.user.mockImplementation(async (username: string) => cloneDetail(store.details[username]));
  mockedAdminAPI.createUser.mockImplementation(async (username: string, note: string) => {
    const created = makeUser(username, { note, default_entrypoint: [], token_ids: [] });
    store.users.push(created);
    store.details[username] = {
      user: { ...created, default_entrypoint: [], token_ids: [] },
      tokens: [],
      related_audit: [],
    };
    return { ...created };
  });
  mockedAdminAPI.issueToken.mockImplementation(async (username: string, description: string) => {
    const token = makeToken(`managed-${store.details[username].tokens.length + 1}`, username, { description });
    store.details[username].tokens.push(token);
    store.details[username].user.token_ids.push(token.token_id);
    store.users = store.users.map((user) =>
      user.username === username ? { ...user, token_ids: [...store.details[username].user.token_ids] } : user,
    );
    return {
      Record: { ...token },
      PlainSecret: 'secret-value',
    };
  });
  mockedAdminAPI.setEntrypoint.mockImplementation(async (username: string, entrypoint: string[]) => {
    store.details[username].user.default_entrypoint = [...entrypoint];
    store.users = store.users.map((user) =>
      user.username === username ? { ...user, default_entrypoint: [...entrypoint] } : user,
    );
    return { ...store.details[username].user, default_entrypoint: [...entrypoint], token_ids: [...store.details[username].user.token_ids] };
  });
  mockedAdminAPI.revokeToken.mockImplementation(async (tokenID: string) => {
    const match = Object.values(store.details).find((detail) => detail.tokens.some((token) => token.token_id === tokenID));
    if (!match) {
      throw new Error(`unknown token ${tokenID}`);
    }
    match.tokens = match.tokens.filter((token) => token.token_id !== tokenID);
    match.user.token_ids = match.user.token_ids.filter((id) => id !== tokenID);
    store.users = store.users.map((user) =>
      user.username === match.user.username ? { ...user, token_ids: [...match.user.token_ids] } : user,
    );
    return makeToken(tokenID, match.user.username);
  });
  mockedAdminAPI.disableUser.mockImplementation(async (username: string) => {
    store.users = store.users.map((user) => (user.username === username ? { ...user, disabled: true } : user));
    store.details[username].user.disabled = true;
    return { status: 'ok', message: 'disabled' };
  });

  return store;
}

describe('UsersPage', () => {
  it('validates user creation and refreshes the list after create', async () => {
    setupUsersStore([], {});
    const user = userEvent.setup();

    renderRoute(<UsersPage />, { path: '/users', route: '/users' });

    expect(await screen.findByText('No managed users provisioned yet.')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create user' }));
    expect(await screen.findByText('Username required')).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText('username'), 'bob');
    await user.type(screen.getByPlaceholderText('note'), 'ops');
    await user.click(screen.getByRole('button', { name: 'Create user' }));

    expect(await screen.findByText('User created')).toBeInTheDocument();
    expect(await screen.findAllByText('bob')).not.toHaveLength(0);
  });

  it('runs the managed user lifecycle for tokens, entrypoint, revoke, and disable', async () => {
    setupUsersStore(
      [makeUser('alice', { note: 'primary operator' })],
      {
        alice: {
          user: makeUser('alice', { note: 'primary operator' }),
          tokens: [makeToken('managed-existing', 'alice')],
          related_audit: [
            {
              timestamp: '2026-04-12T08:05:00Z',
              action: 'issue_managed_token',
              actor: 'admin',
              target: 'alice:managed-existing',
              result: 'ok',
            },
          ],
        },
      },
    );
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);

    renderRoute(<UsersPage />, { path: '/users', route: '/users?user=alice' });

    expect(await screen.findAllByText('alice')).not.toHaveLength(0);
    expect(await screen.findAllByText('managed')).not.toHaveLength(0);
    expect(await screen.findByText('issue managed token')).toBeInTheDocument();
    expect(await screen.findByText('ok')).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText('token description'), 'cli');
    await user.click(screen.getByRole('button', { name: 'Issue token' }));

    expect(await screen.findAllByText('Token issued')).not.toHaveLength(0);
    expect(await screen.findByText(/secret-value/)).toBeInTheDocument();
    expect(await screen.findByText('managed-2')).toBeInTheDocument();

    const entrypointEditor = screen.getByPlaceholderText('["bash","-lc"]');
    fireEvent.change(entrypointEditor, { target: { value: '["bash",1]' } });
    await user.click(screen.getByRole('button', { name: 'Save entrypoint' }));
    expect(await screen.findByText('Entrypoint update failed')).toBeInTheDocument();

    fireEvent.change(entrypointEditor, { target: { value: '["bash","-lc"]' } });
    await user.click(screen.getByRole('button', { name: 'Save entrypoint' }));
    expect(await screen.findByText('Entrypoint updated')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('bash -lc')).toBeInTheDocument());

    await user.click(screen.getAllByRole('button', { name: 'Revoke' })[1]);
    expect(await screen.findByText('Token revoked')).toBeInTheDocument();
    expect(screen.queryByText('managed-2')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Disable' }));
    expect(await screen.findByText('User disabled')).toBeInTheDocument();
  });
});
