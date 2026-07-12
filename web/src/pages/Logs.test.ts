import { describe, expect, it } from 'vitest';
import { formatLogTimestamp, LOG_UNITS } from './Logs';

describe('log display contract', () => {
  it('renders journalctl microsecond epochs as local clock time', () => {
    expect(formatLogTimestamp('1704067200000000'))
      .toBe(new Date('2024-01-01T00:00:00Z').toLocaleTimeString());
  });

  it('offers the mtg systemd unit', () => {
    expect(LOG_UNITS).toContain('mtg');
  });
});
