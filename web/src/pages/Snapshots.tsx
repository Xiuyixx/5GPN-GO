import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Button } from '../components/ui/button';
import { Badge } from '../components/ui/badge';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import { settleRollback } from '../api/apply';
import type { PendingApplyResponse, RollbackResponse, Snapshot, SnapshotsResponse } from '../api/client';

export default function Snapshots() {
  const { t } = useTranslation();
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
    if (!confirm(t('snapshots.rollbackConfirm', { id, hash: hash.slice(0, 12) }))) return;
    setErr(null); setBusy(id); setOk(null);
    try {
      const response = await api.post<RollbackResponse | PendingApplyResponse>(`/api/v1/snapshots/${id}/rollback`);
      await settleRollback(response);
      setOk(t('snapshots.rollbackSuccess', { id }));
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('snapshots.heading')}</Heading>
        <Text className="mt-1">{t('snapshots.subheading')}</Text>
      </div>

      {err && (
        <div className="mb-4">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('snapshots.errorTitle')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {ok && (
        <div className="mb-4">
          <Alert open onClose={() => setOk(null)}>
            <AlertTitle>{t('snapshots.rollbackCompleteTitle')}</AlertTitle>
            <AlertBody>{ok}</AlertBody>
          </Alert>
        </div>
      )}

      <div className="glass p-2">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>#</TableHeader>
              <TableHeader>{t('snapshots.columnCreated')}</TableHeader>
              <TableHeader>{t('snapshots.columnHash')}</TableHeader>
              <TableHeader>{t('snapshots.columnNote')}</TableHeader>
              <TableHeader className="text-right">{t('snapshots.columnActions')}</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {snaps.length === 0 && (
              <TableRow>
                <TableCell colSpan={5}>
                  <Text>{t('snapshots.emptyState')}</Text>
                </TableCell>
              </TableRow>
            )}
            {snaps.map((s, i) => (
              <TableRow key={s.id}>
                <TableCell className="metric">{s.id}</TableCell>
                <TableCell>{new Date(s.created_at).toLocaleString()}</TableCell>
                <TableCell><code className="text-xs">{s.config_hash.slice(0, 12)}…</code></TableCell>
                <TableCell>{s.note || '—'}</TableCell>
                <TableCell className="flex justify-end gap-2">
                  <div className="flex items-center justify-end gap-2">
                    {i === 0 && <Badge color="zinc">{t('snapshots.latestBadge')}</Badge>}
                    {s.active
                      ? <Badge color="lime">{t('snapshots.currentBadge')}</Badge>
                      : s.rollbackable
                        ? <Button plain disabled={busy === s.id} onClick={() => rollback(s.id, s.config_hash)}>
                          {busy === s.id ? t('snapshots.rollbackPending') : t('snapshots.rollbackAction')}
                        </Button>
                        : <Badge color="zinc">{t('snapshots.notRollbackableBadge')}</Badge>}
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </AppShell>
  );
}
