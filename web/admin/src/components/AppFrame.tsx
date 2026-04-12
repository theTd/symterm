import { useState } from 'react';
import { Activity, FileSearch, LayoutDashboard, Menu, ServerCog, ShieldUser, type LucideIcon } from 'lucide-react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { LanguageSwitcher } from './LanguageSwitcher';
import { Badge } from './ui/badge';
import { Button } from './ui/button';
import { Card, CardContent } from './ui/card';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from './ui/sheet';
import { translateConnectionState, useI18n, type ConnectionState } from '../i18n';
import { cn } from '../lib/utils';

type NavItem = {
  to: string;
  label: string;
  caption: string;
  icon: LucideIcon;
};

function connectionVariant(state: ConnectionState) {
  switch (state) {
    case 'live':
      return 'success';
    case 'resync':
      return 'warning';
    case 'offline':
      return 'danger';
    default:
      return 'info';
  }
}

function frameTone(state: ConnectionState) {
  if (state === 'live') {
    return 'border-[color:var(--border-subtle)] bg-[var(--surface-panel)]';
  }
  if (state === 'offline') {
    return 'border-[color:var(--danger)]/35 bg-[var(--danger)]/10';
  }
  if (state === 'resync') {
    return 'border-[color:var(--warning)]/35 bg-[var(--warning)]/10';
  }
  return 'border-[color:var(--info)]/30 bg-[var(--info)]/10';
}

function buildNav(messages: ReturnType<typeof useI18n>['messages']): NavItem[] {
  return [
    {
      to: '/overview',
      label: messages.frame.nav.overview,
      caption: messages.overview.recentEvents,
      icon: LayoutDashboard,
    },
    {
      to: '/sessions',
      label: messages.frame.nav.sessions,
      caption: messages.sessions.description,
      icon: Activity,
    },
    {
      to: '/users',
      label: messages.frame.nav.users,
      caption: messages.users.description,
      icon: ShieldUser,
    },
    {
      to: '/audit',
      label: messages.frame.nav.audit,
      caption: messages.audit.description,
      icon: FileSearch,
    },
    {
      to: '/system',
      label: messages.frame.nav.system,
      caption: messages.system.description,
      icon: ServerCog,
    },
  ];
}

function NavItems(props: { items: NavItem[]; onNavigate?: () => void }) {
  return (
    <nav className="grid gap-2">
      {props.items.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          onClick={props.onNavigate}
          className={({ isActive }) =>
            cn(
              'group rounded-[24px] border px-4 py-4 transition-all',
              isActive
                ? 'border-[color:var(--accent)]/30 bg-[linear-gradient(135deg,rgba(108,212,171,0.16),rgba(108,212,171,0.05))] text-[var(--foreground)] shadow-[0_18px_40px_rgba(8,18,26,0.3)]'
                : 'border-transparent bg-transparent text-[var(--muted-foreground)] hover:border-[color:var(--border-subtle)] hover:bg-white/[0.03] hover:text-[var(--foreground)]',
            )
          }
        >
          {({ isActive }) => (
            <div className="flex items-start gap-3">
              <div
                className={cn(
                  'mt-0.5 flex size-10 shrink-0 items-center justify-center rounded-2xl border transition-colors',
                  isActive
                    ? 'border-[color:var(--accent)]/35 bg-[var(--accent)]/18 text-[var(--accent)]'
                    : 'border-[color:var(--border-subtle)] bg-[var(--surface-muted)] text-[var(--subtle-foreground)] group-hover:text-[var(--foreground)]',
                )}
              >
                <item.icon className="size-4" />
              </div>
              <div className="min-w-0">
                <div className="text-sm font-semibold tracking-[0.01em]">{item.label}</div>
                <div className="mt-1 text-xs leading-5 text-[var(--subtle-foreground)]">{item.caption}</div>
              </div>
            </div>
          )}
        </NavLink>
      ))}
    </nav>
  );
}

