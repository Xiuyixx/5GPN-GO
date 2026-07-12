import { describe, expect, it } from 'vitest';
import { calculateQPS } from './DashboardDNS';
import type { DNSMetrics } from '../api/client';

function metrics(queries: number): DNSMetrics {
  return {
    queries_total: queries,
    hits_block: 0,
    hits_direct: 0,
    hits_proxy: 0,
    upstream_errors: 0,
    refused_axfr: 0,
    listeners: { udp53: 'healthy', tcp53: 'healthy', dot: 'healthy', doh: 'healthy' },
    cert: null,
  };
}

describe('calculateQPS', () => {
  it('computes a rate and clamps counter resets to zero', () => {
    expect(calculateQPS(
      { ts: 1000, metrics: metrics(10) },
      { ts: 6000, metrics: metrics(20) },
    )).toBe(2);
    expect(calculateQPS(
      { ts: 1000, metrics: metrics(20) },
      { ts: 6000, metrics: metrics(2) },
    )).toBe(0);
  });
});
