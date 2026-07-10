import { useState } from 'react';
import { useNavigate } from 'react-router';
import { AuthLayout } from '../components/ui/auth-layout';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Input } from '../components/ui/input';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { api } from '../api/client';
import type { Me } from '../api/client';

export default function Bootstrap() {
  const nav = useNavigate();
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await api.post<Me>('/api/v1/bootstrap', { token, username, password });
      nav('/login', { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <form onSubmit={onSubmit} className="grid w-full max-w-sm grid-cols-1 gap-8">
        <Heading>First-time setup</Heading>
        <Text>Paste the setup token printed on the daemon's first boot and choose your admin credentials.</Text>
        {error && (
          <Alert open onClose={() => setError(null)}>
            <AlertTitle>Setup failed</AlertTitle>
            <AlertBody>{error}</AlertBody>
          </Alert>
        )}
        <Fieldset>
          <Legend className="sr-only">Setup fields</Legend>
          <FieldGroup>
            <Field>
              <Label>Setup token</Label>
              <Input required value={token} onChange={(e) => setToken(e.target.value)} />
            </Field>
            <Field>
              <Label>Username</Label>
              <Input required value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <Field>
              <Label>Password (min. 8 characters)</Label>
              <Input required type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          </FieldGroup>
        </Fieldset>
        <Button type="submit" disabled={busy} className="w-full">
          {busy ? 'Claiming…' : 'Claim panel'}
        </Button>
      </form>
    </AuthLayout>
  );
}
