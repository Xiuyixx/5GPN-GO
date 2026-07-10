import { useEffect, useRef, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Select } from '../components/ui/select';
import { Input } from '../components/ui/input';

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
  const esRef = useRef<EventSource | null>(null);

  function connect(u: string) {
    esRef.current?.close();
    setLines([]);
    setConnected(false);
    const es = new EventSource(`/api/v1/events/logs?unit=${encodeURIComponent(u)}`);
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
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
      <Heading>Logs</Heading>
      <Text className="mt-1">Live tail via SSE. M2 S4 will pipe real journalctl output; today the stream is synthetic.</Text>

      <form className="mt-6 rounded-xl border border-zinc-200 bg-white p-5 shadow-sm dark:border-zinc-800 dark:bg-zinc-900" onSubmit={(e) => e.preventDefault()}>
        <Fieldset>
          <Legend>Stream {connected ? '(connected)' : '(disconnected)'}</Legend>
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

      <pre className="mt-6 max-h-96 overflow-auto rounded-xl border border-zinc-200 bg-zinc-950 p-4 text-xs text-zinc-100">
        {filtered.length === 0
          ? <span className="text-zinc-500">waiting for stream…</span>
          : filtered.map((l) => (
              <div key={l.seq}>
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
    </AppShell>
  );
}
