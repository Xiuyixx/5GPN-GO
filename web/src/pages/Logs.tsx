import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Select } from '../components/ui/select';
import { Input } from '../components/ui/input';
import { useAuthStore } from '../stores/auth';

const UNITS = ['5gpn', 'dnsdist', 'mihomo', 'sniproxy'];

// PRESETS maps a preset key to the regex-alternation the operator wants
// the log stream narrowed to. Each value is a case-insensitive regex
// snippet; the empty string means "no filter". The dropdown just sets
// the `filter` state var — the client already streams the full unit
// via SSE and filters locally, so there's zero server-side contract
// change here.
//
// The regexes intentionally use the same substrings the daemon logs
// use as slog attribute names ("component=frontdoor", "component=acme",
// "apply:", etc.) so they overlap with journalctl grep patterns.
const PRESETS: Record<string, string> = {
  all: '',
  frontdoor: 'frontdoor|resolver',
  acme: 'acme|certmagic|obtain|challenge',
  ios: 'mobileconfig|ios_profile|profile_url|iospreflight',
  mtproxy: 'mtproxy',
  pathb: 'sniforward|quicforward|spoof',
  rulesapply: 'apply:|rebuildAndPublish|resolver.Publish',
};

interface Line {
  ts: string;
  level: string;
  msg: string;
  unit: string;
  seq: number;
}

export default function Logs() {
  const { t } = useTranslation();
  const [unit, setUnit] = useState('5gpn');
  const [filter, setFilter] = useState('');
  const [preset, setPreset] = useState('all');
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
      setErrDetail(t('logs.errorNotSignedIn'));
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
        setErrDetail(t('logs.errorConnectionClosed'));
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

  // Compile the current filter as a case-insensitive regex. If the
  // user typed something that isn't valid regex syntax (a stray "["
  // etc.), fall back to plain substring matching so the input keeps
  // working as it did before this enhancement.
  const matcher = useMemo(() => {
    if (!filter) return null;
    try {
      const re = new RegExp(filter, 'i');
      return (s: string) => re.test(s);
    } catch {
      const needle = filter.toLowerCase();
      return (s: string) => s.toLowerCase().includes(needle);
    }
  }, [filter]);

  const filtered = matcher ? lines.filter((l) => matcher(l.msg)) : lines;

  function applyPreset(next: string) {
    setPreset(next);
    setFilter(PRESETS[next] ?? '');
  }

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('logs.title')}</Heading>
        <Text className="mt-1">{t('logs.subtitle')}</Text>
      </div>

      <form className="glass p-6" onSubmit={(e) => e.preventDefault()}>
        <Fieldset>
          <Legend>
            {t('logs.streamLabel')}
            <span className={`ml-2 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium ${
              connected
                ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
                : 'bg-red-500/15 text-red-700 dark:text-red-300'
            }`}>
              <span className={`h-1.5 w-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'} ${connected ? 'animate-pulse' : ''}`} />
              {connected ? t('logs.statusConnected') : t('logs.statusDisconnected')}
            </span>
          </Legend>
          {errDetail && (
            <div className="mt-2 rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-xs text-red-800 dark:text-red-200">
              {errDetail}
            </div>
          )}
          <FieldGroup className="sm:grid sm:grid-cols-3 sm:gap-4">
            <Field>
              <Label>{t('logs.unitLabel')}</Label>
              <Select value={unit} onChange={(e) => setUnit(e.target.value)}>
                {UNITS.map((u) => <option key={u} value={u}>{u}</option>)}
              </Select>
            </Field>
            <Field>
              <Label>{t('logs.presetLabel')}</Label>
              <Select value={preset} onChange={(e) => applyPreset(e.target.value)}>
                <option value="all">{t('logs.presetAll')}</option>
                <option value="frontdoor">{t('logs.presetFrontdoor')}</option>
                <option value="acme">{t('logs.presetACME')}</option>
                <option value="ios">{t('logs.presetIOS')}</option>
                <option value="mtproxy">{t('logs.presetMTProxy')}</option>
                <option value="pathb">{t('logs.presetPathB')}</option>
                <option value="rulesapply">{t('logs.presetRulesApply')}</option>
              </Select>
            </Field>
            <Field>
              <Label>{t('logs.filterLabel')}</Label>
              <Input
                value={filter}
                onChange={(e) => { setFilter(e.target.value); setPreset('all'); }}
                placeholder={t('logs.filterPlaceholder')}
              />
            </Field>
          </FieldGroup>
        </Fieldset>
        <div className="mt-4 flex gap-2">
          <Button plain onClick={() => setLines([])}>{t('logs.clearButton')}</Button>
          <Button plain onClick={() => connect(unit)}>{t('logs.reconnectButton')}</Button>
        </div>
      </form>

      <div className="glass mt-6 overflow-hidden">
        <pre className="max-h-[28rem] overflow-auto rounded-2xl bg-zinc-950/70 p-4 text-xs text-zinc-100 backdrop-blur">
          {filtered.length === 0
            ? <span className="text-zinc-500">{t('logs.waitingForStream')}</span>
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
