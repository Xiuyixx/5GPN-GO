import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Divider } from '../components/ui/divider';
import { getDNSMetrics } from '../api/client';
import type { DNSListenerHealth, DNSMetrics } from '../api/client';
import { pollSerially } from '../api/poll';

const POLL_MS = 5000;
// 5 minutes of history at one sample per poll tick.
const HISTORY_WINDOW = Math.ceil((5 * 60 * 1000) / POLL_MS) + 1;

interface Sample {
  ts: number;
  metrics: DNSMetrics;
}

const TRANSPORTS: { key: keyof DNSMetrics['listeners']; label: string }[] = [
  { key: 'udp53', label: 'UDP :53' },
  { key: 'tcp53', label: 'TCP :53' },
  { key: 'dot', label: 'DoT :853' },
  { key: 'doh', label: 'DoH :443' },
];

function lightColor(status: DNSListenerHealth): string {
  switch (status) {
    case 'healthy':
      return 'bg-green-500';
    case 'degraded':
      return 'bg-red-500';
    default:
      return 'bg-zinc-400 dark:bg-zinc-600';
  }
}

function certColor(days: number): string {
  if (days > 30) return 'text-green-600 dark:text-green-400';
  if (days > 7) return 'text-amber-600 dark:text-amber-400';
  return 'text-red-600 dark:text-red-400';
}

export function calculateQPS(previous: Sample | null, latest: Sample | null): number {
  if (!previous || !latest) return 0;
  const queryDelta = Math.max(0, latest.metrics.queries_total - previous.metrics.queries_total);
  const seconds = (latest.ts - previous.ts) / 1000;
  return seconds > 0 ? queryDelta / seconds : 0;
}

export default function DNSPlaneCard() {
  const { t } = useTranslation();
  const [history, setHistory] = useState<Sample[]>([]);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void pollSerially(async (signal) => {
      try {
        const m = await getDNSMetrics();
        if (signal.aborted) return;
        setHistory((prev) => {
          const next = [...prev, { ts: Date.now(), metrics: m }];
          return next.slice(-HISTORY_WINDOW);
        });
        setErr(null);
      } catch (e) {
        if (!signal.aborted) setErr(e instanceof Error ? e.message : String(e));
      }
    }, POLL_MS, controller.signal);
    return () => controller.abort();
  }, []);

  const latest = history.length ? history[history.length - 1] : null;
  const prev = history.length > 1 ? history[history.length - 2] : null;
  const oldest = history.length ? history[0] : null;

  // Current QPS: queries_total delta over the last poll interval / 5.
  const qps = calculateQPS(prev, latest);

  // Last-5-minute category hit distribution: delta across the retained window.
  const windowBlock = latest && oldest ? Math.max(0, latest.metrics.hits_block - oldest.metrics.hits_block) : 0;
  const windowDirect = latest && oldest ? Math.max(0, latest.metrics.hits_direct - oldest.metrics.hits_direct) : 0;
  const windowProxy = latest && oldest ? Math.max(0, latest.metrics.hits_proxy - oldest.metrics.hits_proxy) : 0;
  const windowTotal = windowBlock + windowDirect + windowProxy;
  const pct = (n: number) => (windowTotal > 0 ? (n / windowTotal) * 100 : 0);

  const windowUpstreamErrors =
    latest && oldest ? Math.max(0, latest.metrics.upstream_errors - oldest.metrics.upstream_errors) : 0;

  const cert = latest?.metrics.cert ?? null;

  return (
    <div className="glass p-5">
      <div className="flex items-center justify-between">
        <Heading level={3}>{t('dashboard.dnsPlaneTitle')}</Heading>
        <div className="metric text-lg font-semibold text-zinc-900 dark:text-zinc-50">
          {qps.toFixed(1)} <span className="text-xs font-normal text-zinc-500">{t('dashboard.dnsQPS')}</span>
        </div>
      </div>
      <Divider className="my-3" />
      {err && <Text className="mb-3 text-red-600">{t('dashboard.errorLabel', { message: err })}</Text>}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <div className="text-xs font-medium uppercase tracking-wider text-zinc-500">
            {t('dashboard.dnsHitsBlock')} / {t('dashboard.dnsHitsDirect')} / {t('dashboard.dnsHitsProxy')}
          </div>
          <div className="mt-2 flex h-3 w-full overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
            <div className="h-full bg-red-500" style={{ width: `${pct(windowBlock)}%` }} title={t('dashboard.dnsHitsBlock')} />
            <div className="h-full bg-sky-500" style={{ width: `${pct(windowDirect)}%` }} title={t('dashboard.dnsHitsDirect')} />
            <div className="h-full bg-indigo-500" style={{ width: `${pct(windowProxy)}%` }} title={t('dashboard.dnsHitsProxy')} />
          </div>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-zinc-500">
            <span><span className="mr-1 inline-block h-2 w-2 rounded-full bg-red-500" />{t('dashboard.dnsHitsBlock')}: {windowBlock}</span>
            <span><span className="mr-1 inline-block h-2 w-2 rounded-full bg-sky-500" />{t('dashboard.dnsHitsDirect')}: {windowDirect}</span>
            <span><span className="mr-1 inline-block h-2 w-2 rounded-full bg-indigo-500" />{t('dashboard.dnsHitsProxy')}: {windowProxy}</span>
          </div>
        </div>

        <div>
          <div className="text-xs font-medium uppercase tracking-wider text-zinc-500">
            {t('dashboard.dnsPlaneTitle')}
          </div>
          <div className="mt-2 flex flex-wrap gap-3">
            {TRANSPORTS.map(({ key, label }) => {
              const status = latest?.metrics.listeners[key] ?? 'not_configured';
              const statusKey =
                status === 'healthy'
                  ? 'dashboard.dnsListenerHealthy'
                  : status === 'degraded'
                    ? 'dashboard.dnsListenerDegraded'
                    : 'dashboard.dnsListenerNotConfigured';
              return (
                <div key={key} className="flex items-center gap-1.5 text-sm" title={t(statusKey)}>
                  <span className={`inline-block h-2.5 w-2.5 rounded-full ${lightColor(status)}`} aria-hidden />
                  <span className="text-zinc-700 dark:text-zinc-300">{label}</span>
                </div>
              );
            })}
          </div>
        </div>
      </div>

      <Divider className="my-3" />

      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="text-sm">
          {cert ? (
            <span className={certColor(cert.days_until_expiry)}>
              {t('dashboard.dnsCertExpiry', { days: cert.days_until_expiry })}
            </span>
          ) : (
            <span className="text-zinc-400">—</span>
          )}
        </div>
        <div className="text-sm text-zinc-500">
          {t('dashboard.dnsUpstreamErrors')}: {windowUpstreamErrors}
        </div>
      </div>
    </div>
  );
}
