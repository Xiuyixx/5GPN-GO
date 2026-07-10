import { useEffect, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Badge } from '../components/ui/badge';
import { Divider } from '../components/ui/divider';
import { api } from '../api/client';
import type { ExitsResponse, MetricsSample } from '../api/client';

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

function Tile({ title, value, hint }: { title: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border border-zinc-200 bg-white p-5 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
      <div className="text-sm text-zinc-500 dark:text-zinc-400">{title}</div>
      <div className="mt-2 text-2xl font-semibold tabular-nums">{value}</div>
      {hint && <div className="mt-1 text-xs text-zinc-400 dark:text-zinc-500">{hint}</div>}
    </div>
  );
}

export default function Dashboard() {
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
      <Heading>Dashboard</Heading>
      <Text className="mt-1">Live metrics update every 5s. Backend samples are stubbed until M2 S4 wires the real collector.</Text>
      {err && <Text className="mt-2 text-red-600">Error: {err}</Text>}

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          title="Active exit"
          value={exits?.active ?? '—'}
          hint={exits ? `${exits.exits.length} exits configured` : ''}
        />
        <Tile
          title="Live connections"
          value={latest ? String(latest.conns) : '—'}
        />
        <Tile
          title="CPU (5s)"
          value={latest ? `${latest.cpu.toFixed(1)}%` : '—'}
        />
        <Tile
          title="Memory"
          value={latest ? fmtBytes(latest.mem_bytes) : '—'}
        />
      </div>

      <div className="mt-8 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="rounded-xl border border-zinc-200 bg-white p-5 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
          <Heading level={3}>Recent samples</Heading>
          <Divider className="my-3" />
          <div className="max-h-72 overflow-auto text-sm">
            <table className="w-full text-left">
              <thead>
                <tr className="text-zinc-500">
                  <th>Time</th><th>CPU</th><th>Conns</th><th>Tx</th><th>Rx</th>
                </tr>
              </thead>
              <tbody>
                {samples.slice(-12).reverse().map((s) => (
                  <tr key={s.ts}>
                    <td>{new Date(s.ts).toLocaleTimeString()}</td>
                    <td>{s.cpu.toFixed(1)}%</td>
                    <td>{s.conns}</td>
                    <td>{fmtBytes(s.tx_bytes)}</td>
                    <td>{fmtBytes(s.rx_bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        <div className="rounded-xl border border-zinc-200 bg-white p-5 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
          <Heading level={3}>Exits</Heading>
          <Divider className="my-3" />
          <ul className="space-y-2 text-sm">
            {exits?.exits.map((e) => (
              <li key={e.id} className="flex items-center justify-between">
                <span>{e.id} <span className="text-zinc-500">({e.protocol})</span></span>
                {e.active
                  ? <Badge color="lime">active</Badge>
                  : <Badge color="zinc">standby</Badge>}
              </li>
            ))}
          </ul>
        </div>
      </div>
    </AppShell>
  );
}
