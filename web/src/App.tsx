import { useCallback, useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router';
import Login from './pages/Login';
import Bootstrap from './pages/Bootstrap';
import Rules from './pages/Rules';
import Dashboard from './pages/Dashboard';
import Exits from './pages/Exits';
import Snapshots from './pages/Snapshots';
import Backup from './pages/Backup';
import Logs from './pages/Logs';
import { api } from './api/client';
import type { BootstrapStatus } from './api/client';
import { useAuthStore } from './stores/auth';

export default function App() {
  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const token = useAuthStore((s) => s.token);

  const refresh = useCallback(async () => {
    try {
      const s = await api.get<BootstrapStatus>('/api/v1/bootstrap');
      setNeedsSetup(s.needs_setup);
    } catch {
      setNeedsSetup(false);
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  if (needsSetup === null) {
    return <div className="p-8 text-zinc-500">Loading…</div>;
  }

  const authed = (element: React.ReactNode) => (
    needsSetup
      ? <Navigate to="/setup" replace />
      : (token ? element : <Navigate to="/login" replace />)
  );

  return (
    <Routes>
      <Route path="/setup" element={
        needsSetup ? <Bootstrap onDone={refresh} /> : <Navigate to="/login" replace />
      } />
      <Route path="/login" element={
        needsSetup ? <Navigate to="/setup" replace /> : <Login />
      } />
      <Route path="/" element={authed(<Dashboard />)} />
      <Route path="/rules" element={authed(<Rules />)} />
      <Route path="/exits" element={authed(<Exits />)} />
      <Route path="/snapshots" element={authed(<Snapshots />)} />
      <Route path="/backup" element={authed(<Backup />)} />
      <Route path="/logs" element={authed(<Logs />)} />
      <Route path="*" element={
        <Navigate to={needsSetup ? '/setup' : (token ? '/' : '/login')} replace />
      } />
    </Routes>
  );
}
