import { useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router';
import Login from './pages/Login';
import Bootstrap from './pages/Bootstrap';
import Rules from './pages/Rules';
import { api } from './api/client';
import type { BootstrapStatus } from './api/client';
import { useAuthStore } from './stores/auth';

export default function App() {
  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const token = useAuthStore((s) => s.token);

  useEffect(() => {
    api.get<BootstrapStatus>('/api/v1/bootstrap')
      .then((s) => setNeedsSetup(s.needs_setup))
      .catch(() => setNeedsSetup(false));
  }, []);

  if (needsSetup === null) {
    return <div className="p-8 text-zinc-500">Loading…</div>;
  }
  if (needsSetup) {
    return (
      <Routes>
        <Route path="/setup" element={<Bootstrap />} />
        <Route path="*" element={<Navigate to="/setup" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/" element={token ? <Rules /> : <Navigate to="/login" replace />} />
      <Route path="*" element={<Navigate to={token ? '/' : '/login'} replace />} />
    </Routes>
  );
}
