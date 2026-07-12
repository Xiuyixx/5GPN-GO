import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, AlertActions, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { Field, Label } from '../../components/ui/fieldset';
import { Text } from '../../components/ui/text';
import { api } from '../../api/client';
import { Section } from './Section';

interface InternalOnlySettings {
  enabled: boolean;
  cidrs: string;
}

export function InternalOnlySection() {
  const { t } = useTranslation();
  const [cfg, setCfg] = useState<InternalOnlySettings | null>(null);
  const [draft, setDraft] = useState<InternalOnlySettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [confirming, setConfirming] = useState(false);

  async function refresh() {
    try {
      const value = await api.get<InternalOnlySettings>('/api/v1/settings/frontdoor/internal-only');
      setCfg(value);
      setDraft(value);
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void refresh(); }, []);

  async function save() {
    if (!draft) return;
    setConfirming(false);
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const result = await api.post<InternalOnlySettings>('/api/v1/settings/frontdoor/internal-only', draft);
      setCfg(result);
      setDraft(result);
      setBanner(t('settings.internalOnlySaved'));
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  }

  const dirty = Boolean(cfg && draft && (cfg.enabled !== draft.enabled || cfg.cidrs !== draft.cidrs));
  function requestSave() {
    if (cfg && draft && !cfg.enabled && draft.enabled) setConfirming(true);
    else void save();
  }

  return (
    <Section tint="rgb(16 185 129 / 0.35)" title={t('settings.internalOnlyTitle')} description={t('settings.internalOnlyDescription')}>
      {loading && <Text>{t('common.loading')}</Text>}
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.operationFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      {banner && <div className="mb-3 rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-900 dark:text-emerald-200">{banner}</div>}
      {!loading && draft && (
        <div className="space-y-4">
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <div className="flex items-center gap-3">
              <input aria-label={t('settings.internalOnlyEnabledLabel')} type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} className="h-4 w-4" />
              <div><div className="text-sm font-medium">{t('settings.internalOnlyEnabledLabel')}</div><div className="text-xs text-zinc-500">{t('settings.internalOnlyEnabledHint')}</div></div>
            </div>
          </div>
          <Field>
            <Label>{t('settings.internalOnlyCIDRLabel')}</Label>
            <textarea value={draft.cidrs} placeholder={t('settings.internalOnlyCIDRPlaceholder')} onChange={(event) => setDraft({ ...draft, cidrs: event.target.value })} rows={3} className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 font-mono text-xs dark:border-white/10 dark:bg-white/5" />
            <div className="mt-1 text-xs text-zinc-500">{t('settings.internalOnlyCIDRHint')}</div>
          </Field>
          <Button color="indigo" disabled={saving || !dirty} onClick={requestSave}>{saving ? t('settings.submitting') : t('settings.internalOnlySave')}</Button>
        </div>
      )}
      <Alert open={confirming} onClose={() => setConfirming(false)}>
        <AlertTitle>{t('settings.internalOnlyConfirmTitle')}</AlertTitle>
        <AlertBody>{t('settings.internalOnlyConfirmBody')}</AlertBody>
        <AlertActions><Button plain onClick={() => setConfirming(false)}>{t('common.cancel')}</Button><Button color="red" onClick={save}>{t('settings.internalOnlyConfirm')}</Button></AlertActions>
      </Alert>
    </Section>
  );
}
