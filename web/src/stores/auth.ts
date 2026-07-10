import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface AuthState {
  token: string | null;
  username: string | null;
  setToken: (token: string, username?: string | null) => void;
  clear: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      username: null,
      setToken: (token, username = null) => set({ token, username }),
      clear: () => set({ token: null, username: null }),
    }),
    { name: '5gpn-auth' },
  ),
);
