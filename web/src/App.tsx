import { useCallback, useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router';
import { useTranslation } from 'react-i18next';
import Login from './pages/Login';
import Bootstrap from './pages/Bootstrap';
import Wizard from './pages/Wizard';
import Rules from './pages/Rules';
import Dashboard from './pages/Dashboard';
import Exits from './pages/Exits';
import Snapshots from './pages/Snapshots';
import Backup from './pages/Backup';
import Logs from './pages/Logs';
import Settings from './pages/Settings';
import { api } from './api/client';
import type { BootstrapStatus } from './api/client';
import { useAuthStore } from './stores/auth';

export default function App() {
  const { t } = useTranslation();
  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const [needsWizard, setNeedsWizard] = useState<boolean>(false);
  const token = useAuthStore((s) => s.token);

  const refresh = useCallback(async () => {
    try {
      const s = await api.get<BootstrapStatus>('/api/v1/bootstrap');
      setNeedsSetup(s.needs_setup);
      setNeedsWizard(Boolean(s.needs_wizard));
    } catch {
      setNeedsSetup(false);
      setNeedsWizard(false);
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  if (needsSetup === null) {
    return <div className="p-8 text-zinc-500">{t('common.loading')}</div>;
  }

  // authed: bootstrap must be claimed AND the user must be signed in.
  // wizardGate: after auth, first-time users hit /wizard before the
  // dashboard. wizard.complete=true on the server clears needsWizard so
  // subsequent renders route normally.
  const authed = (element: React.ReactNode) => {
    if (needsSetup) return <Navigate to="/setup" replace />;
    if (!token) return <Navigate to="/login" replace />;
    if (needsWizard) return <Navigate to="/wizard" replace />;
    return element;
  };

  return (
    <Routes>
      <Route path="/setup" element={
        needsSetup ? <Bootstrap onDone={refresh} /> : <Navigate to="/login" replace />
      } />
      <Route path="/login" element={
        needsSetup ? <Navigate to="/setup" replace /> : <Login />
      } />
      <Route path="/wizard" element={
        needsSetup
          ? <Navigate to="/setup" replace />
          : (!token
            ? <Navigate to="/login" replace />
            : <Wizard onDone={refresh} />)
      } />
      <Route path="/" element={authed(<Dashboard />)} />
      <Route path="/rules" element={authed(<Rules />)} />
      <Route path="/exits" element={authed(<Exits />)} />
      <Route path="/snapshots" element={authed(<Snapshots />)} />
      <Route path="/backup" element={authed(<Backup />)} />
      <Route path="/logs" element={authed(<Logs />)} />
      <Route path="/settings" element={authed(<Settings />)} />
      <Route path="*" element={
        <Navigate to={
          needsSetup ? '/setup'
            : !token ? '/login'
            : needsWizard ? '/wizard'
            : '/'
        } replace />
      } />
    </Routes>
  );
}
