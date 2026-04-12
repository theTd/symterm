import type { Locale } from './locales';
import { enMessages } from './messages.en';
import { zhCNMessages } from './messages.zh-CN';

export type Messages = typeof enMessages;

export const messagesByLocale: Record<Locale, Messages> = {
  en: enMessages,
  'zh-CN': zhCNMessages,
};
