// First-time-setup wizard.
//
// Reached when GET /api/v1/bootstrap returns { needs_wizard: true }. The
// wizard reads the current panel_settings snapshot on mount (POST paths
// are partial patches, so we prefill with whatever the admin previously
// saved or with sane defaults), walks the operator through Server →
// Telegram Bot → Review, and on Finish POSTs the whole shape plus
// wizard.complete = true so the app.tsx gate stops routing here.
//
// Nothing writes until Finish — Back / Next / Skip all mutate local
// state only.

import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Input } from '../components/ui/input';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { Switch, SwitchField } from '../components/ui/switch';
import LanguageSwitcher from '../components/LanguageSwitcher';
import { api } from '../api/client';
import type { PanelSettings, PanelSettingsUpdate } from '../api/client';

type Step = 'server' | 'tgbot' | 'review';

interface DraftServer {
  domain: string;
  panelBind: string;
  panelPort: number;
  acmeEnabled: boolean;
  acmeEmail: string;
}

interface DraftTGBot {
  token: string;
  adminIdsText: string; // comma/space separated; parsed at submit
}

const DEFAULT_SERVER: DraftServer = {
  domain: '',
  panelBind: '0.0.0.0',
  panelPort: 443,
  // ACME is ON by default. The wizard's whole point is auto-SSL on a
  // public domain; leaving it off + panelPort=443 makes the daemon
  // serve plain HTTP on :443, which browsers reject with
  // ERR_SSL_PROTOCOL_ERROR. If the operator really wants no TLS they
  // must also move the port off 443/80.
  acmeEnabled: true,
  acmeEmail: '',
};

// TLS-standard ports we refuse to bind plain HTTP on. Keep in sync
// with the backend guard in cmd/5gpn/main.go.
const TLS_STANDARD_PORTS = new Set<number>([80, 443]);

const DEFAULT_TGBOT: DraftTGBot = { token: '', adminIdsText: '' };

