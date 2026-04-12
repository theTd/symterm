export type ToneVariant = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info';

export function projectStateTone(state: string | undefined, closeReason?: string) {
  if (closeReason) {
    return 'neutral' satisfies ToneVariant;
  }
  switch (state) {
    case 'active':
      return 'success' satisfies ToneVariant;
    case 'needs-confirmation':
      return 'warning' satisfies ToneVariant;
    case 'terminating':
    case 'terminated':
      return 'neutral' satisfies ToneVariant;
    case 'syncing':
    case 'initializing':
      return 'info' satisfies ToneVariant;
    default:
      return 'neutral' satisfies ToneVariant;
  }
}

export function userStatusTone(disabled: boolean) {
  return disabled ? ('warning' satisfies ToneVariant) : ('success' satisfies ToneVariant);
}

export function auditResultTone(result: string | undefined) {
  if (!result) {
    return 'neutral' satisfies ToneVariant;
  }
  if (result === 'ok') {
    return 'success' satisfies ToneVariant;
  }
  if (result.startsWith('error:')) {
    return 'danger' satisfies ToneVariant;
  }
  return 'neutral' satisfies ToneVariant;
}

export function eventTone(kind: string | undefined) {
  if (!kind) {
    return 'neutral' satisfies ToneVariant;
  }
  if (kind.startsWith('audit_')) {
    return 'accent' satisfies ToneVariant;
  }
  if (kind.startsWith('session_')) {
    return 'info' satisfies ToneVariant;
  }
  if (kind.startsWith('user_') || kind.startsWith('token_')) {
    return 'warning' satisfies ToneVariant;
  }
  return 'neutral' satisfies ToneVariant;
}
