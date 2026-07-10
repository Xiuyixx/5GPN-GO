import { useState } from 'react';
import { useNavigate } from 'react-router';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Input } from '../components/ui/input';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import type { Me } from '../api/client';

interface BootstrapProps {
  onDone?: () => Promise<void> | void;
}

export default function Bootstrap({ onDone }: BootstrapProps) {
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
      if (onDone) await onDone();
      nav('/login', { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="ambient-bg" aria-hidden />
      <div className="fade-up flex min-h-screen items-center justify-center p-6">
        <div className="glass-strong w-full max-w-md p-8">
          <div className="mb-6 flex items-center gap-3">
            <div className="grid h-11 w-11 place-items-center rounded-2xl bg-gradient-to-br from-indigo-500 via-violet-500 to-teal-400 text-sm font-bold text-white shadow-lg shadow-indigo-500/30">
              5G
            </div>
            <div>
              <div className="text-xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">First-time setup</div>
              <div className="text-xs uppercase tracking-widest text-zinc-500">claim your panel</div>
            </div>
          </div>

          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-6">
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
        </div>
      </div>
    </>
  );
}
