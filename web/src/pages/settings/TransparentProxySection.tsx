import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { Field, Label } from '../../components/ui/fieldset';
import { Input } from '../../components/ui/input';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface ProxySettings {
  spoof_enabled: boolean;
  spoof_scope: string;
  spoof_server_ip: string;
  spoof_allow_cidr: string;
  sni_forward_enabled: boolean;
  quic_forward_enabled: boolean;
  panel_backend_tcp: string;
  panel_backend_udp: string;
  server_ip_effective: string;
  server_ip_autodetected: string;
  restart_required?: boolean;
}

export function TransparentProxySection() {
  const { t } = useTranslation();
  const [cfg, setCfg] = useState<ProxySettings | null>(null);
  const [draft, setDraft] = useState<ProxySettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [restartNeeded, setRestartNeeded] = useState(false);
  const [advanced, setAdvanced] = useState(false);

  async function refresh() {
    try {
      const value = await api.get<ProxySettings>('/api/v1/settings/frontdoor/proxy');
      setCfg(value);
      setDraft(value);
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void refresh(); }, []);

  async function runPreflight() {
    setBanner(t('settings.proxyPreflightRunning'));
    try {
      const result = await api.post<{ ok: boolean; server_ip_autodetected?: string; error?: string }>('/api/v1/settings/frontdoor/proxy/preflight', {});
      if (result.ok) {
        setBanner(t('settings.proxyPreflightPass', { ip: result.server_ip_autodetected }));
        if (draft) setDraft({ ...draft, server_ip_autodetected: result.server_ip_autodetected || '' });
      } else {
        setBanner(t('settings.proxyPreflightFail', { err: result.error || 'unknown' }));
      }
    } catch (error) {
      setBanner(error instanceof Error ? error.message : String(error));
    }
  }

  async function save() {
    if (!draft) return;
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const result = await api.post<ProxySettings>('/api/v1/settings/frontdoor/proxy', {
        spoof_enabled: draft.spoof_enabled,
        spoof_scope: draft.spoof_scope,
        spoof_server_ip: draft.spoof_server_ip,
        spoof_allow_cidr: draft.spoof_allow_cidr,
        sni_forward_enabled: draft.sni_forward_enabled,
        quic_forward_enabled: draft.quic_forward_enabled,
        panel_backend_tcp: draft.panel_backend_tcp,
        panel_backend_udp: draft.panel_backend_udp,
      });
      setCfg(result);
      setDraft(result);
      setRestartNeeded(Boolean(result.restart_required));
      setBanner(result.restart_required ? t('settings.proxyRestartRequired') : t('settings.proxySaved'));
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  }

  const dirty = Boolean(cfg && draft && (
    cfg.spoof_enabled !== draft.spoof_enabled
    || cfg.spoof_scope !== draft.spoof_scope
    || cfg.spoof_server_ip !== draft.spoof_server_ip
    || cfg.spoof_allow_cidr !== draft.spoof_allow_cidr
    || cfg.sni_forward_enabled !== draft.sni_forward_enabled
    || cfg.quic_forward_enabled !== draft.quic_forward_enabled
    || cfg.panel_backend_tcp !== draft.panel_backend_tcp
    || cfg.panel_backend_udp !== draft.panel_backend_udp
  ));

  function Toggle({ field, label, hint }: { field: 'spoof_enabled' | 'sni_forward_enabled' | 'quic_forward_enabled'; label: string; hint: string }) {
    if (!draft) return null;
    return (
      <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
        <div className="flex items-center gap-3">
          <input aria-label={label} type="checkbox" checked={draft[field]} onChange={(event) => setDraft({ ...draft, [field]: event.target.checked })} className="h-4 w-4" />
          <div><div className="text-sm font-medium">{label}</div><div className="text-xs text-zinc-500">{hint}</div></div>
        </div>
      </div>
    );
  }

  return (
    <Section tint="rgb(59 130 246 / 0.35)" title={t('settings.proxyTitle')} description={t('settings.proxyDescription')}>
      {loading && <Text>{t('common.loading')}</Text>}
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.operationFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {banner && <div className="mb-3 rounded-lg border border-blue-500/40 bg-blue-500/10 px-3 py-2 text-sm text-blue-900 dark:text-blue-200">{banner}</div>}
      {restartNeeded && <div className="mb-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">{t('settings.proxyRestartHint')}</div>}
      {!loading && draft && (
        <div className="space-y-4">
          <Toggle field="spoof_enabled" label={t('settings.proxySpoofToggle')} hint={t('settings.proxySpoofHint')} />
          <Toggle field="sni_forward_enabled" label={t('settings.proxySNIToggle')} hint={t('settings.proxySNIHint')} />
          <Toggle field="quic_forward_enabled" label={t('settings.proxyQUICToggle')} hint={t('settings.proxyQUICHint')} />
          <Field>
            <Label>{t('settings.proxyServerIPLabel')}</Label>
            <Input value={draft.spoof_server_ip} placeholder={draft.server_ip_autodetected || 'auto'} onChange={(event) => setDraft({ ...draft, spoof_server_ip: event.target.value })} />
            <div className="mt-1 flex items-center gap-3 text-xs text-zinc-500"><span>{t('settings.proxyServerIPHint', { ip: draft.server_ip_effective || '(none)' })}</span><button type="button" onClick={runPreflight} className="text-blue-600 hover:underline dark:text-blue-400">{t('settings.proxyRunPreflight')}</button></div>
          </Field>
          <Field><Label>{t('settings.proxyScopeLabel')}</Label><select value={draft.spoof_scope} onChange={(event) => setDraft({ ...draft, spoof_scope: event.target.value })} className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 text-sm dark:border-white/10 dark:bg-white/5"><option value="all">{t('settings.proxyScopeAll')}</option><option value="private_only">{t('settings.proxyScopePrivate')}</option></select></Field>
          {draft.spoof_scope === 'private_only' && <Field><Label>{t('settings.proxyAllowCIDRLabel')}</Label><Input value={draft.spoof_allow_cidr} placeholder="172.22.0.0/16" onChange={(event) => setDraft({ ...draft, spoof_allow_cidr: event.target.value })} /></Field>}
          <button type="button" onClick={() => setAdvanced(!advanced)} className="text-xs text-zinc-500 hover:text-zinc-700 dark:hover:text-zinc-300">{advanced ? t('settings.proxyHideAdvanced') : t('settings.proxyShowAdvanced')}</button>
          {advanced && <div className="space-y-3"><Field><Label>{t('settings.proxyPanelTCPLabel')}</Label><Input value={draft.panel_backend_tcp} placeholder="127.0.0.1:8444" onChange={(event) => setDraft({ ...draft, panel_backend_tcp: event.target.value })} /></Field><Field><Label>{t('settings.proxyPanelUDPLabel')}</Label><Input value={draft.panel_backend_udp} placeholder="127.0.0.1:8445" onChange={(event) => setDraft({ ...draft, panel_backend_udp: event.target.value })} /></Field></div>}
          <Button color="indigo" disabled={saving || !dirty} onClick={save}>{saving ? t('settings.submitting') : t('settings.proxySave')}</Button>
        </div>
      )}
    </Section>
  );
}
