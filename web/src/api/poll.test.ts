import { describe, expect, it, vi } from 'vitest';
import { pollSerially, pollUntil } from './poll';

describe('poll helpers', () => {
  it('loads until the first terminal value', async () => {
    const load = vi.fn()
      .mockResolvedValueOnce('pending')
      .mockResolvedValueOnce('pending')
      .mockResolvedValueOnce('done');
    const result = await pollUntil(load, (value) => value === 'pending', {
      sleep: async () => undefined,
    });
    expect(result).toBe('done');
    expect(load).toHaveBeenCalledTimes(3);
  });

  it('never overlaps interval work', async () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    let running = 0;
    let maxRunning = 0;
    let calls = 0;
    const loop = pollSerially(async () => {
      running += 1;
      maxRunning = Math.max(maxRunning, running);
      calls += 1;
      await new Promise((resolve) => window.setTimeout(resolve, 25));
      running -= 1;
      if (calls === 3) controller.abort();
    }, 10, controller.signal);

    await vi.runAllTimersAsync();
    await loop;
    expect(calls).toBe(3);
    expect(maxRunning).toBe(1);
    vi.useRealTimers();
  });
});
