import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { QRCodeSVG } from 'qrcode.react';
import AppShell from '../layouts/AppShell';
import { Heading, Subheading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Input } from '../components/ui/input';
import { Field, Label, ErrorMessage } from '../components/ui/fieldset';
import { api, APIError } from '../api/client';
import { useAuthStore } from '../stores/auth';
import { useMeStore } from '../stores/me';

interface UpdateCheck {
  current: string;
  latest: string;
  has_update: boolean;
}

interface TgbotStatus {
  enabled: boolean;
  admin_count: number;
  token_masked?: string;
}

interface IOSProfile {
  url: string;
  port?: number;
  uuid?: string;
}

interface HealthPayload {
  version?: string;
  status?: string;
}

function Section({
  tint,
  title,
  description,
  children,
}: {
  tint: string;
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="glass tile relative overflow-hidden p-6">
      <div
        aria-hidden
        className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
        style={{ background: `radial-gradient(60% 100% at 30% 0%, ${tint}, transparent 70%)` }}
      />
      <div className="relative">
        <Subheading>{title}</Subheading>
        {description && <Text className="mt-2">{description}</Text>}
        <div className="mt-4">{children}</div>
      </div>
    </div>
  );
}

function UpgradeSection() {
  const { t } = useTranslation();
  const [check, setCheck] = useState<UpdateCheck | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const c = await api.get<UpdateCheck>('/api/v1/update/check');
        if (!cancelled) setCheck(c);
      } catch (e) {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function pollHealth(expected: string): Promise<boolean> {
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const h = await api.get<HealthPayload>('/api/v1/health');
        if (h?.version && h.version === expected) return true;
        if (h?.version && expected && h.version !== check?.current) return true;
      } catch {
        // /health may error transiently during restart — keep polling.
      }
    }
    return false;
  }

  async function apply() {
    if (!check?.has_update) return;
    setErr(null);
    setSuccess(null);
    setApplying(true);
    setBanner(t('settings.upgradeInProgress'));
    try {
      await api.post('/api/v1/update/apply');
      // Server responded before restart — attempt one health probe to refresh.
      const ok = await pollHealth(check.latest);
      if (ok) {
        setSuccess(t('settings.upgradedTo', { version: check.latest }));
        try {
          const c = await api.get<UpdateCheck>('/api/v1/update/check');
          setCheck(c);
        } catch { /* ignore */ }
      } else {
        setSuccess(t('settings.upgradeSubmittedRefresh'));
      }
    } catch (e) {
      // Connection drop during restart is expected → poll health.
      const isNetErr =
        e instanceof TypeError ||
        (e instanceof APIError && (e.status === 0 || e.status === 502 || e.status === 503));
      if (isNetErr) {
        const ok = await pollHealth(check.latest);
        if (ok) {
          setSuccess(t('settings.upgradedTo', { version: check.latest }));
          try {
            const c = await api.get<UpdateCheck>('/api/v1/update/check');
            setCheck(c);
          } catch { /* ignore */ }
        } else {
          setErr(t('settings.upgradeTimeout'));
        }
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setApplying(false);
      setBanner(null);
    }
  }

  return (
    <Section
      tint="rgb(99 102 241 / 0.35)"
      title={t('settings.upgradeTitle')}
      description={t('settings.upgradeDescription')}
    >
      {loading && <Text>{t('settings.checkingVersion')}</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.operationFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {success && (
        <div className="mb-3">
          <Alert open onClose={() => setSuccess(null)}>
            <AlertTitle>{t('settings.upgradeComplete')}</AlertTitle>
            <AlertBody>{success}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-indigo-500/40 bg-indigo-500/10 px-3 py-2 text-sm text-indigo-900 dark:text-indigo-200">
          {banner}
        </div>
      )}
      {check && (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">{t('settings.currentVersion')}</dt>
            <dd className="font-mono">{check.current || t('settings.unknown')}</dd>
            <dt className="text-zinc-500">{t('settings.latestVersion')}</dt>
            <dd className="font-mono">
              {check.latest || t('settings.unknown')}
              {check.has_update && <Badge color="amber" className="ml-2">{t('settings.updateAvailable')}</Badge>}
              {!check.has_update && <Badge color="lime" className="ml-2">{t('settings.upToDate')}</Badge>}
            </dd>
          </dl>
          <div className="mt-4 flex items-center gap-3">
            <Button
              color="indigo"
              onClick={apply}
              disabled={!check.has_update || applying}
            >
              {applying ? t('settings.upgrading') : t('settings.oneClickUpgrade')}
            </Button>
            {!check.has_update && <Text>{t('settings.alreadyLatest')}</Text>}
          </div>
        </>
      )}
    </Section>
  );
}

function TgbotSection() {
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
      const s = await api.get<TgbotStatus>('/api/v1/settings/tgbot');
      setStatus(s);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { refresh(); }, []);

  function parseAdmins(raw: string): number[] {
    return raw
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean)
      .map((s) => Number(s))
      .filter((n) => Number.isFinite(n) && Number.isInteger(n));
  }

  async function save(action: 'enable' | 'disable') {
    setSaving(true);
    setSavedMsg(null);
    setErr(null);
    try {
      const body = action === 'disable'
        ? { token: '', admin_chat_ids: [] as number[] }
        : { token: token.trim(), admin_chat_ids: parseAdmins(adminIds) };
      if (action === 'enable') {
        if (!body.token) throw new Error(t('settings.tokenRequired'));
        if (body.admin_chat_ids.length === 0) throw new Error(t('settings.adminRequired'));
      }
      const s = await api.post<TgbotStatus>('/api/v1/settings/tgbot', body);
      setStatus(s);
      setSavedMsg(action === 'disable' ? t('settings.botStopped') : t('settings.botRestarted'));
      if (action === 'disable') { setToken(''); setAdminIds(''); }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Section
      tint="rgb(20 184 166 / 0.35)"
      title={t('settings.tgbotTitle')}
      description={t('settings.tgbotDescription')}
    >
      {loading && <Text>{t('common.loading')}</Text>}

      {!loading && (
        <>
          <dl className="mb-4 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">{t('settings.currentStatus')}</dt>
            <dd>
              {status?.enabled
                ? <Badge color="lime">{t('settings.statusRunning')}</Badge>
                : <Badge color="zinc">{t('settings.statusDisabled')}</Badge>}
            </dd>
            <dt className="text-zinc-500">{t('settings.adminCount')}</dt>
            <dd className="font-mono">{status?.admin_count ?? 0}</dd>
            {status?.token_masked && (
              <>
                <dt className="text-zinc-500">{t('settings.tokenLabel')}</dt>
                <dd className="font-mono">{status.token_masked}</dd>
              </>
            )}
          </dl>

          <div className="space-y-3">
            <div>
              <label className="text-xs font-medium uppercase tracking-wide text-zinc-500">
                {t('settings.botTokenLabel')}
              </label>
              <input
                type="password"
                className="mt-1 block w-full rounded-lg border border-zinc-300 bg-white/70 px-3 py-2 text-sm shadow-sm outline-none focus:ring-2 focus:ring-indigo-500 dark:border-zinc-700 dark:bg-zinc-900/60"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={status?.token_masked ? t('settings.currentPlaceholder', { value: status.token_masked }) : '123456:AA...'}
                autoComplete="off"
              />
              <p className="mt-1 text-xs text-zinc-500">
                {t('settings.botTokenHelp')}
              </p>
            </div>

            <div>
              <label className="text-xs font-medium uppercase tracking-wide text-zinc-500">
                {t('settings.adminChatIdsLabel')}
              </label>
              <input
                type="text"
                className="mt-1 block w-full rounded-lg border border-zinc-300 bg-white/70 px-3 py-2 text-sm shadow-sm outline-none focus:ring-2 focus:ring-indigo-500 dark:border-zinc-700 dark:bg-zinc-900/60"
                value={adminIds}
                onChange={(e) => setAdminIds(e.target.value)}
                placeholder="123456789 987654321"
              />
              <p className="mt-1 text-xs text-zinc-500">
                {t('settings.adminChatIdsHelpPrefix')}<span className="font-mono">/id</span>{t('settings.adminChatIdsHelpSuffix')}
              </p>
            </div>

            {err && (
              <div className="rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-sm text-red-800 dark:text-red-200">
                {err}
              </div>
            )}
            {savedMsg && (
              <div className="rounded-lg border border-emerald-400/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-800 dark:text-emerald-200">
                {savedMsg}
              </div>
            )}

            <div className="flex gap-2 pt-1">
              <Button color="indigo" onClick={() => save('enable')} disabled={saving}>
                {saving ? t('settings.saving') : (status?.enabled ? t('settings.restartBot') : t('settings.startBot'))}
              </Button>
              {status?.enabled && (
                <Button plain onClick={() => save('disable')} disabled={saving}>
                  {t('settings.disable')}
                </Button>
              )}
              <Button plain onClick={refresh} disabled={saving}>{t('settings.refresh')}</Button>
            </div>
          </div>
        </>
      )}
    </Section>
  );
}

