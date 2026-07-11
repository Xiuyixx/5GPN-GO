import { useEffect, useMemo, useState } from 'react';
import { useTranslation, Trans } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Dialog, DialogActions, DialogBody, DialogDescription, DialogTitle } from '../components/ui/dialog';
import { Textarea } from '../components/ui/textarea';
import { api } from '../api/client';
import type { ApplyResponse, DryRunResponse, ExitsResponse, ImportRulesResponse, RegisterRulesetRequest, Rule, RuleKind, RulesetView, RulesetsResponse } from '../api/client';

const KINDS: RuleKind[] = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD',
  'GEOSITE', 'GEOIP', 'IP-CIDR', 'RULE-SET', 'MATCH',
];

// Special actions that are always valid regardless of exits.
const SPECIAL_ACTIONS = ['direct', 'block'];

interface Draft extends Rule {
  _key: string;
}

interface FixtureDraft {
  _key: string;
  domain: string;
  expected_exit: string;
}

let keyCounter = 0;
function makeKey(): string {
  keyCounter += 1;
  return `k${Date.now().toString(36)}-${keyCounter}`;
}

function toDraft(r: Rule): Draft {
  return { ...r, _key: makeKey() };
}

// Rulesets are rendered from the /api/v1/rulesets snapshot instead of
// from group_id in draft — the Segment machinery that grouped by
// group_id has been retired in v0.2.5-rc3. Draft entries carrying a
// group_id are hidden from the Rules table since their canonical view
// lives in a Ruleset card.

