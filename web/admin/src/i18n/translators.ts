import type { Messages } from './message-catalog';
import type { ConnectionState } from './locales';

export function translateConnectionState(messages: Messages, state: ConnectionState) {
  return messages.common.connection[state];
}

export function translateTokenSource(messages: Messages, source: string | undefined) {
  switch (source) {
    case 'managed':
      return messages.common.backend.tokenSource.managed;
    case undefined:
    case '':
      return '-';
    default:
      return source;
  }
}

export function translateAuditAction(messages: Messages, action: string | undefined) {
  switch (action) {
    case 'create_user':
      return messages.common.backend.auditAction.create_user;
    case 'disable_user':
      return messages.common.backend.auditAction.disable_user;
    case 'issue_managed_token':
      return messages.common.backend.auditAction.issue_managed_token;
    case 'revoke_managed_token':
      return messages.common.backend.auditAction.revoke_managed_token;
    case 'set_entrypoint':
      return messages.common.backend.auditAction.set_entrypoint;
    case undefined:
    case '':
      return '-';
    default:
      return action;
  }
}

export function translateAuditResult(messages: Messages, result: string | undefined) {
  if (result === undefined || result === '') {
    return '-';
  }
  if (result === 'ok') {
    return messages.common.backend.auditResult.ok;
  }
  if (result.startsWith('error:')) {
    const detail = result.slice('error:'.length).trim();
    return detail
      ? `${messages.common.backend.auditResult.errorPrefix}: ${detail}`
      : messages.common.backend.auditResult.errorPrefix;
  }
  return result;
}

export function translateRole(messages: Messages, role: string | undefined) {
  switch (role) {
    case 'owner':
      return messages.sessions.roles.owner;
    case 'follower':
      return messages.sessions.roles.follower;
    case undefined:
    case '':
      return '-';
    default:
      return role;
  }
}

export function translateProjectState(messages: Messages, state: string | undefined) {
  switch (state) {
    case 'initializing':
      return messages.sessions.states.initializing;
    case 'syncing':
      return messages.sessions.states.syncing;
    case 'active':
      return messages.sessions.states.active;
    case 'needs-confirmation':
      return messages.sessions.states.needsConfirmation;
    case 'terminating':
      return messages.sessions.states.terminating;
    case 'terminated':
      return messages.sessions.states.terminated;
    case undefined:
    case '':
      return messages.sessions.liveState;
    default:
      return state;
  }
}

export function translateUserStatus(messages: Messages, disabled: boolean) {
  return disabled ? messages.users.status.disabled : messages.users.status.active;
}