function IOSSection() {
  const { t } = useTranslation();
  const [profile, setProfile] = useState<IOSProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const p = await api.get<IOSProfile>('/api/v1/ios/profile-url');
        if (!cancelled) setProfile(p);
      } catch (e) {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function copy() {
    if (!profile?.url) return;
    try {
      await navigator.clipboard.writeText(profile.url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  function downloadHref(url: string): string {
    if (!url) return '#';
    if (/\.mobileconfig(\?|$)/.test(url)) return url;
    return url.replace(/\/$/, '') + '/ios-dot.mobileconfig';
  }

  return (
    <Section
      tint="rgb(168 85 247 / 0.35)"
      title={t('settings.iosTitle')}
      description={t('settings.iosDescription')}
    >
      {loading && <Text>{t('common.loading')}</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.iosProfileError')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {profile?.url && (
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
          <div className="rounded-xl bg-white p-3 shadow-sm ring-1 ring-zinc-200 dark:ring-white/10">
            <QRCodeSVG value={profile.url} size={160} level="M" />
          </div>
          <div className="flex-1 space-y-3">
            <div className="break-all rounded-lg bg-zinc-500/5 px-3 py-2 font-mono text-xs text-zinc-800 dark:text-zinc-200">
              {profile.url}
            </div>
            <div className="flex flex-wrap gap-2">
              <Button color="indigo" onClick={copy}>{copied ? t('settings.copied') : t('settings.copyLink')}</Button>
              <Button href={downloadHref(profile.url)} color="zinc">{t('settings.downloadProfile')}</Button>
            </div>
            {profile.uuid && (
              <div className="text-xs text-zinc-500">
                UUID <span className="font-mono">{profile.uuid}</span>
                {profile.port ? <> · {t('settings.port')} <span className="font-mono">{profile.port}</span></> : null}
              </div>
            )}
          </div>
        </div>
      )}
      {!loading && !profile?.url && !err && (
        <div className="text-sm text-zinc-500">
          {t('settings.iosNoProfile')}
        </div>
      )}
    </Section>
  );
}

function PasswordSection() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const clearAuth = useAuthStore((s) => s.clear);
  const clearMe = useMeStore((s) => s.clear);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [fieldErr, setFieldErr] = useState<{ next?: string; confirm?: string; current?: string }>({});

  function validate(): boolean {
    const fe: { next?: string; confirm?: string; current?: string } = {};
    if (next.length < 8) fe.next = t('settings.newPasswordTooShort');
    if (next !== confirm) fe.confirm = t('settings.passwordMismatch');
    if (current && next && current === next) fe.next = t('settings.newPasswordSameAsCurrent');
    setFieldErr(fe);
    return Object.keys(fe).length === 0;
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    if (!validate()) return;
    setSubmitting(true);
    try {
      await api.post('/api/v1/password', { current, next });
      clearMe();
      clearAuth();
      nav('/login', { replace: true });
    } catch (e) {
      if (e instanceof APIError) {
        if (e.status === 401) setErr(t('settings.currentPasswordIncorrect'));
        else setErr(e.message || t('settings.changePasswordFailed'));
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Section
      tint="rgb(239 68 68 / 0.30)"
      title={t('settings.changePassword')}
      description={t('settings.changePasswordDescription')}
    >
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.changePasswordFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      <form onSubmit={submit} className="space-y-5">
        <Field>
          <Label>{t('settings.currentPasswordLabel')}</Label>
          <Input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            autoComplete="current-password"
            required
          />
          {fieldErr.current && <ErrorMessage>{fieldErr.current}</ErrorMessage>}
        </Field>
        <Field>
          <Label>{t('settings.newPasswordLabel')}</Label>
          <Input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            autoComplete="new-password"
            required
          />
          {fieldErr.next && <ErrorMessage>{fieldErr.next}</ErrorMessage>}
        </Field>
        <Field>
          <Label>{t('settings.confirmPasswordLabel')}</Label>
          <Input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
          />
          {fieldErr.confirm && <ErrorMessage>{fieldErr.confirm}</ErrorMessage>}
        </Field>
        <div>
          <Button type="submit" color="indigo" disabled={submitting}>
            {submitting ? t('settings.submitting') : t('settings.changePassword')}
          </Button>
        </div>
      </form>
    </Section>
  );
}

function RestartSection() {
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function doRestart() {
    setConfirm(false);
    setErr(null);
    setBusy(true);
    setBanner(t('settings.restarting'));
    try {
      await api.post('/api/v1/system/restart');
      setBanner(t('settings.restartQueued'));
    } catch (e) {
      if (e instanceof APIError && e.status === 503) {
        setErr(t('settings.restartUnavailable'));
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      tint="rgb(244 63 94 / 0.35)"
      title={t('settings.restartTitle')}
      description={t('settings.restartDescription')}
    >
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.operationFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">
          {banner}
        </div>
      )}
      <Button color="red" disabled={busy} onClick={() => setConfirm(true)}>
        {busy ? t('settings.restarting') : t('settings.restartButton')}
      </Button>
      <Alert open={confirm} onClose={() => setConfirm(false)}>
        <AlertTitle>{t('settings.restartConfirmTitle')}</AlertTitle>
        <AlertBody>{t('settings.restartConfirmBody')}</AlertBody>
        <div className="mt-4 flex justify-end gap-2">
          <Button plain onClick={() => setConfirm(false)}>{t('common.cancel')}</Button>
          <Button color="red" onClick={doRestart}>{t('settings.restartConfirm')}</Button>
        </div>
      </Alert>
    </Section>
  );
}

export default function Settings() {
  const { t } = useTranslation();
  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('nav.settings')}</Heading>
        <Text className="mt-1">{t('settings.subtitle')}</Text>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UpgradeSection />
        <TgbotSection />
        <IOSSection />
        <PasswordSection />
        <RestartSection />
      </div>
    </AppShell>
  );
}
