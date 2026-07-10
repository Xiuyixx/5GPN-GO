import { Routes, Route, Navigate } from 'react-router';
import Login from './pages/Login';
import Rules from './pages/Rules';

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/" element={<Rules />} />
      <Route path="*" element={<Navigate to="/login" replace />} />
    </Routes>
  );
}
