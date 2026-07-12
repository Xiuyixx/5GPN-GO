import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, APIError } from './client';
import { useAuthStore } from '../stores/auth';
import { useMeStore } from '../stores/me';

describe('api client session handling', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('clears both auth and cached identity after a 401', async () => {
    useAuthStore.getState().setToken('expired-token', 'old-user');
    useMeStore.getState().set(7, 'old-user');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: 'unauthorized', message: 'expired' }),
      { status: 401, headers: { 'Content-Type': 'application/json' } },
    )));

    await expect(api.get('/api/v1/me')).rejects.toBeInstanceOf(APIError);
    expect(useAuthStore.getState().token).toBeNull();
    expect(useMeStore.getState()).toMatchObject({ userId: null, username: null, loaded: false });
  });
});
