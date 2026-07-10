import { create } from 'zustand';

interface MeState {
  username: string | null;
  userId: number | null;
  loaded: boolean;
  set: (userId: number, username: string) => void;
  clear: () => void;
}

export const useMeStore = create<MeState>()((set) => ({
  username: null,
  userId: null,
  loaded: false,
  set: (userId, username) => set({ userId, username, loaded: true }),
  clear: () => set({ userId: null, username: null, loaded: false }),
}));
