export type APIEnvelope<T> = {
  ok: boolean;
  data: T;
  error?: {
    code: string;
    message: string;
  };
  meta?: Record<string, unknown>;
};

export type DaemonInfo = {
  version: string;
  started_at: string;
  listen_addr: string;
  admin_socket_path: string;
  admin_web_addr: string;
};

export type BootstrapPayload = {
  actor?: string;
  daemon: DaemonInfo;
  api_base: string;
  websocket_path: string;
};

export type SessionSnapshot = {
  session_id: string;
  client_id: string;
  project_id: string;
  workspace_root: string;
  workspace_digest: string;
  principal: {
    username: string;
    user_disabled: boolean;
    token_id: string;
    token_source: string;
    authenticated_at: string;
  };
  connected_at: string;
  last_activity_at: string;
  close_reason: string;
  role: string;
  project_state: string;
  sync_epoch: number;
  needs_confirmation: boolean;
  control_bytes_in: number;
  control_bytes_out: number;
  stdio_bytes_in: number;
  stdio_bytes_out: number;
  ownerfs_bytes_in: number;
  ownerfs_bytes_out: number;
  attached_command_count: number;
};

export type UserRecord = {
  username: string;
  disabled: boolean;
  created_at: string;
  updated_at: string;
  default_entrypoint: string[];
  token_ids: string[];
  note?: string;
};

export type UserTokenRecord = {
  token_id: string;
  username: string;
  created_at: string;
  last_used_at?: string;
  revoked_at?: string;
  description?: string;
  source: string;
};

export type AuditRecord = {
  timestamp: string;
  action: string;
  actor: string;
  target: string;
  result: string;
};

export type AdminEvent = {
  cursor: number;
  kind: string;
  daemon?: DaemonInfo;
  session?: SessionSnapshot;
  session_id?: string;
  user?: UserRecord;
  token?: UserTokenRecord;
  audit?: AuditRecord;
};

export type Overview = {
  daemon: DaemonInfo;
  active_session_count: number;
  closed_session_count: number;
  disabled_user_count: number;
  needs_confirmation_count: number;
  recent_events: AdminEvent[];
  recent_audit: AuditRecord[];
};

export type SessionDetailResponse = {
  session: SessionSnapshot;
  related_audit: AuditRecord[];
};

export type UserDetailResponse = {
  user: UserRecord;
  tokens: UserTokenRecord[];
  related_audit: AuditRecord[];
};

export type SessionsResponse = { items: SessionSnapshot[] };
export type UsersResponse = { items: UserRecord[] };
export type ActionResult = { status: string; message: string };

export type IssuedToken = {
  Record: UserTokenRecord;
  PlainSecret: string;
};

type RequestInitWithJSON = RequestInit & {
  json?: unknown;
};

export async function apiRequest<T>(path: string, init: RequestInitWithJSON = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.json !== undefined) {
    headers.set('Content-Type', 'application/json');
  }
  const response = await fetch(path, {
    ...init,
    headers,
    body: init.json !== undefined ? JSON.stringify(init.json) : init.body,
    credentials: 'same-origin',
  });
  const payload = (await response.json()) as APIEnvelope<T>;
  if (!response.ok || !payload.ok) {
    throw new Error(payload.error?.message || `request failed: ${response.status}`);
  }
  return payload.data;
}

export async function apiRequestWithMeta<T>(path: string, init: RequestInitWithJSON = {}): Promise<APIEnvelope<T>> {
  const headers = new Headers(init.headers);
  if (init.json !== undefined) {
    headers.set('Content-Type', 'application/json');
  }
  const response = await fetch(path, {
    ...init,
    headers,
    body: init.json !== undefined ? JSON.stringify(init.json) : init.body,
    credentials: 'same-origin',
  });
  const payload = (await response.json()) as APIEnvelope<T>;
  if (!response.ok || !payload.ok) {
    throw new Error(payload.error?.message || `request failed: ${response.status}`);
  }
  return payload;
}

export const adminAPI = {
  bootstrap: () => apiRequest<BootstrapPayload>('/admin/api/v1/bootstrap'),
  overview: () => apiRequest<Overview>('/admin/api/v1/overview'),
  sessions: (search: URLSearchParams) => apiRequest<SessionsResponse>(`/admin/api/v1/sessions?${search.toString()}`),
  session: (id: string) => apiRequest<SessionDetailResponse>(`/admin/api/v1/sessions/${encodeURIComponent(id)}`),
  terminateSession: (id: string) =>
    apiRequest<ActionResult>(`/admin/api/v1/sessions/${encodeURIComponent(id)}/terminate`, { method: 'POST' }),
  users: () => apiRequest<UsersResponse>('/admin/api/v1/users'),
  user: (username: string) => apiRequest<UserDetailResponse>(`/admin/api/v1/users/${encodeURIComponent(username)}`),
  createUser: (username: string, note: string) =>
    apiRequest<UserRecord>('/admin/api/v1/users', { method: 'POST', json: { username, note } }),
  disableUser: (username: string) =>
    apiRequest<ActionResult>(`/admin/api/v1/users/${encodeURIComponent(username)}/disable`, { method: 'POST' }),
  issueToken: (username: string, description: string) =>
    apiRequest<IssuedToken>(`/admin/api/v1/users/${encodeURIComponent(username)}/tokens`, {
      method: 'POST',
      json: { description },
    }),
  revokeToken: (tokenID: string) =>
    apiRequest<UserTokenRecord>(`/admin/api/v1/tokens/${encodeURIComponent(tokenID)}/revoke`, { method: 'POST' }),
  entrypoint: (username: string) =>
    apiRequest<{ username: string; entrypoint: string[] }>(
      `/admin/api/v1/users/${encodeURIComponent(username)}/entrypoint`,
    ),
  setEntrypoint: (username: string, entrypoint: string[]) =>
    apiRequest<UserRecord>(`/admin/api/v1/users/${encodeURIComponent(username)}/entrypoint`, {
      method: 'PUT',
      json: { entrypoint },
    }),
  audit: (search: URLSearchParams) => apiRequestWithMeta<AuditRecord[]>(`/admin/api/v1/audit?${search.toString()}`),
};