export default function Rules() {
  const { t } = useTranslation();
  const [active, setActive] = useState<Rule[]>([]);
  const [draft, setDraft] = useState<Draft[]>([]);
  const [exits, setExits] = useState<string[]>([]);
  const [fixtures, setFixtures] = useState<FixtureDraft[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [applyRes, setApplyRes] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState<'idle' | 'dryrun' | 'apply'>('idle');
  const [note, setNote] = useState('');
  const [dryOk, setDryOk] = useState(false);

  // Rulesets are the panel's native rule-provider registrations. Each
  // row is a URL 5GPN periodically syncs; Apply expands cached content
  // into the effective mihomo config. Manual rules stay untouched.
  const [rulesets, setRulesets] = useState<RulesetView[]>([]);
  // Snapshot of rulesets as of the last refresh — used for isDirty.
  // Ruleset registration / deletion / enable-toggle updates `rulesets`
  // immediately but leaves `activeRulesets` unchanged until the next
  // refresh (which happens after Apply). Any divergence => dirty.
  const [activeRulesets, setActiveRulesets] = useState<RulesetView[]>([]);
  const [syncingRs, setSyncingRs] = useState<string | null>(null);

  // Ruleset import dialog state. Keeps the operator in control: fetch or
  // paste → preview counts → confirm merges rules into draft.
  const [importOpen, setImportOpen] = useState(false);
  const [importUrl, setImportUrl] = useState('');
  const [importText, setImportText] = useState('');
  const [importMode, setImportMode] = useState<'url' | 'text'>('url');
  const [importAction, setImportAction] = useState('PROXY');
  const [importBusy, setImportBusy] = useState(false);
  const [importPreview, setImportPreview] = useState<ImportRulesResponse | null>(null);
  const [importError, setImportError] = useState<string | null>(null);

  // Available action options: direct/block + every configured exit id.
  const actionOptions = useMemo(() => [...SPECIAL_ACTIONS, ...exits], [exits]);

  async function refresh() {
    try {
      const [rulesRes, exitsRes, rulesetsRes] = await Promise.all([
        api.get<{ rules: Rule[] }>('/api/v1/rules'),
        api.get<ExitsResponse>('/api/v1/exits'),
        api.get<RulesetsResponse>('/api/v1/rulesets'),
      ]);
      // GET /api/v1/rules serves the last-applied rules_yaml verbatim,
      // which for historical reasons can include rules materialized by a
      // previous Ruleset.Expand() (they carry a non-empty group_id). The
      // canonical view of those rows lives in Ruleset cards; the manual
      // rules table only manages ungrouped rows. Filter here so both
      // `active` (dirty baseline) and `draft` (editor state) are the
      // pure manual set. The server strips group_id on apply anyway.
      const list = (rulesRes.rules ?? []).filter((r) => !r.group_id);
      const rsList = rulesetsRes.rulesets ?? [];
      setActive(list);
      setDraft(list.map(toDraft));
      setExits((exitsRes.exits ?? []).map((e) => e.id));
      setRulesets(rsList);
      setActiveRulesets(rsList);
      setDry(null);
      setApplyRes(null);
      setDryOk(false);
      setEditing(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function syncRuleset(name: string) {
    setSyncingRs(name);
    try {
      const updated = await api.post<RulesetView>(`/api/v1/rulesets/${encodeURIComponent(name)}/sync`);
      setRulesets((rs) => rs.map((r) => (r.name === name ? updated : r)));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSyncingRs(null);
    }
  }
  async function toggleRuleset(name: string, enabled: boolean) {
    try {
      const updated = await api.post<RulesetView>(
        `/api/v1/rulesets/${encodeURIComponent(name)}/enabled`, { enabled });
      setRulesets((rs) => rs.map((r) => (r.name === name ? updated : r)));
      invalidate();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }
  async function removeRuleset(name: string) {
    try {
      await api.del(`/api/v1/rulesets/${encodeURIComponent(name)}`);
      setRulesets((rs) => rs.filter((r) => r.name !== name));
      invalidate();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => { refresh(); }, []);

  const isDirty = useMemo(() => {
    if (draft.length !== active.length) return true;
    const rulesChanged = draft.some((r, i) => {
      const a = active[i];
      return !a
        || r.id !== a.id || r.kind !== a.kind || r.pattern !== a.pattern
        || r.action !== a.action || r.priority !== a.priority || r.enabled !== a.enabled;
    });
    if (rulesChanged) return true;
    // Rulesets contribute to the effective config on Apply, so any
    // change (add/remove/toggle/action edit) must count as dirty even
    // if the manual rules table is untouched. Compare against the
    // last-refreshed snapshot; server-side sync-metadata changes
    // (last_synced_at, rule_count, last_error) do not count.
    if (rulesets.length !== activeRulesets.length) return true;
    const byName = new Map(activeRulesets.map((r) => [r.name, r]));
    return rulesets.some((r) => {
      const a = byName.get(r.name);
      return !a
        || r.action !== a.action || r.priority !== a.priority
        || r.enabled !== a.enabled || r.source_url !== a.source_url
        || r.kind !== a.kind;
    });
  }, [draft, active, rulesets, activeRulesets]);

  function invalidate() {
    setDry(null);
    setDryOk(false);
    setApplyRes(null);
  }

  function patch(key: string, changes: Partial<Rule>) {
    setDraft((d) => d.map((r) => (r._key === key ? { ...r, ...changes } : r)));
    invalidate();
  }

  function addRow() {
    const priority = draft.length
      ? Math.max(...draft.map((r) => r.priority)) + 10
      : 10;
    const defaultAction = actionOptions[0] || 'direct';
    const _key = makeKey();
    setDraft((d) => [
      ...d,
      { _key, id: `rule-${d.length + 1}`, kind: 'DOMAIN-SUFFIX', pattern: '', action: defaultAction, priority, enabled: true },
    ]);
    setEditing(_key);
    invalidate();
  }

  function removeRow(key: string) {
    setDraft((d) => d.filter((r) => r._key !== key));
    if (editing === key) setEditing(null);
    invalidate();
  }


  function move(key: string, dir: -1 | 1) {
    setDraft((d) => {
      const idx = d.findIndex((r) => r._key === key);
      const target = idx + dir;
      if (idx < 0 || target < 0 || target >= d.length) return d;
      const clone = d.slice();
      [clone[idx], clone[target]] = [clone[target], clone[idx]];
      return clone.map((r, i) => ({ ...r, priority: (i + 1) * 10 }));
    });
    invalidate();
  }

  function stripDrafts(list: Draft[]): Rule[] {
    return list.map(({ _key, ...r }) => r);
  }

  // Fixture editing.
  function addFixture() {
    setFixtures((f) => [
      ...f,
      { _key: makeKey(), domain: '', expected_exit: actionOptions[0] || 'direct' },
    ]);
    invalidate();
  }
  function patchFixture(key: string, changes: Partial<FixtureDraft>) {
    setFixtures((f) => f.map((x) => (x._key === key ? { ...x, ...changes } : x)));
    invalidate();
  }
  function removeFixture(key: string) {
    setFixtures((f) => f.filter((x) => x._key !== key));
    invalidate();
  }

  async function runDryRun() {
    setError(null); setBusy('dryrun'); setDry(null);
    try {
      const cleanFixtures = fixtures
        .filter((f) => f.domain.trim())
        .map(({ _key, ...f }) => f);
      const res = await api.post<DryRunResponse>('/api/v1/rules/dry-run', {
        rules: stripDrafts(draft),
        fixtures: cleanFixtures,
      });
      setDry(res);
      setDryOk(res.failed === 0);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy('idle');
    }
  }

  async function runApply() {
    setError(null); setBusy('apply'); setApplyRes(null);
    try {
      const res = await api.post<ApplyResponse>('/api/v1/rules/apply', {
        rules: stripDrafts(draft),
        note: note || 'panel edit',
      });
      setApplyRes(res);
      setNote('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy('idle');
    }
  }

  const failedDetails = dry?.results.filter((r) => !r.pass) ?? [];

  // Ruleset import: preview server-side, merge into draft on confirm.
  async function runImportPreview() {
    setImportBusy(true);
    setImportError(null);
    setImportPreview(null);
    try {
      const body: Record<string, unknown> = { action: importAction };
      if (importMode === 'url') body.url = importUrl.trim();
      else body.text = importText;
      const res = await api.post<ImportRulesResponse>('/api/v1/rules/import', body);
      setImportPreview(res);
    } catch (err) {
      setImportError(err instanceof Error ? err.message : String(err));
    } finally {
      setImportBusy(false);
    }
  }

  // URL mode: register as a native rule provider (rulesets table). The
  // panel syncs periodically + expands at apply time — nothing is
  // merged into the draft, so the manual rules table stays clean.
  const [importRulesetName, setImportRulesetName] = useState('');
  async function registerRulesetFromImport() {
    setImportBusy(true);
    setImportError(null);
    try {
      const body: RegisterRulesetRequest = {
        source_url: importUrl.trim(),
        action: importAction,
      };
      if (importRulesetName.trim()) body.name = importRulesetName.trim();
      const created = await api.post<RulesetView>('/api/v1/rulesets', body);
      setRulesets((rs) => [...rs.filter((r) => r.name !== created.name), created]);
      invalidate();
      setImportOpen(false);
      setImportPreview(null);
      setImportUrl('');
      setImportRulesetName('');
    } catch (err) {
      setImportError(err instanceof Error ? err.message : String(err));
    } finally {
      setImportBusy(false);
    }
  }

  // Text mode: no URL to sync from, so the pasted content lands
  // straight in the manual rules draft (legacy behavior).
  function confirmImport() {
    if (!importPreview) return;
    const existingIds = new Set(draft.map((r) => r.id));
    let priorityBase = draft.length
      ? Math.max(...draft.map((r) => r.priority))
      : 0;
    const merged: Draft[] = importPreview.rules.map((r) => {
      let id = r.id;
      let n = 2;
      while (existingIds.has(id)) {
        id = `${r.id}-${n++}`;
      }
      existingIds.add(id);
      priorityBase += 10;
      return { ...r, id, priority: priorityBase, _key: makeKey() };
    });
    setDraft((d) => [...d, ...merged]);
    setImportOpen(false);
    setImportPreview(null);
    setImportUrl('');
    setImportText('');
    invalidate();
  }

  return (
    <AppShell>
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div>
          <Heading>{t('nav.rules')}</Heading>
          <Text className="mt-1">
            {t('rules.tagline')}
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button plain onClick={refresh}>{t('rules.reset')}</Button>
          <Button plain onClick={addRow}>{t('rules.addRule')}</Button>
          <Button plain onClick={() => setImportOpen(true)}>{t('rules.importRuleset')}</Button>
          <Button color="zinc" onClick={runDryRun} disabled={busy !== 'idle'}>
            {busy === 'dryrun' ? t('rules.running') : t('rules.dryRun')}
          </Button>
          <Button color="indigo" onClick={runApply} disabled={busy !== 'idle' || !isDirty || !dryOk}>
            {busy === 'apply' ? t('rules.applying') : t('rules.apply')}
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-4 flex items-start justify-between gap-3 rounded-xl border border-red-400/40 bg-red-500/10 px-4 py-3 text-sm text-red-800 dark:text-red-200">
          <div>
            <div className="font-semibold">{t('rules.errorTitle')}</div>
            <div className="mt-0.5">{error}</div>
          </div>
          <button
            type="button"
            onClick={() => setError(null)}
            aria-label={t('rules.dismissError')}
            className="shrink-0 rounded px-2 py-0.5 text-red-700/70 hover:text-red-900 dark:text-red-300/70 dark:hover:text-red-100"
          >
            ✕
          </button>
        </div>
      )}
      {applyRes && (
        <div className={`mb-4 flex items-start justify-between gap-3 rounded-xl border px-4 py-3 text-sm ${
          applyRes.rolled_back
            ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200'
            : 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200'
        }`}>
          <div>
            <div className="font-semibold">
              {applyRes.rolled_back ? t('rules.applyRolledBack') : t('rules.applySucceeded')}
            </div>
            <div className="mt-0.5">
              {applyRes.rolled_back
                ? t('rules.applyResultDetailRolledBack', { id: applyRes.snapshot_id, health: applyRes.health })
                : t('rules.applyResultDetail', { id: applyRes.snapshot_id, health: applyRes.health })}
            </div>
          </div>
          <button
            type="button"
            onClick={() => setApplyRes(null)}
            aria-label={t('rules.dismissApplyResult')}
            className="shrink-0 rounded px-2 py-0.5 opacity-70 hover:opacity-100"
          >
            ✕
          </button>
        </div>
      )}
      {isDirty && !applyRes && (
        <div className={`mb-4 rounded-xl border px-4 py-3 text-sm ${
          dryOk
            ? 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200'
            : dry
              ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200'
              : 'border-amber-400/40 bg-amber-500/10 text-amber-800 dark:text-amber-200'
        }`}>
          <div className="font-semibold">{t('rules.unsavedChanges')}</div>
          <div className="mt-0.5">
            {dryOk
              ? t('rules.dryRunPassed', { count: dry?.passed ?? 0 })
              : dry
                ? t('rules.dryRunFailed', { count: dry.results.length, failed: failedDetails.length })
                : fixtures.filter((f) => f.domain.trim()).length === 0
                  ? t('rules.addFixtureHint')
                  : t('rules.runDryRunHint')}
          </div>
          {dry && !dryOk && failedDetails.length > 0 && (
            <ul className="mt-2 space-y-1 text-xs">
              {failedDetails.slice(0, 5).map((r) => (
                <li key={r.domain}>
                  <code className="rounded bg-red-500/10 px-1 py-0.5">{r.domain}</code>{' '}
                  <Trans
                    i18nKey="rules.fixtureFailedLine"
                    values={{ expected: r.expected_exit, actual: r.actual_exit || '—' }}
                    components={{ strong: <strong /> }}
                  />
                  {r.matched_rule
                    ? ' ' + t('rules.matchedRuleParen', { rule: r.matched_rule })
                    : ' ' + t('rules.noRuleMatchedParen')}
                </li>
              ))}
              {failedDetails.length > 5 && (
                <li className="text-red-700/70 dark:text-red-300/70">
                  {t('rules.andMoreSeeTable', { count: failedDetails.length - 5 })}
                </li>
              )}
            </ul>
          )}
        </div>
      )}

      {/*
        Rulesets and manual rules render as two distinct sections. The
        Rulesets section only shows when at least one imported batch is
        in the draft; each batch collapses to one card the operator can
        expand, bulk-toggle, or delete outright. The Rules table below
        is only ever populated with rules that carry no group_id.
      */}
      {(() => {
        // Rulesets are the source of truth for imported batches; render
        // them from the /api/v1/rulesets snapshot instead of from
        // group_id in draft. Draft's grouped rules are the result of the
        // last Apply's expansion — they are hidden from the Rules table
        // since the operator manages them via Ruleset cards, not row edits.
        const ungroupedDraftIndices: Array<{ rule: Draft; index: number }> = [];
        draft.forEach((rule, index) => {
          if (!rule.group_id) ungroupedDraftIndices.push({ rule, index });
        });

        const renderRow = (r: Draft, i: number, indented: boolean) => (
          <TableRow key={r._key} className="align-middle">
            <TableCell className={`metric text-zinc-500 ${indented ? 'pl-8' : ''}`}>{r.priority}</TableCell>
            <TableCell className="font-medium">{r.id}</TableCell>
            <TableCell>
              <Badge color="zinc">{r.kind}</Badge>
            </TableCell>
            <TableCell className="text-zinc-600 dark:text-zinc-300">
              {r.pattern
                ? <code className="text-xs">{r.pattern}</code>
                : <span className="text-zinc-400">—</span>}
            </TableCell>
            <TableCell>
              {actionOptions.includes(r.action)
                ? <code className="text-xs">{r.action}</code>
                : <span className="text-red-600 dark:text-red-400 text-xs">{t('rules.unknownAction', { action: r.action })}</span>}
            </TableCell>
            <TableCell>
              <button
                type="button"
                onClick={() => patch(r._key, { enabled: !r.enabled })}
                className="cursor-pointer"
                aria-label={r.enabled ? t('rules.disableRule') : t('rules.enableRule')}
              >
                <Badge color={r.enabled ? 'lime' : 'zinc'}>{r.enabled ? t('rules.statusOn') : t('rules.statusOff')}</Badge>
              </button>
            </TableCell>
            <TableCell className="text-right">
              <div className="flex justify-end gap-1">
                <Button plain aria-label={t('rules.moveUp')} onClick={() => move(r._key, -1)} disabled={i === 0}>↑</Button>
                <Button plain aria-label={t('rules.moveDown')} onClick={() => move(r._key, 1)} disabled={i === draft.length - 1}>↓</Button>
                <Button plain onClick={() => setEditing(editing === r._key ? null : r._key)}>
                  {editing === r._key ? t('common.close') : t('rules.editButton')}
                </Button>
                <Button plain aria-label={t('rules.deleteAria')} onClick={() => removeRow(r._key)}>✕</Button>
              </div>
            </TableCell>
          </TableRow>
        );

        const rsHeader = (
          <div className="mb-3">
            <Heading level={2}>{t('rules.sectionRulesets')}</Heading>
            <Text className="mt-1 text-xs">{t('rules.sectionRulesetsHelp')}</Text>
          </div>
        );

        return (
          <>
            <div className="glass mb-4 p-4">
              {rsHeader}
              {rulesets.length === 0 ? (
                <Text className="text-xs text-zinc-500">{t('rules.rulesetsEmpty')}</Text>
              ) : (
                <div className="space-y-3">
                  {rulesets.map((rs) => {
                    const lastSyncedLabel = rs.last_synced_at
                      ? new Date(rs.last_synced_at * 1000).toLocaleString()
                      : t('rules.rulesetNever');
                    return (
                      <div
                        key={rs.name}
                        className={`rounded-xl border p-3 ${
                          rs.enabled
                            ? 'border-indigo-500/30 bg-indigo-500/5 dark:bg-indigo-500/10'
                            : 'border-zinc-400/30 bg-zinc-100/40 dark:bg-zinc-800/40'
                        }`}
                      >
                        <div className="flex flex-wrap items-center gap-3">
                          <span className="font-mono text-sm text-indigo-700 dark:text-indigo-300">
                            {rs.name}
                          </span>
                          <Badge color={rs.enabled ? 'indigo' : 'zinc'}>
                            {t('rules.groupHeaderCount', { count: rs.rule_count })}
                          </Badge>
                          <span className="text-xs text-zinc-500">
                            {t('rules.rulesetActionLabel')}: <code className="text-xs">{rs.action}</code>
                          </span>
                          <div className="ml-auto flex flex-wrap gap-1">
                            <Button
                              plain
                              onClick={() => syncRuleset(rs.name)}
                              disabled={syncingRs === rs.name}
                            >
                              {syncingRs === rs.name ? t('rules.rulesetSyncing') : t('rules.rulesetSyncNow')}
                            </Button>
                            <Button plain onClick={() => toggleRuleset(rs.name, !rs.enabled)}>
                              {rs.enabled ? t('rules.groupDisableAll') : t('rules.groupEnableAll')}
                            </Button>
                            <Button
                              plain
                              onClick={() => {
                                if (window.confirm(t('rules.rulesetDeleteConfirm', { name: rs.name }))) {
                                  removeRuleset(rs.name);
                                }
                              }}
                            >
                              {t('rules.groupDeleteAll')}
                            </Button>
                          </div>
                        </div>
                        <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs">
                          <dt className="text-zinc-500">{t('rules.rulesetSourceLabel')}</dt>
                          <dd className="truncate font-mono text-[11px]">{rs.source_url}</dd>
                          <dt className="text-zinc-500">{t('rules.rulesetLastSyncedLabel')}</dt>
                          <dd>{lastSyncedLabel}</dd>
                          {rs.last_error && (
                            <>
                              <dt className="text-red-600 dark:text-red-400">error</dt>
                              <dd className="text-red-600 dark:text-red-400">
                                {t('rules.rulesetSyncError', { err: rs.last_error })}
                              </dd>
                            </>
                          )}
                        </dl>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

            <div className="glass p-2">
              <div className="mb-2 px-4 pt-3">
                <Heading level={2}>{t('rules.sectionRules')}</Heading>
              </div>
              {ungroupedDraftIndices.length === 0 ? (
                <div className="p-6 text-center">
                  <Text>
                    <Trans
                      i18nKey="rules.noRulesYet"
                      components={{ strong: <strong /> }}
                    />
                  </Text>
                </div>
              ) : (
                <Table>
                  <TableHead>
                    <TableRow>
                      <TableHeader className="w-16">{t('rules.thPriority')}</TableHeader>
                      <TableHeader>{t('rules.thId')}</TableHeader>
                      <TableHeader>{t('rules.thKind')}</TableHeader>
                      <TableHeader>{t('rules.thPattern')}</TableHeader>
                      <TableHeader>{t('rules.thAction')}</TableHeader>
                      <TableHeader className="w-24">{t('rules.thStatus')}</TableHeader>
                      <TableHeader className="w-40 text-right">{t('rules.thActions')}</TableHeader>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {ungroupedDraftIndices.map(({ rule, index }) => renderRow(rule, index, false))}
                  </TableBody>
                </Table>
              )}
            </div>
          </>
        );
      })()}

      {editing && (() => {
        const r = draft.find((x) => x._key === editing);
        if (!r) return null;
        const isMatch = r.kind === 'MATCH';
        return (
          <div className="glass fade-up mt-4 p-6">
            <div className="mb-4 flex items-center justify-between">
              <Heading level={2}>{t('rules.editRule')}</Heading>
              <Button plain onClick={() => setEditing(null)}>{t('common.close')}</Button>
            </div>
            <Fieldset>
              <Legend className="sr-only">{t('rules.fieldsLegend')}</Legend>
              <FieldGroup>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <Field>
                    <Label>{t('rules.idLabel')}</Label>
                    <Input value={r.id} onChange={(e) => patch(r._key, { id: e.target.value })} />
                  </Field>
                  <Field>
                    <Label>{t('rules.priorityLabel')}</Label>
                    <Input
                      type="number"
                      value={r.priority}
                      onChange={(e) => patch(r._key, { priority: Number(e.target.value) })}
                    />
                  </Field>
                  <Field>
                    <Label>{t('rules.kindLabel')}</Label>
                    <Select value={r.kind} onChange={(e) => patch(r._key, { kind: e.target.value as RuleKind })}>
                      {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
                    </Select>
                  </Field>
                  <Field>
                    <Label>
                      {t('rules.actionLabel')}{' '}
                      <span className="text-zinc-500 text-xs">
                        {t('rules.actionHint')}
                      </span>
                    </Label>
                    <Select value={r.action} onChange={(e) => patch(r._key, { action: e.target.value })}>
                      {!actionOptions.includes(r.action) && (
                        <option value={r.action}>{t('rules.unknownWillFailDryRun', { action: r.action })}</option>
                      )}
                      {actionOptions.map((a) => <option key={a} value={a}>{a}</option>)}
                    </Select>
                    {exits.length === 0 && (
                      <p className="mt-1 text-xs text-amber-700 dark:text-amber-300">
                        <Trans
                          i18nKey="rules.noCustomExits"
                          components={{ exitsLink: <a className="underline" href="/exits" /> }}
                        />
                      </p>
                    )}
                  </Field>
                  <Field className="sm:col-span-2">
                    <Label>{t('rules.patternLabel')} {isMatch && <span className="text-zinc-500">{t('rules.patternDisabledForMatch')}</span>}</Label>
                    <Input
                      value={r.pattern}
                      onChange={(e) => patch(r._key, { pattern: e.target.value })}
                      disabled={isMatch}
                      placeholder={
                        r.kind === 'DOMAIN-SUFFIX' ? 'example.com'
                        : r.kind === 'IP-CIDR' ? '10.0.0.0/8'
                        : r.kind === 'GEOSITE' ? 'cn'
                        : t('rules.patternPlaceholder')
                      }
                    />
                  </Field>
                </div>
              </FieldGroup>
            </Fieldset>
            <div className="mt-4 flex justify-between">
              <Button plain onClick={() => { removeRow(r._key); }}>{t('rules.deleteRule')}</Button>
              <Button color="zinc" onClick={() => setEditing(null)}>{t('rules.done')}</Button>
            </div>
          </div>
        );
      })()}

      {/* Fixtures editor: what Dry-run tests against. Empty = no assertions,
          which means Dry-run only checks that the rules are syntactically
          valid and can be rendered. */}
      <div className="glass mt-6 p-6">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <Heading level={2}>{t('rules.dryRunFixturesHeading')}</Heading>
            <Text className="mt-1 text-sm">
              {t('rules.dryRunFixturesHelp')}
            </Text>
          </div>
          <Button plain onClick={addFixture}>{t('rules.addFixture')}</Button>
        </div>
        {fixtures.length === 0 ? (
          <div className="rounded border border-zinc-200/50 bg-zinc-50/40 px-4 py-3 text-sm text-zinc-600 dark:border-zinc-700/50 dark:bg-zinc-800/40 dark:text-zinc-400">
            {t('rules.noFixtures')}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeader>{t('rules.thDomain')}</TableHeader>
                <TableHeader className="w-56">{t('rules.thExpectedExit')}</TableHeader>
                <TableHeader className="w-16 text-right">{t('rules.thRemove')}</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {fixtures.map((f) => (
                <TableRow key={f._key} className="align-middle">
                  <TableCell>
                    <Input
                      value={f.domain}
                      onChange={(e) => patchFixture(f._key, { domain: e.target.value })}
                      placeholder={t('rules.domainPlaceholder')}
                    />
                  </TableCell>
                  <TableCell>
                    <Select
                      value={f.expected_exit}
                      onChange={(e) => patchFixture(f._key, { expected_exit: e.target.value })}
                    >
                      {!actionOptions.includes(f.expected_exit) && (
                        <option value={f.expected_exit}>{t('rules.unknownAction', { action: f.expected_exit })}</option>
                      )}
                      {actionOptions.map((a) => <option key={a} value={a}>{a}</option>)}
                    </Select>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button plain aria-label={t('rules.removeFixture')} onClick={() => removeFixture(f._key)}>✕</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>

      <form className="glass mt-6 p-6" onSubmit={(e) => e.preventDefault()}>
        <Fieldset>
          <Legend>{t('rules.applyMetadata')}</Legend>
          <FieldGroup>
            <Field>
              <Label>{t('rules.noteLabel')}</Label>
              <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('rules.notePlaceholder')} />
            </Field>
          </FieldGroup>
        </Fieldset>
      </form>

      {dry && (
        <div className="mt-8">
          <Heading level={2}>{t('rules.dryRunResults', { passed: dry.passed, failed: dry.failed })}</Heading>
          <div className="glass mt-4 p-2">
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeader>{t('rules.thDomain')}</TableHeader>
                  <TableHeader>{t('rules.thMatchedRule')}</TableHeader>
                  <TableHeader>{t('rules.thKind')}</TableHeader>
                  <TableHeader>{t('rules.thActual')}</TableHeader>
                  <TableHeader>{t('rules.thExpected')}</TableHeader>
                  <TableHeader>{t('rules.thStatus')}</TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {dry.results.map((r) => (
                  <TableRow key={r.domain}>
                    <TableCell className="font-medium">{r.domain}</TableCell>
                    <TableCell>{r.matched_rule || '—'}</TableCell>
                    <TableCell>{r.matched_kind || '—'}</TableCell>
                    <TableCell>{r.actual_exit || '—'}</TableCell>
                    <TableCell>{r.expected_exit}</TableCell>
                    <TableCell>
                      <Badge color={r.pass ? 'lime' : 'red'}>{r.pass ? t('rules.pass') : r.failure_reason ?? t('rules.fail')}</Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      )}

      <Dialog open={importOpen} onClose={() => setImportOpen(false)} size="2xl">
        <DialogTitle>{t('rules.importRulesetTitle')}</DialogTitle>
        <DialogDescription>
          <Trans
            i18nKey="rules.importDescription"
            components={{ code: <code /> }}
          />
        </DialogDescription>
        <DialogBody>
          <div className="mb-4 flex gap-2">
            {importMode === 'url'
              ? <Button color="zinc" onClick={() => setImportMode('url')}>{t('rules.importUrlTab')}</Button>
              : <Button plain onClick={() => setImportMode('url')}>{t('rules.importUrlTab')}</Button>}
            {importMode === 'text'
              ? <Button color="zinc" onClick={() => setImportMode('text')}>{t('rules.importPasteTab')}</Button>
              : <Button plain onClick={() => setImportMode('text')}>{t('rules.importPasteTab')}</Button>}
          </div>

          {importMode === 'url' ? (
            <Fieldset>
              <FieldGroup>
                <div className="mb-2 rounded-lg border border-indigo-500/30 bg-indigo-500/5 p-2 text-xs text-indigo-800 dark:text-indigo-200">
                  {t('rules.importAsRulesetHelp')}
                </div>
                <Field>
                  <Label>{t('rules.importUrlLabel')}</Label>
                  <Input
                    value={importUrl}
                    onChange={(e) => setImportUrl(e.target.value)}
                    placeholder="https://example.com/proxy.txt"
                  />
                  <p className="mt-1 text-xs text-zinc-500">
                    {t('rules.importUrlHelp')}
                  </p>
                </Field>
                <Field>
                  <Label>{t('rules.importRulesetNameLabel')}</Label>
                  <Input
                    value={importRulesetName}
                    onChange={(e) => setImportRulesetName(e.target.value)}
                    placeholder="gfwlist"
                  />
                  <p className="mt-1 text-xs text-zinc-500">
                    {t('rules.importRulesetNameHelp')}
                  </p>
                </Field>
              </FieldGroup>
            </Fieldset>
          ) : (
            <Fieldset>
              <FieldGroup>
                <Field>
                  <Label>{t('rules.importPasteLabel')}</Label>
                  <Textarea
                    rows={10}
                    value={importText}
                    onChange={(e) => setImportText(e.target.value)}
                    placeholder="DOMAIN-SUFFIX,google.com,Proxy&#10;DOMAIN-KEYWORD,openai,Proxy&#10;IP-CIDR,10.0.0.0/8,DIRECT&#10;FINAL,Proxy"
                  />
                </Field>
              </FieldGroup>
            </Fieldset>
          )}

          <div className="mt-4">
            <Fieldset>
              <FieldGroup>
                <Field>
                  <Label>
                    {t('rules.rewritePoliciesLabel')}{' '}
                    <span className="text-zinc-500 text-xs">
                      {t('rules.rewritePoliciesHint')}
                    </span>
                  </Label>
                  <Select value={importAction} onChange={(e) => setImportAction(e.target.value)}>
                    {!actionOptions.includes(importAction) && (
                      <option value={importAction}>{t('rules.unknownDryRunWillFlag', { action: importAction })}</option>
                    )}
                    {actionOptions.map((a) => <option key={a} value={a}>{a}</option>)}
                  </Select>
                </Field>
              </FieldGroup>
            </Fieldset>
          </div>

          {importError && (
            <div className="mt-4 rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-sm text-red-800 dark:text-red-200">
              {importError}
            </div>
          )}

          {importPreview && (
            <div className="mt-4 rounded-xl border border-emerald-400/40 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-800 dark:text-emerald-200">
              <div className="font-semibold">{t('rules.previewLabel')}</div>
              <div className="mt-1">
                {t('rules.previewRulesReady', { count: importPreview.rules.length })}
                {' · '}
                {t('rules.previewDropped', { count: importPreview.dropped })}
                {importPreview.categories.length > 0 && (
                  <> · {t('rules.previewCategories', {
                    list: importPreview.categories.slice(0, 5).join(', ')
                      + (importPreview.categories.length > 5 ? '…' : ''),
                  })}</>
                )}
              </div>
              {importPreview.source_url && (
                <div className="mt-1 text-xs opacity-75">{t('rules.previewSource', { url: importPreview.source_url })}</div>
              )}
            </div>
          )}
        </DialogBody>
        <DialogActions>
          <Button plain onClick={() => { setImportOpen(false); setImportPreview(null); setImportError(null); }}>
            {t('common.cancel')}
          </Button>
          {!importPreview && (
            <Button color="zinc" onClick={runImportPreview}
                    disabled={importBusy
                              || (importMode === 'url' && !importUrl.trim())
                              || (importMode === 'text' && !importText.trim())}>
              {importBusy ? t('rules.fetching') : t('rules.preview')}
            </Button>
          )}
          {importPreview && (
            <>
              <Button plain onClick={() => setImportPreview(null)}>{t('rules.back')}</Button>
              {importMode === 'url' ? (
                <Button
                  color="indigo"
                  onClick={registerRulesetFromImport}
                  disabled={importPreview.rules.length === 0 || importBusy}
                >
                  {importBusy ? t('rules.rulesetRegisterInProgress') : t('rules.rulesetRegister')}
                </Button>
              ) : (
                <Button color="indigo" onClick={confirmImport} disabled={importPreview.rules.length === 0}>
                  {t('rules.mergeRules', { count: importPreview.rules.length })}
                </Button>
              )}
            </>
          )}
        </DialogActions>
      </Dialog>
    </AppShell>
  );
}
