import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import { settleExitSwitch, type ExitSwitchResponse } from '../api/apply';
import type { ExitSummary, ExitsResponse } from '../api/client';

export default function Exits() {
  const { t } = useTranslation();
  const [state, setState] = useState<ExitsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [id, setId] = useState('');
  const [uri, setUri] = useState('');
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      setState(await api.get<ExitsResponse>('/api/v1/exits'));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => { refresh(); }, []);

  async function add(e: React.FormEvent) {
    e.preventDefault();
    setErr(null); setBusy(true);
    try {
      await api.post<ExitSummary>('/api/v1/exits/add', { id, uri });
      setId(''); setUri('');
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function switchExit(target: string) {
    setErr(null);
    try {
      const response = await api.post<ExitSwitchResponse>('/api/v1/exits/switch', { id: target });
      await settleExitSwitch(response);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      await refresh();
    }
  }

  async function removeExit(target: string) {
    if (!confirm(t('exits.confirmDelete', { id: target }))) return;
    setErr(null);
    try {
      await api.post('/api/v1/exits/delete', { id: target });
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('exits.title')}</Heading>
        <Text className="mt-1">{t('exits.subtitle')}</Text>
      </div>

      {err && (
        <div className="mb-4">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>{t('exits.errorTitle')}</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}

      <form onSubmit={add} className="glass p-6">
        <Fieldset>
          <Legend>{t('exits.addExit')}</Legend>
          <FieldGroup>
            <Field>
              <Label>{t('exits.idLabel')}</Label>
              <Input required value={id} onChange={(e) => setId(e.target.value)} placeholder="wg1" />
            </Field>
            <Field>
              <Label>{t('exits.uriLabel')}</Label>
              <Input required value={uri} onChange={(e) => setUri(e.target.value)} placeholder="trojan://password@host:443?sni=..." />
            </Field>
          </FieldGroup>
        </Fieldset>
        <div className="mt-4">
          <Button type="submit" color="indigo" disabled={busy}>{t('exits.addExit')}</Button>
        </div>
      </form>

      <div className="glass mt-6 p-2">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>{t('exits.columnId')}</TableHeader>
              <TableHeader>{t('exits.columnProtocol')}</TableHeader>
              <TableHeader>{t('exits.columnServer')}</TableHeader>
              <TableHeader>{t('exits.columnPort')}</TableHeader>
              <TableHeader>{t('exits.columnStatus')}</TableHeader>
              <TableHeader className="text-right">{t('exits.columnActions')}</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {state?.exits.map((e) => (
              <TableRow key={e.id}>
                <TableCell className="font-medium">{e.id}</TableCell>
                <TableCell>{e.protocol}</TableCell>
                <TableCell>{e.server}</TableCell>
                <TableCell className="metric">{e.port || '—'}</TableCell>
                <TableCell>
                  {e.active ? <Badge color="lime">{t('exits.statusActive')}</Badge> : <Badge color="zinc">{t('exits.statusStandby')}</Badge>}
                </TableCell>
                <TableCell className="flex justify-end gap-2">
                  {!e.active && <Button plain onClick={() => switchExit(e.id)}>{t('exits.switch')}</Button>}
                  {!e.active && <Button plain onClick={() => removeExit(e.id)}>{t('exits.delete')}</Button>}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </AppShell>
  );
}
