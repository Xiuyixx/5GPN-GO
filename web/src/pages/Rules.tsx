import { useEffect, useMemo, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { api } from '../api/client';
import type { ApplyResponse, DryRunResponse, Rule, RuleKind } from '../api/client';

const KINDS: RuleKind[] = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD',
  'GEOSITE', 'GEOIP', 'IP-CIDR', 'RULE-SET', 'MATCH',
];

const DEFAULT_FIXTURES = [
  { domain: 'baidu.cn',    expected_exit: 'direct' },
  { domain: 'example.com', expected_exit: 'wg1' },
];

interface Draft extends Rule {
  _key: string;
}

let keyCounter = 0;
function makeKey(): string {
  keyCounter += 1;
  return `r${Date.now().toString(36)}-${keyCounter}`;
}

function toDraft(r: Rule): Draft {
  return { ...r, _key: makeKey() };
}

export default function Rules() {
  const [active, setActive] = useState<Rule[]>([]);
  const [draft, setDraft] = useState<Draft[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [applyRes, setApplyRes] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState<'idle' | 'dryrun' | 'apply'>('idle');
  const [note, setNote] = useState('');
  const [dryOk, setDryOk] = useState(false);

  async function refresh() {
    try {
      const { rules } = await api.get<{ rules: Rule[] }>('/api/v1/rules');
      const list = rules ?? [];
      setActive(list);
      setDraft(list.map(toDraft));
      setDry(null);
      setApplyRes(null);
      setDryOk(false);
      setEditing(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => { refresh(); }, []);

  const isDirty = useMemo(() => {
    if (draft.length !== active.length) return true;
    return draft.some((r, i) => {
      const a = active[i];
      return !a
        || r.id !== a.id || r.kind !== a.kind || r.pattern !== a.pattern
        || r.action !== a.action || r.priority !== a.priority || r.enabled !== a.enabled;
    });
  }, [draft, active]);

  function invalidate() {
    setDry(null);
    setDryOk(false);
    setApplyRes(null);
  }

  function patch(key: string, changes: Partial<Rule>) {
    setDraft((d) => d.map((r) => (r._key === key ? { ...r, ...changes } : r)));
    invalidate();
  }

  function addRow() {
    const priority = draft.length
      ? Math.max(...draft.map((r) => r.priority)) + 10
      : 10;
    const _key = makeKey();
    setDraft((d) => [
      ...d,
      { _key, id: `rule-${d.length + 1}`, kind: 'DOMAIN-SUFFIX', pattern: '', action: 'direct', priority, enabled: true },
    ]);
    setEditing(_key);
    invalidate();
  }

  function removeRow(key: string) {
    setDraft((d) => d.filter((r) => r._key !== key));
    if (editing === key) setEditing(null);
    invalidate();
  }

  function move(key: string, dir: -1 | 1) {
    setDraft((d) => {
      const idx = d.findIndex((r) => r._key === key);
      const target = idx + dir;
      if (idx < 0 || target < 0 || target >= d.length) return d;
      const clone = d.slice();
      [clone[idx], clone[target]] = [clone[target], clone[idx]];
      return clone.map((r, i) => ({ ...r, priority: (i + 1) * 10 }));
    });
    invalidate();
  }

  function stripDrafts(list: Draft[]): Rule[] {
    return list.map(({ _key, ...r }) => r);
  }

  async function runDryRun() {
    setError(null); setBusy('dryrun'); setDry(null);
    try {
      const res = await api.post<DryRunResponse>('/api/v1/rules/dry-run', {
        rules: stripDrafts(draft),
        fixtures: DEFAULT_FIXTURES,
      });
      setDry(res);
      setDryOk(res.failed === 0);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy('idle');
    }
  }

  async function runApply() {
    setError(null); setBusy('apply'); setApplyRes(null);
    try {
      const res = await api.post<ApplyResponse>('/api/v1/rules/apply', {
        rules: stripDrafts(draft),
        note: note || 'panel edit',
      });
      setApplyRes(res);
      setNote('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy('idle');
    }
  }

  return (
    <AppShell>
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div>
          <Heading>Rules</Heading>
          <Text className="mt-1">
            Click any row to edit · Dry-run must pass before Apply is enabled.
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button plain onClick={refresh}>Reset</Button>
          <Button plain onClick={addRow}>+ Add rule</Button>
          <Button color="zinc" onClick={runDryRun} disabled={busy !== 'idle'}>
            {busy === 'dryrun' ? 'Running…' : 'Dry-run'}
          </Button>
          <Button color="indigo" onClick={runApply} disabled={busy !== 'idle' || !isDirty || !dryOk}>
            {busy === 'apply' ? 'Applying…' : 'Apply'}
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-4 flex items-start justify-between gap-3 rounded-xl border border-red-400/40 bg-red-500/10 px-4 py-3 text-sm text-red-800 dark:text-red-200">
          <div>
            <div className="font-semibold">Something went wrong</div>
            <div className="mt-0.5">{error}</div>
          </div>
          <button
            type="button"
            onClick={() => setError(null)}
            aria-label="dismiss error"
            className="shrink-0 rounded px-2 py-0.5 text-red-700/70 hover:text-red-900 dark:text-red-300/70 dark:hover:text-red-100"
          >
            ✕
          </button>
        </div>
      )}
      {applyRes && (
        <div className={`mb-4 flex items-start justify-between gap-3 rounded-xl border px-4 py-3 text-sm ${
          applyRes.rolled_back
            ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200'
            : 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200'
        }`}>
          <div>
            <div className="font-semibold">
              {applyRes.rolled_back ? 'Apply rolled back' : 'Apply succeeded'}
            </div>
            <div className="mt-0.5">
              snapshot #{applyRes.snapshot_id} · health {applyRes.health}
              {applyRes.rolled_back ? ' · previous config restored' : ''}
            </div>
          </div>
          <button
            type="button"
            onClick={() => setApplyRes(null)}
            aria-label="dismiss apply result"
            className="shrink-0 rounded px-2 py-0.5 opacity-70 hover:opacity-100"
          >
            ✕
          </button>
        </div>
      )}
      {isDirty && !applyRes && (
        <div className={`mb-4 rounded-xl border px-4 py-3 text-sm ${
          dryOk
            ? 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200'
            : dry
              ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200'
              : 'border-amber-400/40 bg-amber-500/10 text-amber-800 dark:text-amber-200'
        }`}>
          <div className="font-semibold">Unsaved changes</div>
          <div className="mt-0.5">
            {dryOk
              ? 'Dry-run passed — Apply is enabled.'
              : dry
                ? `Dry-run failed (${dry.failed} fixture${dry.failed === 1 ? '' : 's'}). Fix rules or adjust priorities and re-run.`
                : 'Run Dry-run before Apply to guard against regressions.'}
          </div>
        </div>
      )}

      <div className="glass p-2">
        {draft.length === 0 ? (
          <div className="p-6 text-center">
            <Text>No rules yet. Click <strong>+ Add rule</strong> above.</Text>
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeader className="w-16">Priority</TableHeader>
                <TableHeader>ID</TableHeader>
                <TableHeader>Kind</TableHeader>
                <TableHeader>Pattern</TableHeader>
                <TableHeader>Action</TableHeader>
                <TableHeader className="w-24">Status</TableHeader>
                <TableHeader className="w-40 text-right">Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {draft.map((r, i) => (
                <TableRow key={r._key} className="align-middle">
                  <TableCell className="metric text-zinc-500">{r.priority}</TableCell>
                  <TableCell className="font-medium">{r.id}</TableCell>
                  <TableCell>
                    <Badge color="zinc">{r.kind}</Badge>
                  </TableCell>
                  <TableCell className="text-zinc-600 dark:text-zinc-300">
                    {r.pattern
                      ? <code className="text-xs">{r.pattern}</code>
                      : <span className="text-zinc-400">—</span>}
                  </TableCell>
                  <TableCell>{r.action}</TableCell>
                  <TableCell>
                    <button
                      type="button"
                      onClick={() => patch(r._key, { enabled: !r.enabled })}
                      className="cursor-pointer"
                      aria-label={r.enabled ? 'disable rule' : 'enable rule'}
                    >
                      <Badge color={r.enabled ? 'lime' : 'zinc'}>{r.enabled ? 'on' : 'off'}</Badge>
                    </button>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button plain aria-label="move up" onClick={() => move(r._key, -1)} disabled={i === 0}>↑</Button>
                      <Button plain aria-label="move down" onClick={() => move(r._key, 1)} disabled={i === draft.length - 1}>↓</Button>
                      <Button plain onClick={() => setEditing(editing === r._key ? null : r._key)}>
                        {editing === r._key ? 'Close' : 'Edit'}
                      </Button>
                      <Button plain aria-label="delete" onClick={() => removeRow(r._key)}>✕</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>

      {editing && (() => {
        const r = draft.find((x) => x._key === editing);
        if (!r) return null;
        const isMatch = r.kind === 'MATCH';
        return (
          <div className="glass fade-up mt-4 p-6">
            <div className="mb-4 flex items-center justify-between">
              <Heading level={2}>Edit rule</Heading>
              <Button plain onClick={() => setEditing(null)}>Close</Button>
            </div>
            <Fieldset>
              <Legend className="sr-only">Fields</Legend>
              <FieldGroup>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <Field>
                    <Label>ID</Label>
                    <Input value={r.id} onChange={(e) => patch(r._key, { id: e.target.value })} />
                  </Field>
                  <Field>
                    <Label>Priority</Label>
                    <Input
                      type="number"
                      value={r.priority}
                      onChange={(e) => patch(r._key, { priority: Number(e.target.value) })}
                    />
                  </Field>
                  <Field>
                    <Label>Kind</Label>
                    <Select value={r.kind} onChange={(e) => patch(r._key, { kind: e.target.value as RuleKind })}>
                      {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
                    </Select>
                  </Field>
                  <Field>
                    <Label>Action</Label>
                    <Input
                      value={r.action}
                      onChange={(e) => patch(r._key, { action: e.target.value })}
                      placeholder="direct / wg1 / trojan-jp / ..."
                    />
                  </Field>
                  <Field className="sm:col-span-2">
                    <Label>Pattern {isMatch && <span className="text-zinc-500">(disabled for MATCH)</span>}</Label>
                    <Input
                      value={r.pattern}
                      onChange={(e) => patch(r._key, { pattern: e.target.value })}
                      disabled={isMatch}
                      placeholder={
                        r.kind === 'DOMAIN-SUFFIX' ? 'example.com'
                        : r.kind === 'IP-CIDR' ? '10.0.0.0/8'
                        : r.kind === 'GEOSITE' ? 'cn'
                        : 'pattern'
                      }
                    />
                  </Field>
                </div>
              </FieldGroup>
            </Fieldset>
            <div className="mt-4 flex justify-between">
              <Button plain onClick={() => { removeRow(r._key); }}>Delete rule</Button>
              <Button color="zinc" onClick={() => setEditing(null)}>Done</Button>
            </div>
          </div>
        );
      })()}

      <form className="glass mt-6 p-6" onSubmit={(e) => e.preventDefault()}>
        <Fieldset>
          <Legend>Apply metadata</Legend>
          <FieldGroup>
            <Field>
              <Label>Note (persisted with the snapshot)</Label>
              <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. added Netflix routing" />
            </Field>
          </FieldGroup>
        </Fieldset>
      </form>

      {dry && (
        <div className="mt-8">
          <Heading level={2}>Dry-run · passed {dry.passed} · failed {dry.failed}</Heading>
          <div className="glass mt-4 p-2">
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeader>Domain</TableHeader>
                  <TableHeader>Matched rule</TableHeader>
                  <TableHeader>Kind</TableHeader>
                  <TableHeader>Actual</TableHeader>
                  <TableHeader>Expected</TableHeader>
                  <TableHeader>Status</TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {dry.results.map((r) => (
                  <TableRow key={r.domain}>
                    <TableCell className="font-medium">{r.domain}</TableCell>
                    <TableCell>{r.matched_rule || '—'}</TableCell>
                    <TableCell>{r.matched_kind || '—'}</TableCell>
                    <TableCell>{r.actual_exit || '—'}</TableCell>
                    <TableCell>{r.expected_exit}</TableCell>
                    <TableCell>
                      <Badge color={r.pass ? 'lime' : 'red'}>{r.pass ? 'pass' : r.failure_reason ?? 'fail'}</Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      )}
    </AppShell>
  );
}
