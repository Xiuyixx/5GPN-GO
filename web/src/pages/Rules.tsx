import { useEffect, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import type { ApplyResponse, DryRunResponse, Rule } from '../api/client';

const SAMPLE_RULES: Rule[] = [
  { id: 'cn-suffix', kind: 'DOMAIN-SUFFIX', pattern: 'cn', action: 'direct', priority: 10, enabled: true },
  { id: 'match',     kind: 'MATCH',         pattern: '',   action: 'wg1',    priority: 100, enabled: true },
];

const SAMPLE_FIXTURES = [
  { domain: 'baidu.cn',    expected_exit: 'direct' },
  { domain: 'example.com', expected_exit: 'wg1' },
];

export default function Rules() {
  const [rules, setRules] = useState<Rule[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [apply, setApply] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      const { rules } = await api.get<{ rules: Rule[] }>('/api/v1/rules');
      setRules(rules.length ? rules : SAMPLE_RULES);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => { refresh(); }, []);

  async function runDryRun() {
    setError(null); setBusy(true); setDry(null);
    try {
      const res = await api.post<DryRunResponse>('/api/v1/rules/dry-run', {
        rules: rules.length ? rules : SAMPLE_RULES,
        fixtures: SAMPLE_FIXTURES,
      });
      setDry(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function runApply() {
    setError(null); setBusy(true); setApply(null);
    try {
      const res = await api.post<ApplyResponse>('/api/v1/rules/apply', {
        rules: rules.length ? rules : SAMPLE_RULES,
        note: 'panel apply',
      });
      setApply(res);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AppShell>
      <div className="mb-6 flex items-start justify-between">
        <div>
          <Heading>Rules</Heading>
          <Text className="mt-1">Sandbox dry-run + auto-rollback pipeline · manual CRUD lands in a future slice.</Text>
        </div>
        <div className="flex gap-3">
          <Button color="zinc" onClick={runDryRun} disabled={busy}>Dry-run</Button>
          <Button color="indigo" onClick={runApply} disabled={busy}>Apply</Button>
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

      {apply && (
        <div className="mb-4">
          <Alert open onClose={() => setApply(null)}>
            <AlertTitle>Apply result</AlertTitle>
            <AlertBody>
              snapshot #{apply.snapshot_id} · health {apply.health}
              {apply.rolled_back ? ' · rolled back' : ''}
            </AlertBody>
          </Alert>
        </div>
      )}

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
            </TableRow>
          </TableHead>
          <TableBody>
            {(rules.length ? rules : SAMPLE_RULES).map((r) => (
              <TableRow key={r.id}>
                <TableCell className="font-medium">{r.id}</TableCell>
                <TableCell>{r.kind}</TableCell>
                <TableCell>{r.pattern || '—'}</TableCell>
                <TableCell>{r.action}</TableCell>
                <TableCell className="metric">{r.priority}</TableCell>
                <TableCell>
                  <Badge color={r.enabled ? 'lime' : 'zinc'}>{r.enabled ? 'enabled' : 'disabled'}</Badge>
                </TableCell>
              </TableRow>
            ))}
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
