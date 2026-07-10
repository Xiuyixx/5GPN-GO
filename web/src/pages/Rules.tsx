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
import type { ApplyResponse, DryRunResponse, ExitsResponse, Rule, RuleKind } from '../api/client';

const KINDS: RuleKind[] = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD',
  'GEOSITE', 'GEOIP', 'IP-CIDR', 'RULE-SET', 'MATCH',
];

// Special actions that are always valid regardless of exits.
const SPECIAL_ACTIONS = ['direct', 'block'];

interface Draft extends Rule {
  _key: string;
}

interface FixtureDraft {
  _key: string;
  domain: string;
  expected_exit: string;
}

let keyCounter = 0;
function makeKey(): string {
  keyCounter += 1;
  return `k${Date.now().toString(36)}-${keyCounter}`;
}

function toDraft(r: Rule): Draft {
  return { ...r, _key: makeKey() };
}

export default function Rules() {
  const [active, setActive] = useState<Rule[]>([]);
  const [draft, setDraft] = useState<Draft[]>([]);
  const [exits, setExits] = useState<string[]>([]);
  const [fixtures, setFixtures] = useState<FixtureDraft[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [applyRes, setApplyRes] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState<'idle' | 'dryrun' | 'apply'>('idle');
  const [note, setNote] = useState('');
  const [dryOk, setDryOk] = useState(false);

  // Available action options: direct/block + every configured exit id.
  const actionOptions = useMemo(() => [...SPECIAL_ACTIONS, ...exits], [exits]);

  async function refresh() {
    try {
      const [rulesRes, exitsRes] = await Promise.all([
        api.get<{ rules: Rule[] }>('/api/v1/rules'),
        api.get<ExitsResponse>('/api/v1/exits'),
      ]);
      const list = rulesRes.rules ?? [];
      setActive(list);
      setDraft(list.map(toDraft));
      setExits((exitsRes.exits ?? []).map((e) => e.id));
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
    const defaultAction = actionOptions[0] || 'direct';
    const _key = makeKey();
    setDraft((d) => [
      ...d,
      { _key, id: `rule-${d.length + 1}`, kind: 'DOMAIN-SUFFIX', pattern: '', action: defaultAction, priority, enabled: true },
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

  // Fixture editing.
  function addFixture() {
    setFixtures((f) => [
      ...f,
      { _key: makeKey(), domain: '', expected_exit: actionOptions[0] || 'direct' },
    ]);
    invalidate();
  }
  function patchFixture(key: string, changes: Partial<FixtureDraft>) {
    setFixtures((f) => f.map((x) => (x._key === key ? { ...x, ...changes } : x)));
    invalidate();
  }
  function removeFixture(key: string) {
    setFixtures((f) => f.filter((x) => x._key !== key));
    invalidate();
  }

  async function runDryRun() {
    setError(null); setBusy('dryrun'); setDry(null);
    try {
      const cleanFixtures = fixtures
        .filter((f) => f.domain.trim())
        .map(({ _key, ...f }) => f);
      const res = await api.post<DryRunResponse>('/api/v1/rules/dry-run', {
        rules: stripDrafts(draft),
        fixtures: cleanFixtures,
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

  const failedDetails = dry?.results.filter((r) => !r.pass) ?? [];

  return (
    <AppShell>
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div>
          <Heading>Rules</Heading>
          <Text className="mt-1">
            Edit rules · define test fixtures below · Dry-run must pass before Apply.
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
              ? `Dry-run passed (${dry?.passed ?? 0} fixture${(dry?.passed ?? 0) === 1 ? '' : 's'}) — Apply is enabled.`
              : dry
                ? `Dry-run failed: ${failedDetails.length} of ${dry.results.length} fixture${dry.results.length === 1 ? '' : 's'}.`
                : fixtures.filter((f) => f.domain.trim()).length === 0
                  ? 'Add at least one fixture below (domain + expected exit), then run Dry-run.'
                  : 'Run Dry-run before Apply to guard against regressions.'}
          </div>
          {dry && !dryOk && failedDetails.length > 0 && (
            <ul className="mt-2 space-y-1 text-xs">
              {failedDetails.slice(0, 5).map((r) => (
                <li key={r.domain}>
                  <code className="rounded bg-red-500/10 px-1 py-0.5">{r.domain}</code>{' '}
                  expected <strong>{r.expected_exit}</strong>, got <strong>{r.actual_exit || '—'}</strong>
                  {r.matched_rule ? ` (matched ${r.matched_rule})` : ' (no rule matched)'}
                </li>
              ))}
              {failedDetails.length > 5 && (
                <li className="text-red-700/70 dark:text-red-300/70">
                  … and {failedDetails.length - 5} more (see the results table below)
                </li>
              )}
            </ul>
          )}
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
                  <TableCell>
                    {actionOptions.includes(r.action)
                      ? <code className="text-xs">{r.action}</code>
                      : <span className="text-red-600 dark:text-red-400 text-xs">{r.action} (unknown)</span>}
                  </TableCell>
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
                    <Label>
                      Action{' '}
                      <span className="text-zinc-500 text-xs">
                        (direct / block / one of your exits)
                      </span>
                    </Label>
                    <Select value={r.action} onChange={(e) => patch(r._key, { action: e.target.value })}>
                      {!actionOptions.includes(r.action) && (
                        <option value={r.action}>{r.action} (unknown — will fail dry-run)</option>
                      )}
                      {actionOptions.map((a) => <option key={a} value={a}>{a}</option>)}
                    </Select>
                    {exits.length === 0 && (
                      <p className="mt-1 text-xs text-amber-700 dark:text-amber-300">
                        No custom exits configured yet. Add one on the <a className="underline" href="/exits">Exits</a> page.
                      </p>
                    )}
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

      {/* Fixtures editor: what Dry-run tests against. Empty = no assertions,
          which means Dry-run only checks that the rules are syntactically
          valid and can be rendered. */}
      <div className="glass mt-6 p-6">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <Heading level={2}>Dry-run fixtures</Heading>
            <Text className="mt-1 text-sm">
              Each row asserts: given this domain, the ruleset must route it to the expected exit.
              Empty = only syntax + render check.
            </Text>
          </div>
          <Button plain onClick={addFixture}>+ Add fixture</Button>
        </div>
        {fixtures.length === 0 ? (
          <div className="rounded border border-zinc-200/50 bg-zinc-50/40 px-4 py-3 text-sm text-zinc-600 dark:border-zinc-700/50 dark:bg-zinc-800/40 dark:text-zinc-400">
            No fixtures. Dry-run will still validate syntax; add fixtures if you want to
            assert specific routing decisions.
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeader>Domain</TableHeader>
                <TableHeader className="w-56">Expected exit</TableHeader>
                <TableHeader className="w-16 text-right">Remove</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {fixtures.map((f) => (
                <TableRow key={f._key} className="align-middle">
                  <TableCell>
                    <Input
                      value={f.domain}
                      onChange={(e) => patchFixture(f._key, { domain: e.target.value })}
                      placeholder="e.g. www.example.com"
                    />
                  </TableCell>
                  <TableCell>
                    <Select
                      value={f.expected_exit}
                      onChange={(e) => patchFixture(f._key, { expected_exit: e.target.value })}
                    >
                      {!actionOptions.includes(f.expected_exit) && (
                        <option value={f.expected_exit}>{f.expected_exit} (unknown)</option>
                      )}
                      {actionOptions.map((a) => <option key={a} value={a}>{a}</option>)}
                    </Select>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button plain aria-label="remove fixture" onClick={() => removeFixture(f._key)}>✕</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>

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
          <Heading level={2}>Dry-run results · passed {dry.passed} · failed {dry.failed}</Heading>
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
