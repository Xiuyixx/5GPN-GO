import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '../../components/ui/badge';
import { Button } from '../../components/ui/button';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface TgbotStatus {
  enabled: boolean;
  admin_count: number;
  token_masked?: string;
}

export function TgbotSection() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<TgbotStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [token, setToken] = useState('');
  const [adminIds, setAdminIds] = useState('');
  const [saving, setSaving] = useState(false);
  const [savedMsg, setSavedMsg] = useState<string | null>(null);

  async function refresh() {
    setErr(null);
    try {
      setStatus(await api.get<TgbotStatus>('/api/v1/settings/tgbot'));
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void refresh(); }, []);

  function parseAdmins(raw: string): number[] {
    return raw.split(/[\s,]+/).map(Number).filter(Number.isSafeInteger);
  }

  async function save(action: 'enable' | 'disable') {
    setSaving(true);
    setSavedMsg(null);
    setErr(null);
    try {
      const body = action === 'disable'
        ? { token: '', admin_chat_ids: [] as number[] }
        : { token: token.trim(), admin_chat_ids: parseAdmins(adminIds) };
      if (action === 'enable' && !body.token) throw new Error(t('settings.tokenRequired'));
      if (action === 'enable' && body.admin_chat_ids.length === 0) throw new Error(t('settings.adminRequired'));
      setStatus(await api.post<TgbotStatus>('/api/v1/settings/tgbot', body));
      setSavedMsg(action === 'disable' ? t('settings.botStopped') : t('settings.botRestarted'));
      if (action === 'disable') { setToken(''); setAdminIds(''); }
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Section tint="rgb(20 184 166 / 0.35)" title={t('settings.tgbotTitle')} description={t('settings.tgbotDescription')}>
      {loading && <Text>{t('common.loading')}</Text>}
      {!loading && (
        <>
          <dl className="mb-4 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">{t('settings.currentStatus')}</dt>
            <dd><Badge color={status?.enabled ? 'lime' : 'zinc'}>{status?.enabled ? t('settings.statusRunning') : t('settings.statusDisabled')}</Badge></dd>
            <dt className="text-zinc-500">{t('settings.adminCount')}</dt>
            <dd className="font-mono">{status?.admin_count ?? 0}</dd>
            {status?.token_masked && <><dt className="text-zinc-500">{t('settings.tokenLabel')}</dt><dd className="font-mono">{status.token_masked}</dd></>}
          </dl>
          <div className="space-y-3">
            <div>
              <label htmlFor="tgbot-token" className="text-xs font-medium uppercase tracking-wide text-zinc-500">{t('settings.botTokenLabel')}</label>
              <input id="tgbot-token" type="password" className="mt-1 block w-full rounded-lg border border-zinc-300 bg-white/70 px-3 py-2 text-sm shadow-sm outline-none focus:ring-2 focus:ring-indigo-500 dark:border-zinc-700 dark:bg-zinc-900/60" value={token} onChange={(event) => setToken(event.target.value)} placeholder={status?.token_masked ? t('settings.currentPlaceholder', { value: status.token_masked }) : '123456:AA...'} autoComplete="off" />
              <p className="mt-1 text-xs text-zinc-500">{t('settings.botTokenHelp')}</p>
            </div>
            <div>
              <label htmlFor="tgbot-admins" className="text-xs font-medium uppercase tracking-wide text-zinc-500">{t('settings.adminChatIdsLabel')}</label>
              <input id="tgbot-admins" className="mt-1 block w-full rounded-lg border border-zinc-300 bg-white/70 px-3 py-2 text-sm shadow-sm outline-none focus:ring-2 focus:ring-indigo-500 dark:border-zinc-700 dark:bg-zinc-900/60" value={adminIds} onChange={(event) => setAdminIds(event.target.value)} placeholder="123456789 987654321" />
              <p className="mt-1 text-xs text-zinc-500">{t('settings.adminChatIdsHelpPrefix')}<span className="font-mono">/id</span>{t('settings.adminChatIdsHelpSuffix')}</p>
            </div>
            {err && <div className="rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-sm text-red-800 dark:text-red-200">{err}</div>}
            {savedMsg && <div className="rounded-lg border border-emerald-400/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-800 dark:text-emerald-200">{savedMsg}</div>}
            <div className="flex gap-2 pt-1">
              <Button color="indigo" onClick={() => save('enable')} disabled={saving}>{saving ? t('settings.saving') : (status?.enabled ? t('settings.restartBot') : t('settings.startBot'))}</Button>
              {status?.enabled && <Button plain onClick={() => save('disable')} disabled={saving}>{t('settings.disable')}</Button>}
              <Button plain onClick={refresh} disabled={saving}>{t('settings.refresh')}</Button>
            </div>
          </div>
        </>
      )}
    </Section>
  );
}
