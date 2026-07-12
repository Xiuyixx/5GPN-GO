import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { QRCodeSVG } from 'qrcode.react';
import { Alert, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface IOSProfile {
  url: string;
  port?: number;
  uuid?: string;
}

export function IOSSection() {
  const { t } = useTranslation();
  const [profile, setProfile] = useState<IOSProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [preflightRunning, setPreflightRunning] = useState(false);
  const [preflightResult, setPreflightResult] = useState<{ ok: boolean; message: string } | null>(null);
  const [toggling, setToggling] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const value = await api.get<IOSProfile>('/api/v1/ios/profile-url');
        if (!cancelled) setProfile(value);
      } catch (error) {
        if (!cancelled) setErr(error instanceof Error ? error.message : String(error));
      } finally {
        if (!cancelled) setLoading(false);
      }
      try {
        const settings = await api.get<{ ios?: { profile_enabled?: boolean } }>('/api/v1/settings/panel');
        if (!cancelled) setEnabled(Boolean(settings.ios?.profile_enabled));
      } catch {
        // The profile link remains useful when the optional toggle cannot be loaded.
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function runPreflight() {
    setPreflightRunning(true);
    setPreflightResult(null);
    try {
      const result = await api.post<{ ok: boolean; error?: string }>('/api/v1/settings/ios/preflight', {});
      setPreflightResult({
        ok: result.ok,
        message: result.ok
          ? t('settings.iosPreflightPass')
          : t('settings.iosPreflightFail', { err: result.error || 'unknown' }),
      });
    } catch (error) {
      setPreflightResult({ ok: false, message: error instanceof Error ? error.message : String(error) });
    } finally {
      setPreflightRunning(false);
    }
  }

  async function toggleEnabled(next: boolean) {
    setToggling(true);
    try {
      await api.post('/api/v1/settings/ios/profile-enabled', { enabled: next });
      setEnabled(next);
      if (next) setPreflightResult({ ok: true, message: t('settings.iosProfileTurnedOn') });
    } catch (error) {
      setPreflightResult({ ok: false, message: error instanceof Error ? error.message : String(error) });
    } finally {
      setToggling(false);
    }
  }

  async function copy() {
    if (!profile?.url) return;
    try {
      await navigator.clipboard.writeText(profile.url);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  function downloadHref(url: string): string {
    if (/\.mobileconfig(\?|$)/.test(url)) return url;
    return url.replace(/\/$/, '') + '/ios-dot.mobileconfig';
  }

  return (
    <Section tint="rgb(168 85 247 / 0.35)" title={t('settings.iosTitle')} description={t('settings.iosDescription')}>
      {loading && <Text>{t('common.loading')}</Text>}
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.iosProfileError')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {!loading && (
        <div className="mb-4 rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
          <div className="mb-2 flex items-center justify-between gap-3">
            <div>
              <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{t('settings.iosProfileToggleTitle')}</div>
              <div className="text-xs text-zinc-500">{enabled ? t('settings.iosProfileEnabledHint') : t('settings.iosProfileDisabledHint')}</div>
            </div>
            <Button color={enabled ? 'zinc' : 'indigo'} onClick={() => toggleEnabled(!enabled)} disabled={toggling}>
              {toggling ? t('common.loading') : enabled ? t('settings.iosDisable') : t('settings.iosEnable')}
            </Button>
          </div>
          <div className="mt-3 flex items-center gap-2">
            <Button plain onClick={runPreflight} disabled={preflightRunning}>{preflightRunning ? t('settings.iosPreflightRunning') : t('settings.iosPreflightRun')}</Button>
            {preflightResult && <span className={`text-xs ${preflightResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>{preflightResult.message}</span>}
          </div>
        </div>
      )}
      {profile?.url && (
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
          <div className="rounded-xl bg-white p-3 shadow-sm ring-1 ring-zinc-200 dark:ring-white/10"><QRCodeSVG value={profile.url} size={160} level="M" /></div>
          <div className="flex-1 space-y-3">
            <div className="break-all rounded-lg bg-zinc-500/5 px-3 py-2 font-mono text-xs text-zinc-800 dark:text-zinc-200">{profile.url}</div>
            <div className="flex flex-wrap gap-2">
              <Button color="indigo" onClick={copy}>{copied ? t('settings.copied') : t('settings.copyLink')}</Button>
              <Button href={downloadHref(profile.url)} color="zinc">{t('settings.downloadProfile')}</Button>
            </div>
            {profile.uuid && <div className="text-xs text-zinc-500">UUID <span className="font-mono">{profile.uuid}</span>{profile.port ? <> · {t('settings.port')} <span className="font-mono">{profile.port}</span></> : null}</div>}
          </div>
        </div>
      )}
      {!loading && !profile?.url && !err && <div className="text-sm text-zinc-500">{t('settings.iosNoProfile')}</div>}
    </Section>
  );
}
