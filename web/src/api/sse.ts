import { APIError, clearSession } from './client';

export interface SSEParseResult {
  events: string[];
  rest: string;
}

export function parseSSEFrames(input: string): SSEParseResult {
  const events: string[] = [];
  let rest = input;
  while (true) {
    const match = /\r?\n\r?\n/.exec(rest);
    if (!match || match.index === undefined) break;
    const frame = rest.slice(0, match.index);
    rest = rest.slice(match.index + match[0].length);
    const data = frame
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).replace(/^ /, ''));
    if (data.length > 0) events.push(data.join('\n'));
  }
  return { events, rest };
}

export async function streamSSE(
  path: string,
  token: string,
  signal: AbortSignal,
  onOpen: () => void,
  onEvent: (data: string) => void,
): Promise<void> {
  const response = await fetch(path, {
    headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
    signal,
  });
  if (response.status === 401) clearSession();
  if (!response.ok) {
    throw new APIError(`stream request failed: ${response.status}`, 'stream_failed', response.status);
  }
  if (!response.body) throw new Error('streaming response body is unavailable');
  onOpen();

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let rest = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    const parsed = parseSSEFrames(rest + decoder.decode(value, { stream: true }));
    rest = parsed.rest;
    parsed.events.forEach(onEvent);
  }
}
