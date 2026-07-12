import { describe, expect, it } from 'vitest';
import { parseSSEFrames } from './sse';

describe('parseSSEFrames', () => {
  it('retains fragmented input and ignores keepalive comments', () => {
    const first = parseSSEFrames('data: {"seq":1}\n\n: keepalive\n\ndata: {"seq"');
    expect(first.events).toEqual(['{"seq":1}']);
    expect(first.rest).toBe('data: {"seq"');

    const second = parseSSEFrames(first.rest + ':2}\r\n\r\n');
    expect(second.events).toEqual(['{"seq":2}']);
    expect(second.rest).toBe('');
  });

  it('joins multiple data lines according to the SSE format', () => {
    expect(parseSSEFrames('data: first\ndata: second\n\n').events).toEqual(['first\nsecond']);
  });
});
