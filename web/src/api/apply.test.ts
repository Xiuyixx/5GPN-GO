import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './client';
import { settleBackupImport, settleExitSwitch, settleRollback, settleRuleApply } from './apply';

const noWait = { sleep: async () => undefined };

describe('apply lifecycle settlement', () => {
  afterEach(() => vi.restoreAllMocks());

  it('settles a 202 apply exclusively from its apply id', async () => {
    const get = vi.spyOn(api, 'get')
      .mockResolvedValueOnce({ id: 'a1', hash: 'h', status: 'pending', kind: 'apply' })
      .mockResolvedValueOnce({ id: 'a1', hash: 'h', status: 'succeeded', kind: 'apply', snapshot_id: 9 });

    const result = await settleRuleApply({ apply_id: 'a1', hash: 'h', status: 'pending' }, noWait);
    expect(result).toMatchObject({ snapshot_id: 9, health: 'confirmed', rolled_back: false });
    expect(get).toHaveBeenCalledTimes(2);
    expect(get).toHaveBeenNthCalledWith(1, '/api/v1/applies/a1');
    expect(get).toHaveBeenNthCalledWith(2, '/api/v1/applies/a1');
  });

  it('rejects an observing response that cannot be correlated by apply id', async () => {
    const get = vi.spyOn(api, 'get');
    await expect(settleRuleApply({
      snapshot_id: 12,
      rule_version_id: 18,
      rolled_back: false,
      health: 'observing',
    }, noWait)).rejects.toMatchObject({ code: 'invalid_response' });
    expect(get).not.toHaveBeenCalled();
  });

  it('does not report rollback success before its async resolver publish completes', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({ id: 'r1', hash: 'h', status: 'pending', kind: 'rollback' })
      .mockResolvedValueOnce({ id: 'r1', hash: 'h', status: 'succeeded', kind: 'rollback', snapshot_id: 3 });

    const result = await settleRollback({
      apply_id: 'r1',
      hash: 'h',
      status: 'pending',
      snapshot_id: 3,
      rule_version_id: 8,
    }, noWait);
    expect(result).toMatchObject({ snapshot_id: 3, rule_version_id: 8, health: 'confirmed' });
  });

  it('waits for an observing backup import before marking it applied', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({ id: 'i1', hash: 'h', status: 'pending', kind: 'import', snapshot_id: 21 })
      .mockResolvedValueOnce({ id: 'i1', hash: 'h', status: 'succeeded', kind: 'import', snapshot_id: 21 });

    const result = await settleBackupImport({
      entries: 4,
      total_bytes: 100,
      applied: false,
      pending: true,
      apply_id: 'i1',
      applied_snapshot_id: 21,
      apply_result: { health: 'observing', rolled_back: false },
    }, noWait);
    expect(result).toMatchObject({ applied: true, pending: false, apply_result: { health: 'confirmed' } });
  });

  it('waits for an observing exit switch by its apply id', async () => {
    const get = vi.spyOn(api, 'get')
      .mockResolvedValueOnce({ id: 'e1', hash: 'wg1', status: 'pending', kind: 'exit_switch' })
      .mockResolvedValueOnce({ id: 'e1', hash: 'wg1', status: 'succeeded', kind: 'exit_switch', snapshot_id: 30 });

    const result = await settleExitSwitch({
      active: 'wg1', health: 'observing', status: 'pending', apply_id: 'e1', snapshot_id: 30,
    }, noWait);
    expect(result).toMatchObject({ active: 'wg1', health: 'confirmed', snapshot_id: 30 });
    expect(get).toHaveBeenCalledTimes(2);
  });
});
