import { useEffect, useState } from 'react';
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
import type { ExitSummary, ExitsResponse } from '../api/client';

export default function Exits() {
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
      await api.post('/api/v1/exits/switch', { id: target });
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  async function removeExit(target: string) {
    if (!confirm(`Delete exit "${target}"?`)) return;
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
        <Heading>Exits</Heading>
        <Text className="mt-1">Add a proxy by pasting its share URI · ss / vmess / trojan / vless / hysteria2 / tuic / anytls / socks5 / http(s).</Text>
      </div>

      {err && (
        <div className="mb-4">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>Something went wrong</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}

      <form onSubmit={add} className="glass p-6">
        <Fieldset>
          <Legend>Add exit</Legend>
          <FieldGroup>
            <Field>
              <Label>ID (short alphanumeric)</Label>
              <Input required value={id} onChange={(e) => setId(e.target.value)} placeholder="wg1" />
            </Field>
            <Field>
              <Label>Share URI</Label>
              <Input required value={uri} onChange={(e) => setUri(e.target.value)} placeholder="trojan://password@host:443?sni=..." />
            </Field>
          </FieldGroup>
        </Fieldset>
        <div className="mt-4">
          <Button type="submit" color="indigo" disabled={busy}>Add exit</Button>
        </div>
      </form>

      <div className="glass mt-6 p-2">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>ID</TableHeader>
              <TableHeader>Protocol</TableHeader>
              <TableHeader>Server</TableHeader>
              <TableHeader>Port</TableHeader>
              <TableHeader>Status</TableHeader>
              <TableHeader className="text-right">Actions</TableHeader>
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
                  {e.active ? <Badge color="lime">active</Badge> : <Badge color="zinc">standby</Badge>}
                </TableCell>
                <TableCell className="flex justify-end gap-2">
                  {!e.active && <Button plain onClick={() => switchExit(e.id)}>Switch</Button>}
                  {!e.active && <Button plain onClick={() => removeExit(e.id)}>Delete</Button>}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </AppShell>
  );
}
