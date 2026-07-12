import { useAuthStore } from '../stores/auth';
import { useMeStore } from '../stores/me';

export class APIError extends Error {
  code: string;
  status: number;
  constructor(message: string, code: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

export function clearSession() {
  useAuthStore.getState().clear();
  useMeStore.getState().clear();
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
    clearSession();
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

export interface BootstrapStatus { needs_setup: boolean; needs_wizard?: boolean }
export interface LoginResponse { token: string }
export interface Me { user_id: number; username: string }

// Panel-managed settings surfaced via GET/POST /api/v1/settings/panel.
// Read-side (Response): TGBot echoes token_set + token_masked only.
// Write-side (Update): every field is optional; unset fields are left
// as-is in the DB. token is sent as a raw string on update.
export interface PanelSettings {
  server: { domain: string; panel_bind: string; panel_port: number };
  tls: { acme_enabled: boolean; acme_email: string };
  tgbot: { token_set: boolean; token_masked?: string; admin_chat_ids: number[] };
  washim: {
    enabled: boolean;
    listen: string;
    port: number;
    backend: string;
    wa_host: string;
    allow_cidr: string[];
  };
  wizard: { complete: boolean };
}

export interface PanelSettingsUpdate {
  server?: { domain?: string; panel_bind?: string; panel_port?: number };
  tls?: { acme_enabled?: boolean; acme_email?: string };
  tgbot?: { token?: string; admin_chat_ids?: number[] };
  washim?: {
    enabled?: boolean;
    listen?: string;
    port?: number;
    backend?: string;
    wa_host?: string;
    allow_cidr?: string[];
  };
  wizard?: { complete?: boolean };
}

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
  // group_id identifies materialized ruleset entries. Historical one-shot
  // imports may also carry it, so only values matching a registered ruleset
  // are managed expansion metadata.
  group_id?: string;
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
  // group_id every returned rule carries. Frontend uses it to collapse
  // the batch into one card in the Rules list.
  group_id: string;
  // source_format is populated when the auto-detector fell into a
  // non-native path (currently only "gfwlist"). Empty for plain Clash.
  source_format?: string;
}
export interface ApplyResponse {
  snapshot_id: number;
  rule_version_id: number;
  rolled_back: boolean;
  health: string;
  reason?: string;
}

export interface PendingApplyResponse {
  apply_id: string;
  hash: string;
  status: 'pending';
  snapshot_id?: number;
  rule_version_id?: number;
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
  active?: boolean;
  rollbackable: boolean;
}
export interface SnapshotsResponse { snapshots: Snapshot[] }
export interface RollbackResponse {
  snapshot_id: number;
  rule_version_id: number;
  health?: string;
}

export interface ApplyResult {
  health: string;
  rolled_back: boolean;
  reason?: string;
}

// Ruleset (native rule-provider) — one row in the rulesets table.
// last_synced_at is Unix epoch or null; the panel renders "just now" /
// "3 hours ago" from it.
export interface RulesetView {
  name: string;
  source_url: string;
  kind: string;
  action: string;
  priority: number;
  enabled: boolean;
  rule_count: number;
  last_synced_at?: number;
  last_error?: string;
  created_at: number;
  created_by?: string;
}
export interface RulesetsResponse { rulesets: RulesetView[] }
export interface RegisterRulesetRequest {
  name?: string;
  source_url: string;
  kind?: string;
  action: string;
  priority?: number;
  enabled?: boolean;
}

// DNS Plane observability — GET /api/v1/metrics/dns (Dashboard's
// DNSPlaneCard polls this every 5s). Nil-safe on the backend: when the
// DNS front-door isn't wired in, counters are zero and every listener
// reports "not_configured".
export type DNSListenerHealth = 'healthy' | 'degraded' | 'not_configured';

export interface DNSListenerStatus {
  udp53: DNSListenerHealth;
  tcp53: DNSListenerHealth;
  dot: DNSListenerHealth;
  doh: DNSListenerHealth;
}

export interface DNSCertStatus {
  domain: string;
  not_after_unix: number;
  days_until_expiry: number;
}

export interface DNSMetrics {
  queries_total: number;
  hits_block: number;
  hits_direct: number;
  hits_proxy: number;
  upstream_errors: number;
  refused_axfr: number;
  listeners: DNSListenerStatus;
  cert: DNSCertStatus | null;
}

export function getDNSMetrics(): Promise<DNSMetrics> {
  return api.get<DNSMetrics>('/api/v1/metrics/dns');
}

export interface BackupImportResult {
  entries: number;
  total_bytes: number;
  applied: boolean;
  pending?: boolean;
  apply_id?: string;
  status?: 'pending' | 'succeeded' | 'failed';
  ignored_entries?: string[];
  note?: string;
  applied_snapshot_id?: number;
  apply_result?: ApplyResult;
}
