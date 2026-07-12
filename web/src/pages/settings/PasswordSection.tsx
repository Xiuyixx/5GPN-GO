import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { Alert, AlertBody, AlertTitle } from '../../components/ui/alert';
import { Button } from '../../components/ui/button';
import { ErrorMessage, Field, Label } from '../../components/ui/fieldset';
import { Input } from '../../components/ui/input';
import { api, APIError } from '../../api/client';
import { useAuthStore } from '../../stores/auth';
import { useMeStore } from '../../stores/me';
import { Section } from './Section';

export function PasswordSection() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const clearAuth = useAuthStore((state) => state.clear);
  const clearMe = useMeStore((state) => state.clear);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [fieldErr, setFieldErr] = useState<{ next?: string; confirm?: string }>({});

  function validate(): boolean {
    const errors: { next?: string; confirm?: string } = {};
    if (next.length < 8) errors.next = t('settings.newPasswordTooShort');
    if (next !== confirm) errors.confirm = t('settings.passwordMismatch');
    if (current && next && current === next) errors.next = t('settings.newPasswordSameAsCurrent');
    setFieldErr(errors);
    return Object.keys(errors).length === 0;
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setErr(null);
    if (!validate()) return;
    setSubmitting(true);
    try {
      await api.post('/api/v1/password', { current, next });
      clearMe();
      clearAuth();
      navigate('/login', { replace: true });
    } catch (error) {
      setErr(error instanceof APIError && error.status === 401
        ? t('settings.currentPasswordIncorrect')
        : error instanceof Error ? error.message : String(error));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Section tint="rgb(239 68 68 / 0.30)" title={t('settings.changePassword')} description={t('settings.changePasswordDescription')}>
      {err && <Alert open onClose={() => setErr(null)}><AlertTitle>{t('settings.changePasswordFailed')}</AlertTitle><AlertBody>{err}</AlertBody></Alert>}
      <form onSubmit={submit} className="space-y-5">
        <Field><Label>{t('settings.currentPasswordLabel')}</Label><Input type="password" value={current} onChange={(event) => setCurrent(event.target.value)} autoComplete="current-password" required /></Field>
        <Field><Label>{t('settings.newPasswordLabel')}</Label><Input type="password" value={next} onChange={(event) => setNext(event.target.value)} autoComplete="new-password" required />{fieldErr.next && <ErrorMessage>{fieldErr.next}</ErrorMessage>}</Field>
        <Field><Label>{t('settings.confirmPasswordLabel')}</Label><Input type="password" value={confirm} onChange={(event) => setConfirm(event.target.value)} autoComplete="new-password" required />{fieldErr.confirm && <ErrorMessage>{fieldErr.confirm}</ErrorMessage>}</Field>
        <Button type="submit" color="indigo" disabled={submitting}>{submitting ? t('settings.submitting') : t('settings.changePassword')}</Button>
      </form>
    </Section>
  );
}
