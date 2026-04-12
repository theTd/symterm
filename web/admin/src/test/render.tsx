import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { I18nProvider, type Locale } from '../i18n';

export function renderRoute(element: ReactElement, options: { path: string; route: string; locale?: Locale }) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
      mutations: {
        retry: false,
      },
    },
  });

  return render(
    <I18nProvider initialLocale={options.locale ?? 'en'}>
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[options.route]}>
          <Routes>
            <Route path={options.path} element={element} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>,
  );
}