export function AppFrame(props: {
  actor?: string;
  connection: ConnectionState;
  connectionDetail?: string;
}) {
  const { messages } = useI18n();
  const location = useLocation();
  const [navOpen, setNavOpen] = useState(false);
  const navItems = buildNav(messages);
  const current = navItems.find((item) => location.pathname === item.to || location.pathname.startsWith(`${item.to}/`)) ?? navItems[0];

  return (
    <div className="relative min-h-screen overflow-hidden">
      <div className="pointer-events-none absolute inset-0 bg-grid opacity-30" />
      <div className="relative mx-auto flex min-h-screen max-w-[1600px] gap-5 px-4 py-4 sm:px-6 lg:px-8">
        <aside className="sticky top-4 hidden h-[calc(100vh-2rem)] w-[300px] shrink-0 lg:block">
          <Card className="surface-ring flex h-full flex-col overflow-hidden border-[color:var(--border-strong)] bg-[var(--surface-shell)]">
            <CardContent className="flex h-full flex-col gap-8 p-6">
              <div className="space-y-4">
                <div className="inline-flex rounded-full border border-[color:var(--accent)]/25 bg-[var(--accent)]/12 px-3 py-1 text-[0.68rem] font-semibold uppercase tracking-[0.28em] text-[var(--accent)]">
                  symterm
                </div>
                <div className="space-y-3">
                  <h1 className="max-w-[12ch] text-[2rem] font-semibold leading-none tracking-[-0.04em] text-balance">
                    {messages.frame.title}
                  </h1>
                  <p className="max-w-[26ch] text-sm leading-6 text-[var(--muted-foreground)]">{messages.frame.description}</p>
                </div>
              </div>
              <NavItems items={navItems} />
              <div className="mt-auto space-y-4">
                <Card className="border-[color:var(--border-subtle)] bg-[var(--surface-card)]/80">
                  <CardContent className="space-y-4 p-4">
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div className="text-[0.7rem] font-semibold uppercase tracking-[0.24em] text-[var(--subtle-foreground)]">
                          {messages.frame.title}
                        </div>
                        <div className="mt-2 text-sm text-[var(--muted-foreground)]">
                          {props.actor || messages.common.anonymous}
                        </div>
                      </div>
                      <Badge variant={connectionVariant(props.connection)}>{translateConnectionState(messages, props.connection)}</Badge>
                    </div>
                    <p className="text-xs leading-5 text-[var(--subtle-foreground)]">
                      {props.connection === 'live' ? current.caption : props.connectionDetail || messages.frame.retrying}
                    </p>
                  </CardContent>
                </Card>
                <LanguageSwitcher />
              </div>
            </CardContent>
          </Card>
        </aside>
        <div className="flex min-w-0 flex-1 flex-col gap-5">
          <header className="sticky top-4 z-30">
            <Card className="surface-ring border-[color:var(--border-strong)] bg-[var(--surface-shell)]">
              <CardContent className="flex items-center gap-3 p-3 sm:p-4">
                <div className="lg:hidden">
                  <Sheet open={navOpen} onOpenChange={setNavOpen}>
                    <SheetTrigger asChild>
                      <Button variant="ghost" size="icon" aria-label="Open navigation">
                        <Menu className="size-5" />
                      </Button>
                    </SheetTrigger>
                    <SheetContent side="left" className="bg-[var(--surface-shell)]">
                      <SheetHeader className="pr-10">
                        <SheetTitle>{messages.frame.title}</SheetTitle>
                        <SheetDescription>{messages.frame.description}</SheetDescription>
                      </SheetHeader>
                      <div className="mt-6 space-y-6">
                        <NavItems items={navItems} onNavigate={() => setNavOpen(false)} />
                        <LanguageSwitcher />
                      </div>
                    </SheetContent>
                  </Sheet>
                </div>
                <div className="min-w-0 flex-1">
                  <div className="text-[0.68rem] font-semibold uppercase tracking-[0.24em] text-[var(--subtle-foreground)]">symterm</div>
                  <div className="mt-2 flex flex-wrap items-center gap-3">
                    <h2 className="truncate text-xl font-semibold tracking-[-0.03em] text-[var(--foreground)]">{current.label}</h2>
                    <Badge variant="neutral" className="hidden sm:inline-flex">
                      {props.actor || messages.common.anonymous}
                    </Badge>
                  </div>
                </div>
                <div className="hidden items-center gap-3 sm:flex">
                  <Badge variant={connectionVariant(props.connection)}>{translateConnectionState(messages, props.connection)}</Badge>
                  <div className="hidden xl:block">
                    <LanguageSwitcher compact />
                  </div>
                </div>
              </CardContent>
            </Card>
          </header>
          {props.connection !== 'live' ? (
            <Card className={cn('surface-ring border', frameTone(props.connection))}>
              <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="space-y-1">
                  <div className="text-sm font-semibold">{messages.frame.realtimeAttention}</div>
                  <p className="text-sm text-[var(--muted-foreground)]">{props.connectionDetail || messages.frame.retrying}</p>
                </div>
                <Badge variant={connectionVariant(props.connection)}>{translateConnectionState(messages, props.connection)}</Badge>
              </CardContent>
            </Card>
          ) : null}
          <main className="pb-10">
            <Outlet />
          </main>
        </div>
      </div>
    </div>
  );
}
