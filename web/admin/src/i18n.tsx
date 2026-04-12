/* eslint-disable react-refresh/only-export-components */

export { supportedLocales, type ConnectionState, type Locale } from './i18n/locales';
export { type Messages, messagesByLocale } from './i18n/message-catalog';
export { I18nProvider, useI18n } from './i18n/runtime';
export {
  translateAuditAction,
  translateAuditResult,
  translateConnectionState,
  translateProjectState,
  translateRole,
  translateTokenSource,
  translateUserStatus,
} from './i18n/translators';
