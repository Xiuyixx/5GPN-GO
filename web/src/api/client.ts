import { useAuthStore } from '../stores/auth';

export class APIError extends Error {
  code: string;
  status: number;
  constructor(message: string, code: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = useAuthStore.getState().token;
  if (token) headers.Authorization = `Bearer ${token}`;

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (res.status === 401) {
    useAuthStore.getState().clear();
  }

  if (res.status === 204) return undefined as T;

  const contentType = res.headers.get('content-type') ?? '';
  const isJSON = contentType.includes('application/json');
  const payload = isJSON ? await res.json() : await res.text();

  if (!res.ok) {
    const errBody = isJSON ? (payload as { error?: string; message?: string }) : { message: String(payload) };
    throw new APIError(errBody.message ?? 'request failed', errBody.error ?? 'error', res.status);
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
};

export interface BootstrapStatus { needs_setup: boolean }
export interface LoginResponse { token: string }
export interface Me { user_id: number; username: string }

export type RuleKind =
  | 'DOMAIN' | 'DOMAIN-SUFFIX' | 'DOMAIN-KEYWORD'
  | 'GEOSITE' | 'GEOIP' | 'IP-CIDR' | 'RULE-SET' | 'MATCH';

export interface Rule {
  id: string;
  kind: RuleKind;
  pattern: string;
  action: string;
  priority: number;
  enabled: boolean;
  notes?: string;
}

export interface Fixture { domain: string; ip?: string; expected_exit: string; notes?: string }
export interface DryRunResult {
  domain: string;
  matched_rule: string;
  matched_kind: string;
  actual_exit: string;
  expected_exit: string;
  pass: boolean;
  failure_reason?: string;
}
export interface DryRunResponse { results: DryRunResult[]; passed: number; failed: number }

export interface ImportRulesRequest {
  url?: string;
  text?: string;
  action?: string;
  id_prefix?: string;
  starting_priority?: number;
  keep_categories?: string[];
  direct_categories?: string[];
}
export interface ImportRulesResponse {
  rules: Rule[];
  converted: number;
  dropped: number;
  categories: string[];
  source_url?: string;
  source_kind: 'url' | 'text';
}
export interface ApplyResponse {
  snapshot_id: number;
  rule_version_id: number;
  rolled_back: boolean;
  health: string;
  reason?: string;
}

export interface ExitSummary {
  id: string;
  protocol: string;
  server: string;
  port: number;
  active: boolean;
  notes?: string;
}
export interface ExitsResponse { exits: ExitSummary[]; active: string }

export interface MetricsSample {
  ts: string;
  cpu: number;
  mem_bytes: number;
  conns: number;
  tx_bytes: number;
  rx_bytes: number;
}

export interface Snapshot {
  id: number;
  created_at: string;
  config_hash: string;
  note?: string;
}
export interface SnapshotsResponse { snapshots: Snapshot[] }
export interface RollbackResponse { snapshot_id: number; rule_version_id: number }

export interface ApplyResult {
  health: string;
  rolled_back: boolean;
  reason?: string;
}

export interface BackupImportResult {
  entries: number;
  total_bytes: number;
  applied: boolean;
  note?: string;
  applied_snapshot_id?: number;
  apply_result?: ApplyResult;
}