function parseAdminIds(raw: string): number[] {
  return raw
    .split(/[,\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n !== 0);
}

interface WizardProps {
  onDone?: () => Promise<void> | void;
}

export default function Wizard({ onDone }: WizardProps) {
  const { t } = useTranslation();
  const nav = useNavigate();

  const [step, setStep] = useState<Step>('server');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);

  const [server, setServer] = useState<DraftServer>(DEFAULT_SERVER);
  const [tgbot, setTgbot] = useState<DraftTGBot>(DEFAULT_TGBOT);

  // Prefill from server on mount so re-visits show what was last saved.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const s = await api.get<PanelSettings>('/api/v1/settings/panel');
        if (cancelled) return;
        setServer({
          domain: s.server.domain,
          panelBind: s.server.panel_bind || DEFAULT_SERVER.panelBind,
          panelPort: s.server.panel_port || DEFAULT_SERVER.panelPort,
          acmeEnabled: s.tls.acme_enabled,
          acmeEmail: s.tls.acme_email,
        });
        setTgbot({
          token: '', // never round-trip the real token — user must retype if changing
          adminIdsText: (s.tgbot.admin_chat_ids ?? []).join(', '),
        });
      } catch (e) {
        // First install: no panel_settings row yet. Defaults are fine.
        // Non-404 errors we surface anyway so the operator sees them.
        if (e instanceof Error && !/404|not.found/i.test(e.message)) {
          setError(e.message);
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const serverStepValid = useMemo(() => {
    if (!server.domain.trim()) return false;
    if (server.panelPort < 1 || server.panelPort > 65535) return false;
    // TLS-standard ports (80/443) must have ACME on. Otherwise the
    // daemon would bind plain HTTP on :443 and browsers would fail
    // the TLS handshake with ERR_SSL_PROTOCOL_ERROR. This mirrors the
    // fail-fast guard on the daemon side (cmd/5gpn/main.go).
    if (TLS_STANDARD_PORTS.has(server.panelPort) && !server.acmeEnabled) return false;
    if (server.acmeEnabled && !/@/.test(server.acmeEmail)) return false;
    return true;
  }, [server]);

  const tlsPortWarning = useMemo(() => {
    if (TLS_STANDARD_PORTS.has(server.panelPort) && !server.acmeEnabled) {
      return t('wizard.tlsPortRequiresAcme', {
        defaultValue:
          'Port {{port}} is reserved for TLS. Turn on Auto-SSL, or move the panel to another port.',
        port: server.panelPort,
      });
    }
    return null;
  }, [server.panelPort, server.acmeEnabled, t]);

  const tgbotStepValid = useMemo(() => {
    // Empty token is OK (skip). If a token is present, at least one admin id.
    if (!tgbot.token.trim()) return true;
    return parseAdminIds(tgbot.adminIdsText).length > 0;
  }, [tgbot]);

  async function finish() {
    setError(null);
    setSaving(true);
    try {
      const patch: PanelSettingsUpdate = {
        server: {
          domain: server.domain.trim(),
          panel_bind: server.panelBind.trim(),
          panel_port: server.panelPort,
        },
        tls: {
          acme_enabled: server.acmeEnabled,
          acme_email: server.acmeEmail.trim(),
        },
        wizard: { complete: true },
      };
      const tokenSet = tgbot.token.trim() !== '';
      if (tokenSet) {
        patch.tgbot = {
          token: tgbot.token.trim(),
          admin_chat_ids: parseAdminIds(tgbot.adminIdsText),
        };
      } else if (tgbot.adminIdsText.trim()) {
        // Admin IDs entered but no token — persist ids anyway (Skip case).
        patch.tgbot = { admin_chat_ids: parseAdminIds(tgbot.adminIdsText) };
      }
      const resp = await api.post<PanelSettings & { tgbot_warning?: string }>(
        '/api/v1/settings/panel', patch,
      );
      // TG bot token was rejected by Telegram (bad token, revoked, etc.).
      // Everything else was still persisted and wizard is marked complete —
      // show a non-blocking warning and stay on the wizard page so the
      // operator can retype or clear the token. Any other outcome navigates
      // straight to the dashboard.
      if (resp.tgbot_warning) {
        setWarning(resp.tgbot_warning);
        setSaving(false);
        return;
      }
      if (onDone) await onDone();
      nav('/', { replace: true });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <div className="ambient-bg" aria-hidden />
      <div className="fade-up flex min-h-screen items-center justify-center p-6">
        <div className="glass-strong w-full max-w-2xl p-8">
          <div className="mb-6 flex items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              <div className="grid h-11 w-11 place-items-center rounded-2xl bg-gradient-to-br from-indigo-500 via-violet-500 to-teal-400 text-sm font-bold text-white shadow-lg shadow-indigo-500/30">
                5G
              </div>
              <div>
                <div className="text-xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">{t('wizard.heading')}</div>
                <div className="text-xs text-zinc-500">{t('wizard.subheading')}</div>
              </div>
            </div>
            <LanguageSwitcher compact />
          </div>

          <StepPills current={step} />

          {loading && <div className="text-sm text-zinc-500">{t('wizard.loadingCurrent')}</div>}
          {error && (
            <div className="my-3">
              <Alert open onClose={() => setError(null)}>
                <AlertTitle>{t('wizard.errorTitle')}</AlertTitle>
                <AlertBody>{error}</AlertBody>
              </Alert>
            </div>
          )}
          {warning && (
            <div className="my-3 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-900 dark:text-amber-200">
              <div className="mb-2 font-medium">{t('wizard.tgbotWarningTitle')}</div>
              <div className="mb-2 text-xs">{t('wizard.tgbotWarningBody', { detail: warning })}</div>
              <div className="flex justify-end gap-2">
                <Button plain onClick={() => setWarning(null)}>{t('wizard.tgbotWarningRetype')}</Button>
                <Button
                  color="amber"
                  onClick={async () => {
                    setWarning(null);
                    if (onDone) await onDone();
                    nav('/', { replace: true });
                  }}
                >
                  {t('wizard.tgbotWarningContinue')}
                </Button>
              </div>
            </div>
          )}

          {!loading && step === 'server' && (
            <ServerStep server={server} setServer={setServer} tlsPortWarning={tlsPortWarning} />
          )}
          {!loading && step === 'tgbot' && (
            <TGBotStep tgbot={tgbot} setTgbot={setTgbot} />
          )}
          {!loading && step === 'review' && (
            <ReviewStep server={server} tgbot={tgbot} />
          )}

          <div className="mt-6 flex items-center justify-between gap-2">
            <div>
              {step !== 'server' && (
                <Button plain onClick={() => setStep(step === 'review' ? 'tgbot' : 'server')}>
                  {t('wizard.back')}
                </Button>
              )}
            </div>
            <div className="flex items-center gap-2">
              {step === 'tgbot' && (
                <Button plain onClick={() => setStep('review')}>{t('wizard.skip')}</Button>
              )}
              {step !== 'review' && (
                <Button
                  color="indigo"
                  disabled={step === 'server' ? !serverStepValid : !tgbotStepValid}
                  onClick={() => setStep(step === 'server' ? 'tgbot' : 'review')}
                >
                  {t('wizard.next')}
                </Button>
              )}
              {step === 'review' && (
                <Button color="indigo" disabled={saving} onClick={finish}>
                  {saving ? t('wizard.saving') : t('wizard.finish')}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>
    </>
  );
}

function StepPills({ current }: { current: Step }) {
  const { t } = useTranslation();
  const steps: { key: Step; label: string }[] = [
    { key: 'server', label: t('wizard.stepServer') },
    { key: 'tgbot',  label: t('wizard.stepTGBot') },
    { key: 'review', label: t('wizard.stepReview') },
  ];
  return (
    <ol className="mb-6 flex items-center gap-2 text-xs">
      {steps.map((s, i) => {
        const isCurrent = s.key === current;
        const done = steps.findIndex((x) => x.key === current) > i;
        return (
          <li key={s.key} className="flex items-center gap-2">
            <span
              className={
                'inline-flex h-6 min-w-6 items-center justify-center rounded-full px-2 font-mono ' +
                (isCurrent
                  ? 'bg-indigo-500 text-white'
                  : done
                  ? 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300'
                  : 'bg-zinc-200 text-zinc-500 dark:bg-zinc-700')
              }
            >
              {i + 1}
            </span>
            <span className={isCurrent ? 'font-medium text-zinc-900 dark:text-zinc-100' : 'text-zinc-500'}>
              {s.label}
            </span>
            {i < steps.length - 1 && <span className="mx-1 text-zinc-400">›</span>}
          </li>
        );
      })}
    </ol>
  );
}

interface ServerStepProps {
  server: DraftServer;
  setServer: (v: DraftServer) => void;
  tlsPortWarning: string | null;
}
function ServerStep({ server, setServer, tlsPortWarning }: ServerStepProps) {
  const { t } = useTranslation();
  return (
    <Fieldset>
      <Legend className="sr-only">{t('wizard.stepServer')}</Legend>
      <FieldGroup>
        <Field>
          <Label>{t('wizard.serverDomainLabel')}</Label>
          <Input
            required
            placeholder={t('wizard.serverDomainPlaceholder')}
            value={server.domain}
            onChange={(e) => setServer({ ...server, domain: e.target.value })}
          />
          <div className="mt-1 text-xs text-zinc-500">{t('wizard.serverDomainHelp')}</div>
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-[2fr_1fr]">
          <Field>
            <Label>{t('wizard.serverPanelBindLabel')}</Label>
            <Input
              value={server.panelBind}
              onChange={(e) => setServer({ ...server, panelBind: e.target.value })}
            />
            <div className="mt-1 text-xs text-zinc-500">{t('wizard.serverPanelBindHelp')}</div>
          </Field>
          <Field>
            <Label>{t('wizard.serverPanelPortLabel')}</Label>
            <Input
              type="number"
              min={1}
              max={65535}
              value={server.panelPort}
              onChange={(e) => setServer({ ...server, panelPort: Number(e.target.value) || 0 })}
            />
            <div className="mt-1 text-xs text-zinc-500">{t('wizard.serverPanelPortHelp')}</div>
          </Field>
        </div>
        {tlsPortWarning && (
          <div className="rounded-xl border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-200">
            {tlsPortWarning}
          </div>
        )}
        <SwitchField>
          <Label>{t('wizard.tlsEnableLabel')}</Label>
          <Switch
            checked={server.acmeEnabled}
            onChange={(v) => setServer({ ...server, acmeEnabled: v })}
          />
          <div className="mt-1 text-xs text-zinc-500">{t('wizard.tlsEnableHelp')}</div>
        </SwitchField>
        {server.acmeEnabled && (
          <Field>
            <Label>{t('wizard.tlsEmailLabel')}</Label>
            <Input
              type="email"
              value={server.acmeEmail}
              onChange={(e) => setServer({ ...server, acmeEmail: e.target.value })}
            />
            <div className="mt-1 text-xs text-zinc-500">{t('wizard.tlsEmailHelp')}</div>
          </Field>
        )}
      </FieldGroup>
    </Fieldset>
  );
}

interface TGBotStepProps { tgbot: DraftTGBot; setTgbot: (v: DraftTGBot) => void }
function TGBotStep({ tgbot, setTgbot }: TGBotStepProps) {
  const { t } = useTranslation();
  const ids = parseAdminIds(tgbot.adminIdsText);
  const needsIds = tgbot.token.trim() !== '' && ids.length === 0;
  return (
    <Fieldset>
      <Legend className="sr-only">{t('wizard.stepTGBot')}</Legend>
      <div className="mb-3 text-xs text-zinc-500">{t('wizard.tgbotIntro')}</div>
      <FieldGroup>
        <Field>
          <Label>{t('wizard.tgbotTokenLabel')}</Label>
          <Input
            type="password"
            placeholder={t('wizard.tgbotTokenPlaceholder')}
            value={tgbot.token}
            onChange={(e) => setTgbot({ ...tgbot, token: e.target.value })}
            autoComplete="off"
          />
          <div className="mt-1 text-xs text-zinc-500">{t('wizard.tgbotTokenHelp')}</div>
        </Field>
        <Field>
          <Label>{t('wizard.tgbotAdminIdsLabel')}</Label>
          <Input
            placeholder={t('wizard.tgbotAdminIdsPlaceholder')}
            value={tgbot.adminIdsText}
            onChange={(e) => setTgbot({ ...tgbot, adminIdsText: e.target.value })}
          />
          <div className="mt-1 text-xs text-zinc-500">{t('wizard.tgbotAdminIdsHelp')}</div>
          {needsIds && (
            <div className="mt-1 text-xs text-red-600 dark:text-red-400">
              {t('wizard.tgbotRequireIds')}
            </div>
          )}
        </Field>
      </FieldGroup>
    </Fieldset>
  );
}

interface ReviewStepProps { server: DraftServer; tgbot: DraftTGBot }
function ReviewStep({ server, tgbot }: ReviewStepProps) {
  const { t } = useTranslation();
  const adminCount = parseAdminIds(tgbot.adminIdsText).length;
  const tgbotEnabled = tgbot.token.trim() !== '';
  return (
    <div className="space-y-4">
      <div className="rounded-xl border border-indigo-500/30 bg-indigo-500/5 p-3 text-xs text-indigo-800 dark:text-indigo-200">
        {t('wizard.reviewIntro')}
      </div>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-zinc-500">{t('wizard.reviewDomain')}</dt>
        <dd className="font-mono">{server.domain}</dd>
        <dt className="text-zinc-500">{t('wizard.reviewBind')}</dt>
        <dd className="font-mono">{server.panelBind}</dd>
        <dt className="text-zinc-500">{t('wizard.reviewPort')}</dt>
        <dd className="font-mono">{server.panelPort}</dd>
        <dt className="text-zinc-500">{t('wizard.reviewTls')}</dt>
        <dd>{
          server.acmeEnabled
            ? t('wizard.reviewTlsOn', { email: server.acmeEmail })
            : t('wizard.reviewTlsOff')
        }</dd>
        <dt className="text-zinc-500">{t('wizard.reviewTGBot')}</dt>
        <dd>{
          tgbotEnabled
            ? t('wizard.reviewTGBotOn', { count: adminCount })
            : t('wizard.reviewTGBotOff')
        }</dd>
      </dl>
      <div className="rounded-xl border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-800 dark:text-amber-200">
        {t('wizard.restartHint')}
      </div>
    </div>
  );
}
