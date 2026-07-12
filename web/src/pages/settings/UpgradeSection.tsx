import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Badge } from '../../components/ui/badge';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface UpdateCheck {
  current: string;
  latest: string;
  has_update: boolean;
}

export function UpgradeSection() {
  const { t } = useTranslation();
  const [check, setCheck] = useState<UpdateCheck | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void api.get<UpdateCheck>('/api/v1/update/check')
      .then((value) => { if (!cancelled) setCheck(value); })
      .catch((error: unknown) => {
        if (!cancelled) setErr(error instanceof Error ? error.message : String(error));
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  return (
    <Section tint="rgb(99 102 241 / 0.35)" title={t('settings.upgradeTitle')} description={t('settings.upgradeDescription')}>
      {loading && <Text>{t('settings.checkingVersion')}</Text>}
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.operationFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {check && (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">{t('settings.currentVersion')}</dt>
            <dd className="font-mono">{check.current || t('settings.unknown')}</dd>
            <dt className="text-zinc-500">{t('settings.latestVersion')}</dt>
            <dd className="font-mono">
              {check.latest || t('settings.unknown')}
              <Badge color={check.has_update ? 'amber' : 'lime'} className="ml-2">
                {check.has_update ? t('settings.updateAvailable') : t('settings.upToDate')}
              </Badge>
            </dd>
          </dl>
          <div className="mt-4">
            <Text>{check.has_update ? t('settings.externalUpgradeRequired') : t('settings.alreadyLatest')}</Text>
          </div>
        </>
      )}
    </Section>
  );
}
