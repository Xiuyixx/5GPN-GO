export interface PollOptions {
  intervalMs?: number;
  timeoutMs?: number;
  signal?: AbortSignal;
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

function abortError(): DOMException {
  return new DOMException('The operation was aborted', 'AbortError');
}

export function delay(ms: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return Promise.reject(abortError());
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, ms);
    signal?.addEventListener('abort', () => {
      window.clearTimeout(timer);
      reject(abortError());
    }, { once: true });
  });
}

export async function pollUntil<T>(
  load: () => Promise<T>,
  isPending: (value: T) => boolean,
  options: PollOptions = {},
): Promise<T> {
  const intervalMs = options.intervalMs ?? 500;
  const timeoutMs = options.timeoutMs ?? 60_000;
  const sleep = options.sleep ?? delay;
  const deadline = Date.now() + timeoutMs;

  while (true) {
    if (options.signal?.aborted) throw abortError();
    const value = await load();
    if (!isPending(value)) return value;
    if (Date.now() >= deadline) throw new Error('operation timed out');
    await sleep(intervalMs, options.signal);
  }
}

export async function pollSerially(
  task: (signal: AbortSignal) => Promise<void>,
  intervalMs: number,
  signal: AbortSignal,
): Promise<void> {
  while (!signal.aborted) {
    await task(signal);
    if (!signal.aborted) {
      try {
        await delay(intervalMs, signal);
      } catch (error) {
        if (signal.aborted) return;
        throw error;
      }
    }
  }
}
