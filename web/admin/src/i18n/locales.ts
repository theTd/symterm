export const LOCALE_STORAGE_KEY = 'symterm-admin-locale';

export const supportedLocales = ['en', 'zh-CN'] as const;
export type Locale = (typeof supportedLocales)[number];
export type ConnectionState = 'connecting' | 'live' | 'offline' | 'resync';
