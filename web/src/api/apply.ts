import { api, APIError } from './client';
import type { ApplyResponse, BackupImportResult, PendingApplyResponse, RollbackResponse } from './client';
import { pollUntil, type PollOptions } from './poll';

export interface ApplyEntry {
  id: string;
  hash: string;
  status: 'pending' | 'succeeded' | 'failed';
  kind: 'apply' | 'rollback' | 'import' | 'exit_switch';
  snapshot_id?: number;
  error?: string;
}

export function isPendingApply(
  response: ApplyResponse | RollbackResponse | PendingApplyResponse,
): response is PendingApplyResponse {
  return 'status' in response
    && response.status === 'pending'
    && typeof response.apply_id === 'string';
}

export async function waitForApplyEntry(id: string, options?: PollOptions): Promise<ApplyEntry> {
  const entry = await pollUntil(
    () => api.get<ApplyEntry>(`/api/v1/applies/${encodeURIComponent(id)}`),
    (value) => value.status === 'pending',
    options,
  );
  if (entry.status === 'failed') {
    throw new APIError(entry.error || 'apply failed', 'apply_failed', 500);
  }
  return entry;
}

export async function settleRuleApply(
  response: ApplyResponse | PendingApplyResponse,
  options?: PollOptions,
): Promise<ApplyResponse> {
  if (isPendingApply(response)) {
    const entry = await waitForApplyEntry(response.apply_id, options);
    const snapshotID = response.snapshot_id ?? entry.snapshot_id;
    if (!snapshotID) {
      throw new APIError('terminal apply entry has no snapshot id', 'invalid_response', 500);
    }
    return {
      snapshot_id: snapshotID,
      rule_version_id: response.rule_version_id ?? 0,
      rolled_back: false,
      health: 'confirmed',
    };
  }
  if (response.health !== 'observing') return response;
  throw new APIError('observing apply response has no apply id', 'invalid_response', 500);
}

export async function settleRollback(
  response: RollbackResponse | PendingApplyResponse,
  options?: PollOptions,
): Promise<RollbackResponse> {
  if (isPendingApply(response)) {
    const entry = await waitForApplyEntry(response.apply_id, options);
    return {
      snapshot_id: response.snapshot_id ?? entry.snapshot_id ?? 0,
      rule_version_id: response.rule_version_id ?? 0,
      health: 'confirmed',
    };
  }
  if (response.health !== 'observing') return response;
  throw new APIError('observing rollback response has no apply id', 'invalid_response', 500);
}

export async function settleBackupImport(
  response: BackupImportResult,
  options?: PollOptions,
): Promise<BackupImportResult> {
  if (!response.pending) return response;
  if (!response.apply_id) {
    throw new APIError('pending backup import has no apply id', 'invalid_response', 500);
  }
  const entry = await waitForApplyEntry(response.apply_id, options);
  const snapshotID = response.applied_snapshot_id ?? entry.snapshot_id;
  if (!snapshotID) {
    throw new APIError('terminal backup apply entry has no snapshot id', 'invalid_response', 500);
  }
  return {
    ...response,
    pending: false,
    applied: true,
    applied_snapshot_id: snapshotID,
    apply_result: {
      health: 'confirmed',
      rolled_back: false,
    },
  };
}

export interface ExitSwitchResponse {
  active: string;
  health: string;
  status?: 'pending';
  apply_id?: string;
  snapshot_id?: number;
}

export async function settleExitSwitch(
  response: ExitSwitchResponse,
  options?: PollOptions,
): Promise<ExitSwitchResponse> {
  if (response.status === 'pending' && response.apply_id) {
    await waitForApplyEntry(response.apply_id, options);
    return { active: response.active, health: 'confirmed', snapshot_id: response.snapshot_id };
  }
  if (response.health !== 'observing') return response;
  throw new APIError('observing exit switch has no apply id', 'invalid_response', 500);
}
