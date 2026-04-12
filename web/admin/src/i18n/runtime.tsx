/* eslint-disable react-refresh/only-export-components */

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';

import { messagesByLocale, type Messages } from './message-catalog';
import { LOCALE_STORAGE_KEY, type Locale } from './locales';

type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  messages: Messages;
  formatDateTime: (value: string | number | Date) => string;
};

const I18nContext = createContext<I18nContextValue | null>(null);

function normalizeLocale(locale: string | null | undefined): Locale | null {
  if (!locale) {
    return null;
  }
  const lower = locale.toLowerCase();
  if (lower.startsWith('zh')) {
    return 'zh-CN';
  }
  if (lower.startsWith('en')) {
    return 'en';
  }
  return null;
}

function detectInitialLocale(): Locale {
  if (typeof window === 'undefined') {
    return 'en';
  }
  const persisted = normalizeLocale(window.localStorage.getItem(LOCALE_STORAGE_KEY));
  if (persisted) {
    return persisted;
  }
  for (const locale of window.navigator.languages ?? []) {
    const normalized = normalizeLocale(locale);
    if (normalized) {
      return normalized;
    }
  }
  return normalizeLocale(window.navigator.language) ?? 'en';
}

export function I18nProvider(props: { children: ReactNode; initialLocale?: Locale }) {
  const [locale, setLocale] = useState<Locale>(() => props.initialLocale ?? detectInitialLocale());
  const messages = messagesByLocale[locale];

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    }
    document.documentElement.lang = locale;
    document.title = messages.app.documentTitle;
  }, [locale, messages.app.documentTitle]);

  return (
    <I18nContext.Provider
      value={{
        locale,
        setLocale,
        messages,
        formatDateTime: (value) => new Date(value).toLocaleString(locale),
      }}
    >
      {props.children}
    </I18nContext.Provider>
  );
}

export function useI18n() {
  const value = useContext(I18nContext);
  if (!value) {
    throw new Error('useI18n must be used within I18nProvider');
  }
  return value;
}
