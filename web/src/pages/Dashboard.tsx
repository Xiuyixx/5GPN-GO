import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Badge } from '../components/ui/badge';
import { Divider } from '../components/ui/divider';
import { api } from '../api/client';
import type { ExitsResponse, MetricsSample } from '../api/client';
import DNSPlaneCard from './DashboardDNS';

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

function Tile({ title, value, hint, accent }: { title: string; value: string; hint?: string; accent?: string }) {
  return (
    <div className="glass tile relative overflow-hidden p-5">
      <div
        aria-hidden
        className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
        style={{ background: accent ?? 'radial-gradient(60% 100% at 30% 0%, rgb(99 102 241 / 0.35), transparent 70%)' }}
      />
      <div className="relative">
        <div className="text-xs font-medium uppercase tracking-wider text-zinc-500">{title}</div>
        <div className="metric mt-3 text-3xl font-semibold text-zinc-900 dark:text-zinc-50">{value}</div>
        {hint && <div className="mt-1 text-xs text-zinc-400 dark:text-zinc-500">{hint}</div>}
      </div>
    </div>
  );
}

const TILE_ACCENTS = [
  'radial-gradient(60% 100% at 30% 0%, rgb(99 102 241 / 0.35), transparent 70%)',
  'radial-gradient(60% 100% at 30% 0%, rgb(20 184 166 / 0.35), transparent 70%)',
  'radial-gradient(60% 100% at 30% 0%, rgb(236 72 153 / 0.30), transparent 70%)',
  'radial-gradient(60% 100% at 30% 0%, rgb(250 204 21 / 0.30), transparent 70%)',
];

export default function Dashboard() {
  const { t } = useTranslation();
  const [samples, setSamples] = useState<MetricsSample[]>([]);
  const [exits, setExits] = useState<ExitsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function refresh() {
    try {
      const [m, e] = await Promise.all([
        api.get<MetricsSample[]>('/api/v1/metrics'),
        api.get<ExitsResponse>('/api/v1/exits'),
      ]);
      setSamples(m);
      setExits(e);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, []);

  const latest = samples.length ? samples[samples.length - 1] : null;

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('nav.dashboard')}</Heading>
        <Text className="mt-1">{t('dashboard.subtitle')}</Text>
        {err && <Text className="mt-2 text-red-600">{t('dashboard.errorLabel', { message: err })}</Text>}
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          title={t('dashboard.activeExitTitle')}
          value={exits?.active ?? '—'}
          hint={exits ? t('dashboard.exitsConfigured', { count: exits.exits.length }) : ''}
          accent={TILE_ACCENTS[0]}
        />
        <Tile
          title={t('dashboard.connectionsTitle')}
          value={latest ? String(latest.conns) : '—'}
          hint={t('dashboard.liveCount')}
          accent={TILE_ACCENTS[1]}
        />
        <Tile
          title={t('dashboard.cpu')}
          value={latest ? `${latest.cpu.toFixed(1)}%` : '—'}
          hint={t('dashboard.cpuWindow')}
          accent={TILE_ACCENTS[2]}
        />
        <Tile
          title={t('dashboard.memoryTitle')}
          value={latest ? fmtBytes(latest.mem_bytes) : '—'}
          hint={t('dashboard.residentSet')}
          accent={TILE_ACCENTS[3]}
        />
      </div>

      <div className="mt-8 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="glass p-5">
          <Heading level={3}>{t('dashboard.recentSamples')}</Heading>
          <Divider className="my-3" />
          <div className="-mx-2 max-h-72 overflow-auto text-sm">
            <table className="w-full min-w-[520px] text-left">
              <thead>
                <tr className="text-zinc-500">
                  <th className="py-2 font-medium">{t('dashboard.timeColumn')}</th>
                  <th className="py-2 font-medium">{t('dashboard.cpu')}</th>
                  <th className="py-2 font-medium">{t('dashboard.connsColumn')}</th>
                  <th className="py-2 font-medium">{t('dashboard.txColumn')}</th>
                  <th className="py-2 font-medium">{t('dashboard.rxColumn')}</th>
                </tr>
              </thead>
              <tbody className="metric">
                {samples.slice(-12).reverse().map((s) => (
                  <tr key={s.ts} className="border-t border-zinc-100 dark:border-zinc-800/60">
                    <td className="py-2 text-zinc-500">{new Date(s.ts).toLocaleTimeString()}</td>
                    <td className="py-2">{s.cpu.toFixed(1)}%</td>
                    <td className="py-2">{s.conns}</td>
                    <td className="py-2">{fmtBytes(s.tx_bytes)}</td>
                    <td className="py-2">{fmtBytes(s.rx_bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        <div className="glass p-5">
          <Heading level={3}>{t('nav.exits')}</Heading>
          <Divider className="my-3" />
          <ul className="space-y-2 text-sm">
            {exits?.exits.map((e) => (
              <li key={e.id} className="flex items-center justify-between rounded-lg px-2 py-1.5 transition-colors hover:bg-white/40 dark:hover:bg-white/5">
                <span className="font-medium">
                  {e.id} <span className="ml-1 font-normal text-zinc-500">· {e.protocol}</span>
                </span>
                {e.active
                  ? <Badge color="lime">{t('dashboard.badgeActive')}</Badge>
                  : <Badge color="zinc">{t('dashboard.badgeStandby')}</Badge>}
              </li>
            ))}
          </ul>
        </div>
      </div>

      <div className="mt-8">
        <DNSPlaneCard />
      </div>
    </AppShell>
  );
}
