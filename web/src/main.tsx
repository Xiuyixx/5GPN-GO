import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import App from './App';
import './styles/tailwind.css';
import './i18n'; // initializes i18next before the first render
import { useThemeStore } from './stores/theme';

// Apply the effective theme to <html> BEFORE React's first render. Without
// this, a user whose stored pick is `dark` sees a flash of light content
// while zustand rehydrates + the effect runs. We poke the DOM directly here
// with the rehydrated store value.
function applyThemeClass(effective: 'light' | 'dark') {
  const root = document.documentElement;
  root.classList.toggle('dark', effective === 'dark');
  root.classList.toggle('light', effective === 'light');
}

// Rehydrate synchronously so `theme` reflects what the operator picked
// last session before we compute effective.
useThemeStore.persist.rehydrate();
applyThemeClass(useThemeStore.getState().effective);

// Follow the store from now on (React components use `useThemeStore()`
// which triggers this subscription too).
useThemeStore.subscribe((s) => applyThemeClass(s.effective));

// If the operator picked `system`, keep the effective palette in sync
// with the OS palette while the tab is open.
if (typeof window !== 'undefined' && window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)');
  mq.addEventListener('change', () => useThemeStore.getState().refreshFromSystem());
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
