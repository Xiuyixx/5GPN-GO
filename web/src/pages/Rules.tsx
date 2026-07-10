import { useEffect, useMemo, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
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
  _key: string; // stable key for React lists across edits
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
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [applyRes, setApplyRes] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState<'idle' | 'dryrun' | 'apply'>('idle');
  const [note, setNote] = useState('');
  const [dryOk, setDryOk] = useState(false); // gate Apply behind at least one dry-run pass

  async function refresh() {
    try {
      const { rules } = await api.get<{ rules: Rule[] }>('/api/v1/rules');
      const list = rules ?? [];
      setActive(list);
      setDraft(list.map(toDraft));
      setDry(null);
      setApplyRes(null);
      setDryOk(false);
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

  function patch(key: string, changes: Partial<Rule>) {
    setDraft((d) => d.map((r) => (r._key === key ? { ...r, ...changes } : r)));
    setDry(null); setDryOk(false); setApplyRes(null);
  }

  function addRow() {
    const priority = draft.length
      ? Math.max(...draft.map((r) => r.priority)) + 10
      : 10;
    setDraft((d) => [
      ...d,
      { _key: makeKey(), id: `rule-${d.length + 1}`, kind: 'DOMAIN-SUFFIX', pattern: '', action: 'direct', priority, enabled: true },
    ]);
    setDry(null); setDryOk(false);
  }

  function removeRow(key: string) {
    setDraft((d) => d.filter((r) => r._key !== key));
    setDry(null); setDryOk(false);
  }

  function move(key: string, dir: -1 | 1) {
    setDraft((d) => {
      const idx = d.findIndex((r) => r._key === key);
      const target = idx + dir;
      if (idx < 0 || target < 0 || target >= d.length) return d;
      const clone = d.slice();
      [clone[idx], clone[target]] = [clone[target], clone[idx]];
      // Renumber priorities in steps of 10 for a clean gradient.
      return clone.map((r, i) => ({ ...r, priority: (i + 1) * 10 }));
    });
    setDry(null); setDryOk(false);
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
      <div className="mb-6 flex items-start justify-between">
        <div>
          <Heading>Rules</Heading>
          <Text className="mt-1">
            Draft edits stay local until you Apply — Dry-run must pass before Apply is enabled.
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
        <div className="mb-4">
          <Alert open onClose={() => setError(null)}>
            <AlertTitle>Something went wrong</AlertTitle>
            <AlertBody>{error}</AlertBody>
          </Alert>
        </div>
      )}
      {applyRes && (
        <div className="mb-4">
          <Alert open onClose={() => setApplyRes(null)}>
            <AlertTitle>Apply result</AlertTitle>
            <AlertBody>
              snapshot #{applyRes.snapshot_id} · health {applyRes.health}
              {applyRes.rolled_back ? ' · rolled back' : ''}
            </AlertBody>
          </Alert>
        </div>
      )}
      {isDirty && !applyRes && (
        <div className="mb-4">
          <Alert open onClose={() => { /* stays open until Applied/Reset */ }}>
            <AlertTitle>Unsaved changes</AlertTitle>
            <AlertBody>
              {dryOk
                ? 'Dry-run passed — Apply is enabled.'
                : dry
                ? `Dry-run failed (${dry.failed} fixture${dry.failed === 1 ? '' : 's'}). Fix rules or adjust priorities and re-run.`
                : 'Run Dry-run before Apply to guard against regressions.'}
            </AlertBody>
          </Alert>
        </div>
      )}

      <form className="glass p-6 mb-6" onSubmit={(e) => e.preventDefault()}>
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

      <div className="glass p-2">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>ID</TableHeader>
              <TableHeader>Kind</TableHeader>
              <TableHeader>Pattern</TableHeader>
              <TableHeader>Action</TableHeader>
              <TableHeader>Priority</TableHeader>
              <TableHeader>Status</TableHeader>
              <TableHeader className="text-right">Actions</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {draft.length === 0 && (
              <TableRow>
                <TableCell colSpan={7}>
                  <Text>No rules yet — click <strong>+ Add rule</strong> to create one.</Text>
                </TableCell>
              </TableRow>
            )}
            {draft.map((r, i) => {
              const isMatch = r.kind === 'MATCH';
              return (
                <TableRow key={r._key}>
                  <TableCell>
                    <Input value={r.id} onChange={(e) => patch(r._key, { id: e.target.value })} className="!p-1 !text-xs" />
                  </TableCell>
                  <TableCell>
                    <Select value={r.kind} onChange={(e) => patch(r._key, { kind: e.target.value as RuleKind })} className="!p-1 !text-xs">
                      {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
                    </Select>
                  </TableCell>
                  <TableCell>
                    <Input
                      value={r.pattern}
                      onChange={(e) => patch(r._key, { pattern: e.target.value })}
                      placeholder={isMatch ? '(unused)' : 'pattern'}
                      disabled={isMatch}
                      className="!p-1 !text-xs"
                    />
                  </TableCell>
                  <TableCell>
                    <Input value={r.action} onChange={(e) => patch(r._key, { action: e.target.value })} className="!p-1 !text-xs" />
                  </TableCell>
                  <TableCell className="metric">
                    <Input
                      type="number"
                      value={r.priority}
                      onChange={(e) => patch(r._key, { priority: Number(e.target.value) })}
                      className="!p-1 !text-xs w-20"
                    />
                  </TableCell>
                  <TableCell>
                    <button
                      type="button"
                      onClick={() => patch(r._key, { enabled: !r.enabled })}
                      className="cursor-pointer"
                      aria-label={r.enabled ? 'disable rule' : 'enable rule'}
                    >
                      <Badge color={r.enabled ? 'lime' : 'zinc'}>{r.enabled ? 'enabled' : 'disabled'}</Badge>
                    </button>
                  </TableCell>
                  <TableCell className="flex justify-end gap-1">
                    <Button plain aria-label="move up"   onClick={() => move(r._key, -1)} disabled={i === 0}>↑</Button>
                    <Button plain aria-label="move down" onClick={() => move(r._key, 1)}  disabled={i === draft.length - 1}>↓</Button>
                    <Button plain aria-label="delete"    onClick={() => removeRow(r._key)}>✕</Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

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
