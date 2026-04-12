import { supportedLocales, useI18n, type Locale } from '../i18n';
import { cn } from '../lib/utils';

export function LanguageSwitcher(props: { compact?: boolean }) {
  const { locale, setLocale, messages } = useI18n();

  const labelFor = (option: Locale) => (option === 'en' ? messages.language.en : messages.language.zhCN);

  return (
    <div
      className={cn(
        'inline-flex items-center gap-1 rounded-full border border-[color:var(--border-subtle)] bg-[var(--surface-muted)] p-1',
        props.compact && 'scale-95',
      )}
      aria-label={messages.language.label}
      role="group"
    >
      {supportedLocales.map((option) => {
        const active = option === locale;
        return (
          <button
            key={option}
            type="button"
            aria-pressed={active}
            className={cn(
              'rounded-full px-3 py-1.5 text-xs font-semibold tracking-[0.16em] uppercase transition-colors',
              active
                ? 'bg-[var(--foreground)] text-[var(--surface-canvas)]'
                : 'text-[var(--muted-foreground)] hover:text-[var(--foreground)]',
            )}
            onClick={() => setLocale(option)}
          >
            {labelFor(option)}
          </button>
        );
      })}
    </div>
  );
}
