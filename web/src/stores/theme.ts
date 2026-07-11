// Theme store: system | light | dark.
//
// - `theme` is the operator's stated preference.
// - `effective` is what the DOM should reflect right now (system resolves
//   to light or dark based on prefers-color-scheme).
// - main.tsx subscribes to this store, toggles the `.dark` class on
//   <html>, and also wires a matchMedia listener that pokes the store
//   whenever the OS palette flips while `theme === 'system'`.

import { create } from 'zustand';
import { persist } from 'zustand/middleware';

export type Theme = 'system' | 'light' | 'dark';
export type Effective = 'light' | 'dark';

interface ThemeState {
  theme: Theme;
  effective: Effective;
  setTheme: (t: Theme) => void;
  // Called by the matchMedia listener when the OS palette flips.
  refreshFromSystem: () => void;
}

const STORAGE_KEY = '5gpn-theme';

function systemIsDark(): boolean {
  if (typeof window === 'undefined') return false;
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false;
}

function resolve(theme: Theme): Effective {
  if (theme === 'system') return systemIsDark() ? 'dark' : 'light';
  return theme;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set, get) => ({
      theme: 'system',
      effective: resolve('system'),
      setTheme: (theme) => set({ theme, effective: resolve(theme) }),
      refreshFromSystem: () => {
        if (get().theme !== 'system') return;
        set({ effective: resolve('system') });
      },
    }),
    {
      name: STORAGE_KEY,
      // Only persist the operator's choice — recompute effective on load
      // so a rehydrated 'system' pick reflects the current OS palette.
      partialize: (s) => ({ theme: s.theme }),
      onRehydrateStorage: () => (state) => {
        if (state) state.effective = resolve(state.theme);
      },
    },
  ),
);
