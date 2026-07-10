import { useEffect, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Button } from '../components/ui/button';
import { Badge } from '../components/ui/badge';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import type { Snapshot, SnapshotsResponse } from '../api/client';

export default function Snapshots() {
  const [snaps, setSnaps] = useState<Snapshot[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  async function refresh() {
    try {
      const res = await api.get<SnapshotsResponse>('/api/v1/snapshots');
      setSnaps(res.snapshots ?? []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => { refresh(); }, []);

  async function rollback(id: number, hash: string) {
    if (!confirm(`Roll back to snapshot #${id} (${hash.slice(0, 12)}…)?`)) return;
    setErr(null); setBusy(id); setOk(null);
    try {
      await api.post(`/api/v1/snapshots/${id}/rollback`);
      setOk(`Rolled back to snapshot #${id}`);
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <AppShell>
      <Heading>Snapshots</Heading>
      <Text className="mt-1">Every apply writes an immutable snapshot. Rollback restores the paired rule_version as the active one.</Text>

      {err && (
        <div className="mt-4">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>Something went wrong</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {ok && (
        <div className="mt-4">
          <Alert open onClose={() => setOk(null)}>
            <AlertTitle>Rollback complete</AlertTitle>
            <AlertBody>{ok}</AlertBody>
          </Alert>
        </div>
      )}

      <div className="mt-6">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>#</TableHeader>
              <TableHeader>Created</TableHeader>
              <TableHeader>Hash</TableHeader>
              <TableHeader>Note</TableHeader>
              <TableHeader className="text-right">Actions</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {snaps.length === 0 && (
              <TableRow>
                <TableCell colSpan={5}>
                  <Text>No snapshots yet — apply a ruleset from the Rules page to generate one.</Text>
                </TableCell>
              </TableRow>
            )}
            {snaps.map((s, i) => (
              <TableRow key={s.id}>
                <TableCell>{s.id}</TableCell>
                <TableCell>{new Date(s.created_at).toLocaleString()}</TableCell>
                <TableCell><code className="text-xs">{s.config_hash.slice(0, 12)}…</code></TableCell>
                <TableCell>{s.note || '—'}</TableCell>
                <TableCell className="flex justify-end gap-2">
                  {i === 0
                    ? <Badge color="lime">current</Badge>
                    : <Button plain disabled={busy === s.id} onClick={() => rollback(s.id, s.config_hash)}>Roll back</Button>}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </AppShell>
  );
}
