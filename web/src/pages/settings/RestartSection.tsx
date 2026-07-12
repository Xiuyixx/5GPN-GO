import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, AlertActions, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { api, APIError } from '../../api/client';
import { Section } from './Section';

export function RestartSection() {
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function restart() {
    setConfirm(false);
    setErr(null);
    setBusy(true);
    setBanner(t('settings.restarting'));
    try {
      await api.post('/api/v1/system/restart');
      setBanner(t('settings.restartQueued'));
    } catch (error) {
      setErr(error instanceof APIError && error.status === 503
        ? t('settings.restartUnavailable')
        : error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section tint="rgb(244 63 94 / 0.35)" title={t('settings.restartTitle')} description={t('settings.restartDescription')}>
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.operationFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {banner && <div className="mb-3 rounded-lg border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">{banner}</div>}
      <Button color="red" disabled={busy} onClick={() => setConfirm(true)}>{busy ? t('settings.restarting') : t('settings.restartButton')}</Button>
      <Alert open={confirm} onClose={() => setConfirm(false)}>
        <AlertTitle>{t('settings.restartConfirmTitle')}</AlertTitle>
        <AlertBody>{t('settings.restartConfirmBody')}</AlertBody>
        <AlertActions><Button plain onClick={() => setConfirm(false)}>{t('common.cancel')}</Button><Button color="red" onClick={restart}>{t('settings.restartConfirm')}</Button></AlertActions>
      </Alert>
    </Section>
  );
}
