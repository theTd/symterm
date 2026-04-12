import { useQuery } from '@tanstack/react-query';
import { Notice } from '../components/StateView';
import { DefinitionList, PageIntro, SectionCard } from '../components/AdminPrimitives';
import { Badge } from '../components/ui/badge';
import { useI18n } from '../i18n';
import { adminAPI } from '../lib/api';

export function SystemPage() {
  const { messages, formatDateTime } = useI18n();
  const bootstrap = useQuery({
    queryKey: ['bootstrap'],
    queryFn: adminAPI.bootstrap,
  });

  if (bootstrap.isLoading) {
    return (
      <div className="space-y-5">
        <PageIntro eyebrow={messages.frame.nav.system} title={messages.system.title} description={messages.system.description} />
        <SectionCard title={messages.system.title} description={messages.system.description}>
          <div className="text-sm text-[var(--muted-foreground)]">{messages.system.loading}</div>
        </SectionCard>
      </div>
    );
  }
  if (bootstrap.isError || !bootstrap.data) {
    return (
      <Notice tone="error" title={messages.system.unableToLoadTitle}>
        {String(bootstrap.error)}
      </Notice>
    );
  }

  return (
    <div className="space-y-5">
      <PageIntro
        eyebrow={messages.frame.nav.system}
        title={messages.system.title}
        description={messages.system.description}
      />
      <SectionCard
        title={messages.system.title}
        description={messages.system.description}
        actions={<Badge variant="neutral">{bootstrap.data.daemon.version || messages.system.dev}</Badge>}
      >
        <DefinitionList
          items={[
            { label: messages.system.fields.version, value: bootstrap.data.daemon.version || messages.system.dev },
            { label: messages.system.fields.started, value: formatDateTime(bootstrap.data.daemon.started_at) },
            { label: messages.system.fields.listen, value: bootstrap.data.daemon.listen_addr || '-' },
            { label: messages.system.fields.adminSocket, value: bootstrap.data.daemon.admin_socket_path || '-' },
            { label: messages.system.fields.adminWeb, value: bootstrap.data.daemon.admin_web_addr || '-' },
          ]}
        />
      </SectionCard>
    </div>
  );
}
