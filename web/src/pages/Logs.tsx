import { useEffect, useRef, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Select } from '../components/ui/select';
import { Input } from '../components/ui/input';
import { useAuthStore } from '../stores/auth';

const UNITS = ['5gpn', 'dnsdist', 'mihomo', 'sniproxy'];

interface Line {
  ts: string;
  level: string;
  msg: string;
  unit: string;
  seq: number;
}

export default function Logs() {
  const [unit, setUnit] = useState('5gpn');
  const [filter, setFilter] = useState('');
  const [lines, setLines] = useState<Line[]>([]);
  const [connected, setConnected] = useState(false);
  const [errDetail, setErrDetail] = useState<string | null>(null);
  const esRef = useRef<EventSource | null>(null);
  const token = useAuthStore((s) => s.token);

  function connect(u: string) {
    esRef.current?.close();
    setLines([]);
    setConnected(false);
    setErrDetail(null);
    if (!token) {
      setErrDetail('not signed in — reload the page and log back in');
      return;
    }
    // EventSource cannot set Authorization headers; the backend accepts
    // ?access_token=<jwt> as a fallback for /api/v1/events/*.
    const url = `/api/v1/events/logs?unit=${encodeURIComponent(u)}&access_token=${encodeURIComponent(token)}`;
    const es = new EventSource(url);
    es.onopen = () => { setConnected(true); setErrDetail(null); };
    es.onerror = () => {
      setConnected(false);
      // EventSource auto-reconnects; the error event fires on every drop.
      // Only surface a message if we never opened at all.
      if (es.readyState === EventSource.CLOSED) {
        setErrDetail('connection closed (auth rejected or server dropped)');
      }
    };
    es.onmessage = (ev) => {
      try {
        const line: Line = JSON.parse(ev.data);
        setLines((prev) => [...prev.slice(-499), line]);
      } catch { /* ignore malformed */ }
    };
    esRef.current = es;
  }

  useEffect(() => {
    connect(unit);
    return () => { esRef.current?.close(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [unit]);

  const filtered = filter
    ? lines.filter((l) => l.msg.toLowerCase().includes(filter.toLowerCase()))
    : lines;

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>Logs</Heading>
        <Text className="mt-1">Live tail via SSE · journalctl piped from the daemon host.</Text>
      </div>

      <form className="glass p-6" onSubmit={(e) => e.preventDefault()}>
        <Fieldset>
          <Legend>
            Stream
            <span className={`ml-2 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium ${
              connected
                ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
                : 'bg-red-500/15 text-red-700 dark:text-red-300'
            }`}>
              <span className={`h-1.5 w-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'} ${connected ? 'animate-pulse' : ''}`} />
              {connected ? 'connected' : 'disconnected'}
            </span>
          </Legend>
          {errDetail && (
            <div className="mt-2 rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-xs text-red-800 dark:text-red-200">
              {errDetail}
            </div>
          )}
          <FieldGroup className="sm:grid sm:grid-cols-2 sm:gap-4">
            <Field>
              <Label>Unit</Label>
              <Select value={unit} onChange={(e) => setUnit(e.target.value)}>
                {UNITS.map((u) => <option key={u} value={u}>{u}</option>)}
              </Select>
            </Field>
            <Field>
              <Label>Filter</Label>
              <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="substring match" />
            </Field>
          </FieldGroup>
        </Fieldset>
        <div className="mt-4 flex gap-2">
          <Button plain onClick={() => setLines([])}>Clear</Button>
          <Button plain onClick={() => connect(unit)}>Reconnect</Button>
        </div>
      </form>

      <div className="glass mt-6 overflow-hidden">
        <pre className="max-h-[28rem] overflow-auto rounded-2xl bg-zinc-950/70 p-4 text-xs text-zinc-100 backdrop-blur">
          {filtered.length === 0
            ? <span className="text-zinc-500">waiting for stream…</span>
            : filtered.map((l) => (
                <div key={l.seq} className="whitespace-pre-wrap">
                  <span className="text-zinc-500">{new Date(l.ts).toLocaleTimeString()}</span>
                  {' '}
                  <span className="text-cyan-300">[{l.unit}]</span>
                  {' '}
                  <span className="text-emerald-300">{l.level}</span>
                  {' '}
                  {l.msg}
                </div>
              ))
          }
        </pre>
      </div>
    </AppShell>
  );
}
