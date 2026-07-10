import { useState } from 'react';
import { useNavigate } from 'react-router';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Input } from '../components/ui/input';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { api } from '../api/client';
import type { LoginResponse } from '../api/client';
import { useAuthStore } from '../stores/auth';

export default function Login() {
  const nav = useNavigate();
  const setToken = useAuthStore((s) => s.setToken);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const { token } = await api.post<LoginResponse>('/api/v1/login', { username, password });
      setToken(token, username);
      nav('/', { replace: true });
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
              <div className="text-xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">Sign in to 5gpn</div>
              <div className="text-xs uppercase tracking-widest text-zinc-500">personal gateway</div>
            </div>
          </div>

          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-6">
            {error && (
              <Alert open onClose={() => setError(null)}>
                <AlertTitle>Sign-in failed</AlertTitle>
                <AlertBody>{error}</AlertBody>
              </Alert>
            )}
            <Fieldset>
              <Legend className="sr-only">Credentials</Legend>
              <FieldGroup>
                <Field>
                  <Label>Username</Label>
                  <Input required autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
                </Field>
                <Field>
                  <Label>Password</Label>
                  <Input required type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
              </FieldGroup>
            </Fieldset>
            <Button type="submit" disabled={busy} className="w-full">
              {busy ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>
        </div>
      </div>
    </>
  );
}
