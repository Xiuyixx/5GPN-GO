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
  const [enabled, setEnabled] = useState<boolean>(false);
  const [preflightRunning, setPreflightRunning] = useState(false);
  const [preflightResult, setPreflightResult] = useState<{ ok: boolean; message: string } | null>(null);
  const [toggling, setToggling] = useState(false);

  async function refreshEnabled() {
    try {
      const s = await api.get<{ ios?: { profile_enabled?: boolean } }>('/api/v1/settings/panel');
      setEnabled(Boolean(s.ios?.profile_enabled));
    } catch {
      // fall through — leave enabled=false
    }
  }

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
      await refreshEnabled();
    })();
    return () => { cancelled = true; };
  }, []);

  async function runPreflight() {
    setPreflightRunning(true);
    setPreflightResult(null);
    try {
      const r = await api.post<{ ok: boolean; dot_handshake: boolean; sample_query: boolean; error?: string }>(
        '/api/v1/settings/ios/preflight',
        {},
      );
      setPreflightResult({
        ok: r.ok,
        message: r.ok
          ? t('settings.iosPreflightPass', { defaultValue: 'Preflight passed — DoT is answering.' })
          : t('settings.iosPreflightFail', {
              defaultValue: 'Preflight failed: {{err}}',
              err: r.error || 'unknown',
            }),
      });
    } catch (e) {
      setPreflightResult({ ok: false, message: e instanceof Error ? e.message : String(e) });
    } finally {
      setPreflightRunning(false);
    }
  }

  async function toggleEnabled(next: boolean) {
    setToggling(true);
    try {
      await api.post('/api/v1/settings/ios/profile-enabled', { enabled: next });
      setEnabled(next);
      if (next) setPreflightResult({ ok: true, message: t('settings.iosProfileTurnedOn', { defaultValue: 'iOS profile enabled — the download link is now live.' }) });
    } catch (e) {
      setPreflightResult({ ok: false, message: e instanceof Error ? e.message : String(e) });
    } finally {
      setToggling(false);
    }
  }

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

      {!loading && (
        <div className="mb-4 rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
          <div className="mb-2 flex items-center justify-between gap-3">
            <div>
              <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">
                {t('settings.iosProfileToggleTitle', { defaultValue: 'iOS Profile' })}
              </div>
              <div className="text-xs text-zinc-500">
                {enabled
                  ? t('settings.iosProfileEnabledHint', { defaultValue: 'Enabled — the mobileconfig download link is live.' })
                  : t('settings.iosProfileDisabledHint', {
                      defaultValue: 'Disabled — run preflight, then enable so the mobileconfig link goes live.',
                    })}
              </div>
            </div>
            <Button
              color={enabled ? 'zinc' : 'indigo'}
              onClick={() => toggleEnabled(!enabled)}
              disabled={toggling}
            >
              {toggling
                ? t('common.loading')
                : enabled
                ? t('settings.iosDisable', { defaultValue: 'Disable' })
                : t('settings.iosEnable', { defaultValue: 'Enable' })}
            </Button>
          </div>
          <div className="mt-3 flex items-center gap-2">
            <Button plain onClick={runPreflight} disabled={preflightRunning}>
              {preflightRunning
                ? t('settings.iosPreflightRunning', { defaultValue: 'Running preflight…' })
                : t('settings.iosPreflightRun', { defaultValue: 'Run preflight' })}
            </Button>
            {preflightResult && (
              <span
                className={
                  'text-xs ' +
                  (preflightResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400')
                }
              >
                {preflightResult.message}
              </span>
            )}
          </div>
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

interface MTProxySettings {
  enabled: boolean;
  listen: string;
  secret_configured: boolean;
  fronting_domain: string;
  connect_link_hint: string;
  service_status: string;
}

interface PanelDomain {
  server?: { domain?: string };
}

// MTProxySection wraps the Telegram MTProto proxy settings. This card
// used to drive the panel's in-tree obfuscated2 relay; that code had a
// subtle relay bug we couldn't fix in reasonable time, so both VPS now
// run the externally-installed 9seconds/mtg 2.x service and this card
// is a thin driver over `systemctl` + `mtg generate-secret` via the
// backend /api/v1/settings/mtproxy handler surface.
//
// The raw secret is still one-shot: the server returns the full
// base64-url value only from the /generate-secret endpoint, and every
// subsequent GET only reports the decoded fronting domain. Because of
// that the "share this link / show QR" panel is available only in the
// same session tick as a fresh Generate — after navigating away the
// operator must Rotate to see it again.
function MTProxySection() {
  const { t } = useTranslation();
  const [cfg, setCfg] = useState<MTProxySettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);
  // draftEnabled / draftDomain hold the pending toggle + text-input
  // state. Kept separate from cfg so a mid-edit refresh doesn't wipe
  // the operator's typing.
  const [draftEnabled, setDraftEnabled] = useState(false);
  const [draftDomain, setDraftDomain] = useState('www.cloudflare.com');
  // freshSecret holds the raw base64-url returned by /generate-secret.
  // Cleared on refresh / navigation — matches the "only shown once"
  // contract of the previous UX.
  const [freshSecret, setFreshSecret] = useState<string | null>(null);
  const [freshLink, setFreshLink] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [domain, setDomain] = useState<string>('');

  async function refresh() {
    setErr(null);
    try {
      const c = await api.get<MTProxySettings>('/api/v1/settings/mtproxy');
      setCfg(c);
      setDraftEnabled(c.enabled);
      // Prefer the mtg-decoded domain; fall back to the sane default.
      setDraftDomain(c.fronting_domain || 'www.cloudflare.com');
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  async function loadDomain() {
    try {
      const p = await api.get<PanelDomain>('/api/v1/settings/panel');
      setDomain(p.server?.domain?.trim() || '');
    } catch {
      // Domain is only used to render the tg:// link nicely; a missing
      // value degrades to "<server-address>" placeholder text rather
      // than blocking the whole card.
    }
  }

  useEffect(() => {
    refresh();
    loadDomain();
  }, []);

  async function save() {
    if (!cfg) return;
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const body: { enabled: boolean; fronting_domain?: string } = {
        enabled: draftEnabled,
      };
      // Only send fronting_domain when it actually changed — an
      // unchanged POST should not rotate the secret.
      if (draftDomain.trim() && draftDomain.trim() !== (cfg.fronting_domain || '').trim()) {
        body.fronting_domain = draftDomain.trim();
      }
      const r = await api.post<MTProxySettings>('/api/v1/settings/mtproxy', body);
      setCfg(r);
      setDraftEnabled(r.enabled);
      setDraftDomain(r.fronting_domain || 'www.cloudflare.com');
      setBanner(t('settings.mtproxySaved'));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  async function generateSecret() {
    setGenerating(true);
    setErr(null);
    setBanner(null);
    try {
      const r = await api.post<{ ok: boolean; secret: string; connect_link: string }>(
        '/api/v1/settings/mtproxy/generate-secret',
        { fronting_domain: draftDomain.trim() || 'www.cloudflare.com' },
      );
      if (!r.ok || !r.secret) {
        throw new Error(t('settings.operationFailed'));
      }
      setFreshSecret(r.secret);
      setFreshLink(r.connect_link || null);
      await refresh();
      setBanner(t('settings.mtproxyRestartHint'));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setGenerating(false);
    }
  }

  function dirty(): boolean {
    if (!cfg) return false;
    if (cfg.enabled !== draftEnabled) return true;
    if (draftDomain.trim() && draftDomain.trim() !== (cfg.fronting_domain || '').trim()) return true;
    return false;
  }

  // Build the tg:// link and Telegram-web deep link. Only meaningful
  // when we hold a freshly-generated secret in memory.
  function buildLinks(): { tg: string; https: string; portDisplay: string } | null {
    if (!freshSecret) return null;
    const listen = cfg?.listen || '';
    const idx = listen.lastIndexOf(':');
    const port = idx >= 0 ? listen.slice(idx + 1) : '';
    const host = domain || (idx > 0 ? listen.slice(0, idx) : '') || 'YOUR_SERVER';
    const server = encodeURIComponent(host);
    const portDisplay = port || '?';
    // Telegram clients accept the base64-url ee-prefix secret in the
    // `secret` slot. The tg:// scheme and https://t.me/proxy landing
    // page share the same querystring.
    const params = `server=${server}&port=${port}&secret=${freshSecret}`;
    return {
      tg: freshLink || `tg://proxy?${params}`,
      https: `https://t.me/proxy?${params}`,
      portDisplay,
    };
  }

  async function copyToClipboard(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Non-secure contexts (http://) can't reach clipboard API. The
      // "Copied" flash won't fire, but the text is still selectable
      // from the UI.
    }
  }

  const links = buildLinks();
  const notInstalled = cfg?.service_status === 'not-installed';

  return (
    <Section
      tint="rgb(56 189 248 / 0.35)"
      title={t('settings.mtproxyTitle')}
      description={t('settings.mtproxyDescription')}
    >
      {loading && <Text>{t('common.loading')}</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.operationFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-sm text-sky-900 dark:text-sky-200">
          {banner}
        </div>
      )}
      {!loading && cfg && (
        <div className="mb-3 rounded-lg border border-zinc-300/60 bg-zinc-50/70 px-3 py-2 text-xs text-zinc-600 dark:border-white/10 dark:bg-white/5 dark:text-zinc-300">
          {t('settings.mtproxyExternalNote')}
        </div>
      )}
      {notInstalled && (
        <div className="mb-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">
          {t('settings.mtproxyLoopbackFallback')}
        </div>
      )}

      {!loading && cfg && (
        <div className="space-y-4">
          {/* Service status */}
          <div className="text-xs text-zinc-500">
            {t('settings.mtproxyServiceStatus', { status: cfg.service_status || 'unknown' })}
          </div>

          {/* Enable toggle */}
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <label className="flex items-center gap-3">
              <input
                type="checkbox"
                checked={draftEnabled}
                disabled={!cfg.secret_configured}
                onChange={(e) => setDraftEnabled(e.target.checked)}
                className="h-4 w-4"
              />
              <div>
                <div className="text-sm font-medium">
                  {t('settings.mtproxyEnabledLabel')}
                </div>
                <div className="text-xs text-zinc-500">
                  {t('settings.mtproxyEnabledHint')}
                </div>
              </div>
            </label>
          </div>

          {/* Listen address — read-only, managed by mtg unit */}
          <Field>
            <Label>{t('settings.mtproxyListenLabel')}</Label>
            <Input value={cfg.listen} readOnly />
            <div className="mt-1 text-xs text-zinc-500">
              {t('settings.mtproxyListenHint')}
            </div>
          </Field>

          {/* Fronting domain — replaces the manually-typed secret. */}
          <Field>
            <Label>{t('settings.mtproxyFrontingDomainLabel')}</Label>
            <Input
              value={draftDomain}
              placeholder="www.cloudflare.com"
              onChange={(e) => setDraftDomain(e.target.value)}
            />
            <div className="mt-1 text-xs text-zinc-500">
              {t('settings.mtproxyFrontingDomainHint')}
            </div>
          </Field>

          {/* Secret block */}
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <div className="text-sm font-medium">{t('settings.mtproxySecretLabel')}</div>
            <div className="mt-2 flex items-center gap-3">
              <span className="font-mono text-sm">
                {cfg.secret_configured
                  ? t('settings.mtproxySecretConfigured', {
                      domain: cfg.fronting_domain || 'unknown',
                    })
                  : t('settings.mtproxySecretNotConfigured')}
              </span>
              <Button plain onClick={generateSecret} disabled={generating || notInstalled}>
                {generating
                  ? t('settings.saving')
                  : cfg.secret_configured
                    ? t('settings.mtproxyRotateSecret')
                    : t('settings.mtproxyGenerateSecret')}
              </Button>
            </div>

            {freshSecret && (
              <div className="mt-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-200">
                <div className="mb-2 font-medium">
                  {t('settings.mtproxySecretRevealWarning')}
                </div>
                <div className="flex items-center gap-2">
                  <code className="break-all rounded bg-white/60 px-2 py-1 font-mono text-[11px] text-amber-950 dark:bg-white/10 dark:text-amber-100">
                    {freshSecret}
                  </code>
                  <Button plain onClick={() => copyToClipboard(freshSecret)}>
                    {copied ? t('settings.mtproxyCopied') : t('settings.mtproxyCopyLink')}
                  </Button>
                </div>
              </div>
            )}
          </div>

          {/* Client link + QR (only after fresh generate) */}
          {links && (
            <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
              <div className="text-sm font-medium">
                {t('settings.mtproxyClientLinkTitle')}
              </div>
              <div className="mt-2 text-xs text-zinc-500">
                {t('settings.mtproxyQRHint')}
              </div>
              <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-start">
                <div className="rounded-lg bg-white p-2">
                  <QRCodeSVG value={links.https} size={160} />
                </div>
                <div className="flex-1 space-y-2 text-xs">
                  <div>
                    <div className="text-zinc-500">tg://</div>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 break-all rounded bg-zinc-100 px-2 py-1 font-mono text-[11px] dark:bg-zinc-800">
                        {links.tg}
                      </code>
                      <Button plain onClick={() => copyToClipboard(links.tg)}>
                        {copied ? t('settings.mtproxyCopied') : t('settings.mtproxyCopyLink')}
                      </Button>
                    </div>
                  </div>
                  <div>
                    <div className="text-zinc-500">https://t.me/proxy</div>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 break-all rounded bg-zinc-100 px-2 py-1 font-mono text-[11px] dark:bg-zinc-800">
                        {links.https}
                      </code>
                      <Button plain onClick={() => copyToClipboard(links.https)}>
                        {copied ? t('settings.mtproxyCopied') : t('settings.mtproxyCopyLink')}
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}

          <div className="flex gap-2 pt-1">
            <Button color="indigo" onClick={save} disabled={saving || !dirty() || notInstalled}>
              {saving ? t('settings.saving') : t('settings.mtproxySave')}
            </Button>
            <Button plain onClick={refresh} disabled={saving}>
              {t('settings.refresh')}
            </Button>
          </div>
        </div>
      )}
    </Section>
  );
}

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

interface InternalOnlySettings {
  enabled: boolean;
  cidrs: string;
}

// InternalOnlySection wraps the "restrict panel + proxies to internal
// network clients" toggle. When enabled, the panel API + mtproxy +
// sniforward + quicforward all drop connections whose source IP is
// outside the configured CIDR list (default: RFC1918 + loopback +
// v6 ULA). Business rule: paying customers all come in through the
// 联通 5G APN slice, so any request from a public IP is prima facie
// non-customer scanner traffic.
//
// The change applies live — no daemon restart — because the middleware
// and every proxy share a single *access.Gate whose snapshot is
// swapped atomically on Gate.Refresh().
function InternalOnlySection() {
  const { t } = useTranslation();
  const [cfg, setCfg] = useState<InternalOnlySettings | null>(null);
  const [draft, setDraft] = useState<InternalOnlySettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function refresh() {
    try {
      const c = await api.get<InternalOnlySettings>('/api/v1/settings/frontdoor/internal-only');
      setCfg(c);
      setDraft(c);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  async function save() {
    if (!draft) return;
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const r = await api.post<InternalOnlySettings>('/api/v1/settings/frontdoor/internal-only', {
        enabled: draft.enabled,
        cidrs: draft.cidrs,
      });
      setCfg(r);
      setDraft(r);
      setBanner(t('settings.internalOnlySaved', {
        defaultValue: 'Saved. Allowlist applied live to the panel and every proxy listener.',
      }));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  function dirty(): boolean {
    if (!cfg || !draft) return false;
    return cfg.enabled !== draft.enabled || cfg.cidrs !== draft.cidrs;
  }

  return (
    <Section
      tint="rgb(16 185 129 / 0.35)"
      title={t('settings.internalOnlyTitle', { defaultValue: '内网限制访问' })}
      description={t('settings.internalOnlyDescription', {
        defaultValue:
          'Restrict the panel, MTProxy, and the transparent forwarders to clients whose source IP is inside the internal (private) CIDR list. Public-IP scanners are dropped at accept time.',
      })}
    >
      {loading && <Text>{t('common.loading')}</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.operationFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-900 dark:text-emerald-200">
          {banner}
        </div>
      )}

      {!loading && draft && (
        <div className="space-y-4">
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <label className="flex items-center gap-3">
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
                className="h-4 w-4"
              />
              <div>
                <div className="text-sm font-medium">
                  {t('settings.internalOnlyEnabledLabel', { defaultValue: '启用内网限制' })}
                </div>
                <div className="text-xs text-zinc-500">
                  {t('settings.internalOnlyEnabledHint', {
                    defaultValue:
                      'When on, only source IPs inside the CIDR list below can reach the panel and the proxies. Loopback is always allowed.',
                  })}
                </div>
              </div>
            </label>
          </div>

          <Field>
            <Label>
              {t('settings.internalOnlyCIDRLabel', { defaultValue: '允许的 CIDR（逗号分隔）' })}
            </Label>
            <textarea
              value={draft.cidrs}
              placeholder={t('settings.internalOnlyCIDRPlaceholder', {
                defaultValue: '172.16.0.0/12,10.0.0.0/8,192.168.0.0/16,127.0.0.0/8,::1/128,fd00::/8',
              })}
              onChange={(e) => setDraft({ ...draft, cidrs: e.target.value })}
              rows={3}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 font-mono text-xs dark:border-white/10 dark:bg-white/5"
            />
            <div className="mt-1 text-xs text-zinc-500">
              {t('settings.internalOnlyCIDRHint', {
                defaultValue:
                  'Comma-separated CIDR blocks. Empty falls back to private-only defaults (RFC1918 + loopback + v6 ULA).',
              })}
            </div>
          </Field>

          <div>
            <Button color="indigo" disabled={saving || !dirty()} onClick={save}>
              {saving
                ? t('settings.submitting')
                : t('settings.internalOnlySave', { defaultValue: 'Save internal-only settings' })}
            </Button>
          </div>
        </div>
      )}
    </Section>
  );
}

function TransparentProxySection() {
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
      const c = await api.get<ProxySettings>('/api/v1/settings/frontdoor/proxy');
      setCfg(c);
      setDraft(c);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  async function runPreflight() {
    setBanner(t('settings.proxyPreflightRunning', { defaultValue: 'Probing egress IP…' }));
    try {
      const r = await api.post<{ ok: boolean; server_ip_autodetected?: string; error?: string }>(
        '/api/v1/settings/frontdoor/proxy/preflight',
        {},
      );
      if (r.ok) {
        setBanner(t('settings.proxyPreflightPass', {
          defaultValue: 'Egress IP detected: {{ip}}',
          ip: r.server_ip_autodetected,
        }));
        if (draft) setDraft({ ...draft, server_ip_autodetected: r.server_ip_autodetected || '' });
      } else {
        setBanner(t('settings.proxyPreflightFail', {
          defaultValue: 'Preflight failed: {{err}}',
          err: r.error || 'unknown',
        }));
      }
    } catch (e) {
      setBanner(e instanceof Error ? e.message : String(e));
    }
  }

  async function save() {
    if (!draft) return;
    setSaving(true);
    setErr(null);
    setBanner(null);
    try {
      const r = await api.post<ProxySettings>('/api/v1/settings/frontdoor/proxy', {
        spoof_enabled: draft.spoof_enabled,
        spoof_scope: draft.spoof_scope,
        spoof_server_ip: draft.spoof_server_ip,
        spoof_allow_cidr: draft.spoof_allow_cidr,
        sni_forward_enabled: draft.sni_forward_enabled,
        quic_forward_enabled: draft.quic_forward_enabled,
        panel_backend_tcp: draft.panel_backend_tcp,
        panel_backend_udp: draft.panel_backend_udp,
      });
      setCfg(r);
      setDraft(r);
      if (r.restart_required) {
        setRestartNeeded(true);
        setBanner(t('settings.proxyRestartRequired', {
          defaultValue: 'Saved. Restart the daemon so port :443 changes take effect.',
        }));
      } else {
        setBanner(t('settings.proxySaved', {
          defaultValue: 'Saved. Spoof policy applied live to the running resolver.',
        }));
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  function dirty(): boolean {
    if (!cfg || !draft) return false;
    return (
      cfg.spoof_enabled !== draft.spoof_enabled ||
      cfg.spoof_scope !== draft.spoof_scope ||
      cfg.spoof_server_ip !== draft.spoof_server_ip ||
      cfg.spoof_allow_cidr !== draft.spoof_allow_cidr ||
      cfg.sni_forward_enabled !== draft.sni_forward_enabled ||
      cfg.quic_forward_enabled !== draft.quic_forward_enabled ||
      cfg.panel_backend_tcp !== draft.panel_backend_tcp ||
      cfg.panel_backend_udp !== draft.panel_backend_udp
    );
  }

  return (
    <Section
      tint="rgb(59 130 246 / 0.35)"
      title={t('settings.proxyTitle', { defaultValue: 'Transparent Proxy (Path B)' })}
      description={t('settings.proxyDescription', {
        defaultValue:
          'Redirect proxy-classified DNS answers to this gateway, then transparently forward TCP/UDP :443 to the real origin via SNI/QUIC peek.',
      })}
    >
      {loading && <Text>{t('common.loading')}</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('settings.operationFailed')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-blue-500/40 bg-blue-500/10 px-3 py-2 text-sm text-blue-900 dark:text-blue-200">
          {banner}
        </div>
      )}
      {restartNeeded && (
        <div className="mb-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">
          {t('settings.proxyRestartHint', {
            defaultValue: 'Restart the daemon (Settings → Restart) to apply :443 socket changes.',
          })}
        </div>
      )}

      {!loading && draft && (
        <div className="space-y-4">
          {/* Spoof toggle */}
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <label className="flex items-center gap-3">
              <input
                type="checkbox"
                checked={draft.spoof_enabled}
                onChange={(e) => setDraft({ ...draft, spoof_enabled: e.target.checked })}
                className="h-4 w-4"
              />
              <div>
                <div className="text-sm font-medium">
                  {t('settings.proxySpoofToggle', { defaultValue: 'DNS Spoof (redirect proxy answers to this gateway)' })}
                </div>
                <div className="text-xs text-zinc-500">
                  {t('settings.proxySpoofHint', {
                    defaultValue:
                      'Proxy-classified A/AAAA answers point at this server\'s IP instead of the real origin. Requires SNI/QUIC forward to complete the loop.',
                  })}
                </div>
              </div>
            </label>
          </div>

          {/* SNI forward */}
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <label className="flex items-center gap-3">
              <input
                type="checkbox"
                checked={draft.sni_forward_enabled}
                onChange={(e) => setDraft({ ...draft, sni_forward_enabled: e.target.checked })}
                className="h-4 w-4"
              />
              <div>
                <div className="text-sm font-medium">
                  {t('settings.proxySNIToggle', { defaultValue: 'TCP :443 SNI Forwarder' })}
                </div>
                <div className="text-xs text-zinc-500">
                  {t('settings.proxySNIHint', {
                    defaultValue:
                      'Own public TCP :443. Panel HTTPS moves to a loopback backend; SNI split routes panel-domain traffic to the panel, all other SNIs to the real host. Requires restart.',
                  })}
                </div>
              </div>
            </label>
          </div>

          {/* QUIC forward */}
          <div className="rounded-xl border border-zinc-200 bg-white/60 p-4 dark:border-white/10 dark:bg-white/5">
            <label className="flex items-center gap-3">
              <input
                type="checkbox"
                checked={draft.quic_forward_enabled}
                onChange={(e) => setDraft({ ...draft, quic_forward_enabled: e.target.checked })}
                className="h-4 w-4"
              />
              <div>
                <div className="text-sm font-medium">
                  {t('settings.proxyQUICToggle', { defaultValue: 'UDP :443 QUIC SNI Forwarder' })}
                </div>
                <div className="text-xs text-zinc-500">
                  {t('settings.proxyQUICHint', {
                    defaultValue:
                      'Own public UDP :443 for QUIC/HTTP3. DoH3 (if enabled) moves to a loopback UDP port. Requires restart.',
                  })}
                </div>
              </div>
            </label>
          </div>

          {/* Server IP */}
          <Field>
            <Label>
              {t('settings.proxyServerIPLabel', { defaultValue: 'Gateway IP (leave empty to auto-detect)' })}
            </Label>
            <Input
              value={draft.spoof_server_ip}
              placeholder={draft.server_ip_autodetected || 'auto'}
              onChange={(e) => setDraft({ ...draft, spoof_server_ip: e.target.value })}
            />
            <div className="mt-1 flex items-center gap-3 text-xs text-zinc-500">
              <span>
                {t('settings.proxyServerIPHint', {
                  defaultValue: 'Effective: {{ip}}',
                  ip: draft.server_ip_effective || '(none)',
                })}
              </span>
              <button
                type="button"
                onClick={runPreflight}
                className="text-blue-600 hover:underline dark:text-blue-400"
              >
                {t('settings.proxyRunPreflight', { defaultValue: 'Re-detect' })}
              </button>
            </div>
          </Field>

          {/* Scope */}
          <Field>
            <Label>{t('settings.proxyScopeLabel', { defaultValue: 'Spoof Scope' })}</Label>
            <select
              value={draft.spoof_scope}
              onChange={(e) => setDraft({ ...draft, spoof_scope: e.target.value })}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 text-sm dark:border-white/10 dark:bg-white/5"
            >
              <option value="all">
                {t('settings.proxyScopeAll', { defaultValue: 'all — spoof every proxy client (recommended for public DoH)' })}
              </option>
              <option value="private_only">
                {t('settings.proxyScopePrivate', { defaultValue: 'private_only — spoof only allow-listed CIDRs' })}
              </option>
            </select>
          </Field>

          {/* Allow CIDR — only shown when private_only */}
          {draft.spoof_scope === 'private_only' && (
            <Field>
              <Label>
                {t('settings.proxyAllowCIDRLabel', {
                  defaultValue: 'Allow CIDRs (comma-separated, e.g. 172.22.0.0/16)',
                })}
              </Label>
              <Input
                value={draft.spoof_allow_cidr}
                placeholder="172.22.0.0/16"
                onChange={(e) => setDraft({ ...draft, spoof_allow_cidr: e.target.value })}
              />
            </Field>
          )}

          {/* Advanced */}
          <div>
            <button
              type="button"
              onClick={() => setAdvanced(!advanced)}
              className="text-xs text-zinc-500 hover:text-zinc-700 dark:hover:text-zinc-300"
            >
              {advanced
                ? t('settings.proxyHideAdvanced', { defaultValue: '▼ Hide advanced' })
                : t('settings.proxyShowAdvanced', { defaultValue: '▶ Advanced (panel backend ports)' })}
            </button>
          </div>
          {advanced && (
            <div className="space-y-3">
              <Field>
                <Label>{t('settings.proxyPanelTCPLabel', { defaultValue: 'Panel loopback TCP backend' })}</Label>
                <Input
                  value={draft.panel_backend_tcp}
                  placeholder="127.0.0.1:8444"
                  onChange={(e) => setDraft({ ...draft, panel_backend_tcp: e.target.value })}
                />
              </Field>
              <Field>
                <Label>{t('settings.proxyPanelUDPLabel', { defaultValue: 'Panel loopback UDP backend (DoH3)' })}</Label>
                <Input
                  value={draft.panel_backend_udp}
                  placeholder="127.0.0.1:8445"
                  onChange={(e) => setDraft({ ...draft, panel_backend_udp: e.target.value })}
                />
              </Field>
            </div>
          )}

          <div>
            <Button color="indigo" disabled={saving || !dirty()} onClick={save}>
              {saving ? t('settings.submitting') : t('settings.proxySave', { defaultValue: 'Save proxy settings' })}
            </Button>
          </div>
        </div>
      )}
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
        <MTProxySection />
        <InternalOnlySection />
        <TransparentProxySection />
        <PasswordSection />
        <RestartSection />
      </div>
    </AppShell>
  );
}
