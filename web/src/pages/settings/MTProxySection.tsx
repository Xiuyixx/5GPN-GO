import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { QRCodeSVG } from 'qrcode.react';
import { Alert, AlertActions, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { Field, Label } from '../../components/ui/fieldset';
import { Input } from '../../components/ui/input';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface MTProxySettings {
  enabled: boolean;
  listen: string;
  secret_configured: boolean;
  fronting_domain: string;
  service_status: string;
}

interface PanelDomain {
  server?: { domain?: string };
}

export function MTProxySection() {
  const { t } = useTranslation();
  const [cfg, setCfg] = useState<MTProxySettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);
  const [draftEnabled, setDraftEnabled] = useState(false);
  const [draftDomain, setDraftDomain] = useState('www.cloudflare.com');
  const [freshSecret, setFreshSecret] = useState<string | null>(null);
  const [freshLink, setFreshLink] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [domain, setDomain] = useState('');
  const [confirmRotate, setConfirmRotate] = useState(false);

  async function refresh() {
    setErr(null);
    try {
      const value = await api.get<MTProxySettings>('/api/v1/settings/mtproxy');
      setCfg(value);
      setDraftEnabled(value.enabled);
      setDraftDomain(value.fronting_domain || 'www.cloudflare.com');
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
    void api.get<PanelDomain>('/api/v1/settings/panel')
      .then((value) => setDomain(value.server?.domain?.trim() || ''))
      .catch(() => undefined);
  }, []);

  async function save() {
    if (!cfg) return;
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const body: { enabled: boolean; fronting_domain?: string } = { enabled: draftEnabled };
      if (draftDomain.trim() && draftDomain.trim() !== cfg.fronting_domain.trim()) {
        body.fronting_domain = draftDomain.trim();
      }
      const result = await api.post<MTProxySettings>('/api/v1/settings/mtproxy', body);
      setCfg(result);
      setDraftEnabled(result.enabled);
      setDraftDomain(result.fronting_domain || 'www.cloudflare.com');
      setBanner(t('settings.mtproxySaved'));
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  }

  async function generateSecret() {
    setConfirmRotate(false);
    setGenerating(true);
    setErr(null);
    setBanner(null);
    try {
      const result = await api.post<{ ok: boolean; secret: string; connect_link: string }>(
        '/api/v1/settings/mtproxy/generate-secret',
        { fronting_domain: draftDomain.trim() || 'www.cloudflare.com' },
      );
      if (!result.ok || !result.secret) throw new Error(t('settings.operationFailed'));
      setFreshSecret(result.secret);
      setFreshLink(result.connect_link || null);
      await refresh();
      setBanner(t('settings.mtproxyRestartHint'));
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setGenerating(false);
    }
  }

  function dirty(): boolean {
    return Boolean(cfg && (
      cfg.enabled !== draftEnabled
      || (draftDomain.trim() && draftDomain.trim() !== cfg.fronting_domain.trim())
    ));
  }

  function buildLinks(): { tg: string; https: string } | null {
    if (!freshSecret) return null;
    const listen = cfg?.listen || '';
    const index = listen.lastIndexOf(':');
    const port = index >= 0 ? listen.slice(index + 1) : '';
    const host = domain || (index > 0 ? listen.slice(0, index) : '') || 'YOUR_SERVER';
    const params = `server=${encodeURIComponent(host)}&port=${port}&secret=${freshSecret}`;
    return { tg: freshLink || `tg://proxy?${params}`, https: `https://t.me/proxy?${params}` };
  }

  async function copyToClipboard(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  const links = buildLinks();
  const notInstalled = cfg?.service_status === 'not-installed';

  return (
    <Section tint="rgb(56 189 248 / 0.35)" title={t('settings.mtproxyTitle')} description={t('settings.mtproxyDescription')}>
      {loading && <Text>{t('common.loading')}</Text>}
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.operationFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {banner && <div className="mb-3 rounded-lg border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-sm text-sky-900 dark:text-sky-200">{banner}</div>}
      {!loading && cfg && <div className="mb-3 rounded-lg border border-zinc-300/60 bg-zinc-50/70 px-3 py-2 text-xs text-zinc-600 dark:border-white/10 dark:bg-white/5 dark:text-zinc-300">{t('settings.mtproxyExternalNote')}</div>}
      {notInstalled && <div className="mb-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">{t('settings.mtproxyLoopbackFallback')}</div>}
      {!loading && cfg && (
        <div className="space-y-4">
          <div className="text-xs text-zinc-500">{t('settings.mtproxyServiceStatus', { status: cfg.service_status || 'unknown' })}</div>
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <div className="flex items-center gap-3">
              <input aria-label={t('settings.mtproxyEnabledLabel')} type="checkbox" checked={draftEnabled} disabled={!cfg.secret_configured} onChange={(event) => setDraftEnabled(event.target.checked)} className="h-4 w-4" />
              <div><div className="text-sm font-medium">{t('settings.mtproxyEnabledLabel')}</div><div className="text-xs text-zinc-500">{t('settings.mtproxyEnabledHint')}</div></div>
            </div>
          </div>
          <Field><Label>{t('settings.mtproxyListenLabel')}</Label><Input value={cfg.listen} readOnly /><div className="mt-1 text-xs text-zinc-500">{t('settings.mtproxyListenHint')}</div></Field>
          <Field><Label>{t('settings.mtproxyFrontingDomainLabel')}</Label><Input value={draftDomain} placeholder="www.cloudflare.com" onChange={(event) => setDraftDomain(event.target.value)} /><div className="mt-1 text-xs text-zinc-500">{t('settings.mtproxyFrontingDomainHint')}</div></Field>
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <div className="text-sm font-medium">{t('settings.mtproxySecretLabel')}</div>
            <div className="mt-2 flex items-center gap-3">
              <span className="font-mono text-sm">{cfg.secret_configured ? t('settings.mtproxySecretConfigured', { domain: cfg.fronting_domain || 'unknown' }) : t('settings.mtproxySecretNotConfigured')}</span>
              <Button plain onClick={() => cfg.secret_configured ? setConfirmRotate(true) : void generateSecret()} disabled={generating || notInstalled}>{generating ? t('settings.saving') : cfg.secret_configured ? t('settings.mtproxyRotateSecret') : t('settings.mtproxyGenerateSecret')}</Button>
            </div>
            {freshSecret && <div className="mt-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-200"><div className="mb-2 font-medium">{t('settings.mtproxySecretRevealWarning')}</div><div className="flex items-center gap-2"><code className="break-all rounded bg-white/60 px-2 py-1 font-mono text-[11px] text-amber-950 dark:bg-white/10 dark:text-amber-100">{freshSecret}</code><Button plain onClick={() => copyToClipboard(freshSecret)}>{copied ? t('settings.mtproxyCopied') : t('settings.mtproxyCopyLink')}</Button></div></div>}
          </div>
          {links && (
            <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
              <div className="text-sm font-medium">{t('settings.mtproxyClientLinkTitle')}</div><div className="mt-2 text-xs text-zinc-500">{t('settings.mtproxyQRHint')}</div>
              <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-start">
                <div className="rounded-lg bg-white p-2"><QRCodeSVG value={links.https} size={160} /></div>
                <div className="flex-1 space-y-2 text-xs">{([['tg://', links.tg], ['https://t.me/proxy', links.https]] as const).map(([label, value]) => <div key={label}><div className="text-zinc-500">{label}</div><div className="flex items-center gap-2"><code className="flex-1 break-all rounded bg-zinc-100 px-2 py-1 font-mono text-[11px] dark:bg-zinc-800">{value}</code><Button plain onClick={() => copyToClipboard(value)}>{copied ? t('settings.mtproxyCopied') : t('settings.mtproxyCopyLink')}</Button></div></div>)}</div>
              </div>
            </div>
          )}
          <div className="flex gap-2 pt-1"><Button color="indigo" onClick={save} disabled={saving || !dirty() || notInstalled}>{saving ? t('settings.saving') : t('settings.mtproxySave')}</Button><Button plain onClick={refresh} disabled={saving}>{t('settings.refresh')}</Button></div>
        </div>
      )}
      <Alert open={confirmRotate} onClose={() => setConfirmRotate(false)}>
        <AlertTitle>{t('settings.mtproxyRotateConfirmTitle')}</AlertTitle>
        <AlertBody>{t('settings.mtproxyRotateConfirmBody')}</AlertBody>
        <AlertActions><Button plain onClick={() => setConfirmRotate(false)}>{t('common.cancel')}</Button><Button color="red" onClick={generateSecret}>{t('settings.mtproxyRotateSecret')}</Button></AlertActions>
      </Alert>
    </Section>
  );
}
