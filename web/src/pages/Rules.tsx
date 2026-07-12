import { useEffect, useMemo, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { settleRuleApply } from '../api/apply';
import { api } from '../api/client';
import type {
  ApplyResponse,
  DryRunResponse,
  ExitsResponse,
  ImportRulesResponse,
  PendingApplyResponse,
  RegisterRulesetRequest,
  Rule,
  RulesetView,
  RulesetsResponse,
} from '../api/client';
import { RulesEditor } from './rules/RulesEditor';
import { RulesFixtures } from './rules/RulesFixtures';
import { RulesHeader } from './rules/RulesHeader';
import { RulesImportDialog } from './rules/RulesImportDialog';
import {
  makeKey,
  mergeImportedRules,
  SPECIAL_ACTIONS,
  stripDrafts,
  toDraft,
} from './rules/model';
import type { Draft, FixtureDraft } from './rules/model';

export default function Rules() {
  const [active, setActive] = useState<Rule[]>([]);
  const [draft, setDraft] = useState<Draft[]>([]);
  const [exits, setExits] = useState<string[]>([]);
  const [fixtures, setFixtures] = useState<FixtureDraft[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dry, setDry] = useState<DryRunResponse | null>(null);
  const [applyResult, setApplyResult] = useState<ApplyResponse | null>(null);
  const [busy, setBusy] = useState<'idle' | 'dryrun' | 'apply'>('idle');
  const [note, setNote] = useState('');
  const [dryOk, setDryOk] = useState(false);
  const [rulesets, setRulesets] = useState<RulesetView[]>([]);
  const [activeRulesets, setActiveRulesets] = useState<RulesetView[]>([]);
  const [syncingRuleset, setSyncingRuleset] = useState<string | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [importUrl, setImportUrl] = useState('');
  const [importText, setImportText] = useState('');
  const [importMode, setImportMode] = useState<'url' | 'text'>('url');
  const [importAction, setImportAction] = useState('PROXY');
  const [importBusy, setImportBusy] = useState(false);
  const [importPreview, setImportPreview] = useState<ImportRulesResponse | null>(null);
  const [importError, setImportError] = useState<string | null>(null);
  const [importRulesetName, setImportRulesetName] = useState('');

  const actionOptions = useMemo(() => [...SPECIAL_ACTIONS, ...exits], [exits]);

  async function refresh() {
    try {
      const [rulesResponse, exitsResponse, rulesetsResponse] = await Promise.all([
        api.get<{ rules: Rule[] }>('/api/v1/rules'),
        api.get<ExitsResponse>('/api/v1/exits'),
        api.get<RulesetsResponse>('/api/v1/rulesets'),
      ]);
      const nextRulesets = rulesetsResponse.rulesets ?? [];
      const managedGroups = new Set(nextRulesets.map((ruleset) => ruleset.name));
      const manualRules = (rulesResponse.rules ?? []).filter(
        (rule) => !rule.group_id || !managedGroups.has(rule.group_id),
      );
      setActive(manualRules);
      setDraft(manualRules.map(toDraft));
      setExits((exitsResponse.exits ?? []).map((exit) => exit.id));
      setRulesets(nextRulesets);
      setActiveRulesets(nextRulesets);
      setDry(null);
      setApplyResult(null);
      setDryOk(false);
      setEditing(null);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  useEffect(() => { void refresh(); }, []);

  const isDirty = useMemo(() => {
    if (draft.length !== active.length) return true;
    if (draft.some((rule, index) => {
      const baseline = active[index];
      return !baseline
        || rule.id !== baseline.id
        || rule.kind !== baseline.kind
        || rule.pattern !== baseline.pattern
        || rule.action !== baseline.action
        || rule.priority !== baseline.priority
        || rule.enabled !== baseline.enabled;
    })) return true;
    if (rulesets.length !== activeRulesets.length) return true;
    const baselineByName = new Map(activeRulesets.map((ruleset) => [ruleset.name, ruleset]));
    return rulesets.some((ruleset) => {
      const baseline = baselineByName.get(ruleset.name);
      return !baseline
        || ruleset.action !== baseline.action
        || ruleset.priority !== baseline.priority
        || ruleset.enabled !== baseline.enabled
        || ruleset.source_url !== baseline.source_url
        || ruleset.kind !== baseline.kind;
    });
  }, [active, activeRulesets, draft, rulesets]);

  function invalidate() {
    setDry(null);
    setDryOk(false);
    setApplyResult(null);
  }

  function patchRule(key: string, changes: Partial<Rule>) {
    setDraft((current) => current.map((rule) => rule._key === key ? { ...rule, ...changes } : rule));
    invalidate();
  }

  function addRule() {
    const priority = draft.length ? Math.max(...draft.map((rule) => rule.priority)) + 10 : 10;
    const key = makeKey();
    setDraft((current) => [...current, {
      _key: key,
      id: `rule-${current.length + 1}`,
      kind: 'DOMAIN-SUFFIX',
      pattern: '',
      action: actionOptions[0] || 'direct',
      priority,
      enabled: true,
    }]);
    setEditing(key);
    invalidate();
  }

  function removeRule(key: string) {
    setDraft((current) => current.filter((rule) => rule._key !== key));
    if (editing === key) setEditing(null);
    invalidate();
  }

  function moveRule(key: string, direction: -1 | 1) {
    setDraft((current) => {
      const index = current.findIndex((rule) => rule._key === key);
      const target = index + direction;
      if (index < 0 || target < 0 || target >= current.length) return current;
      const reordered = current.slice();
      [reordered[index], reordered[target]] = [reordered[target], reordered[index]];
      return reordered.map((rule, position) => ({ ...rule, priority: (position + 1) * 10 }));
    });
    invalidate();
  }

  function addFixture() {
    setFixtures((current) => [...current, {
      _key: makeKey(), domain: '', expected_exit: actionOptions[0] || 'direct',
    }]);
    invalidate();
  }

  function patchFixture(key: string, changes: Partial<FixtureDraft>) {
    setFixtures((current) => current.map((fixture) => fixture._key === key ? { ...fixture, ...changes } : fixture));
    invalidate();
  }

  async function runDryRun() {
    setError(null);
    setBusy('dryrun');
    setDry(null);
    try {
      const result = await api.post<DryRunResponse>('/api/v1/rules/dry-run', {
        rules: stripDrafts(draft),
        fixtures: fixtures
          .filter((fixture) => fixture.domain.trim())
          .map(({ _key, ...fixture }) => fixture),
      });
      setDry(result);
      setDryOk(result.failed === 0);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy('idle');
    }
  }

  async function runApply() {
    setError(null);
    setBusy('apply');
    setApplyResult(null);
    try {
      const response = await api.post<ApplyResponse | PendingApplyResponse>('/api/v1/rules/apply', {
        rules: stripDrafts(draft),
        note: note || 'panel edit',
      });
      const result = await settleRuleApply(response);
      setNote('');
      await refresh();
      setApplyResult(result);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy('idle');
    }
  }

  async function syncRuleset(name: string) {
    setSyncingRuleset(name);
    try {
      const updated = await api.post<RulesetView>(`/api/v1/rulesets/${encodeURIComponent(name)}/sync`);
      setRulesets((current) => current.map((ruleset) => ruleset.name === name ? updated : ruleset));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSyncingRuleset(null);
    }
  }

  async function toggleRuleset(name: string, enabled: boolean) {
    try {
      const updated = await api.post<RulesetView>(`/api/v1/rulesets/${encodeURIComponent(name)}/enabled`, { enabled });
      setRulesets((current) => current.map((ruleset) => ruleset.name === name ? updated : ruleset));
      invalidate();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  async function removeRuleset(name: string) {
    try {
      await api.del(`/api/v1/rulesets/${encodeURIComponent(name)}`);
      setRulesets((current) => current.filter((ruleset) => ruleset.name !== name));
      invalidate();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  async function previewImport() {
    setImportBusy(true);
    setImportError(null);
    setImportPreview(null);
    try {
      const body: Record<string, unknown> = { action: importAction };
      if (importMode === 'url') body.url = importUrl.trim();
      else body.text = importText;
      setImportPreview(await api.post<ImportRulesResponse>('/api/v1/rules/import', body));
    } catch (caught) {
      setImportError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setImportBusy(false);
    }
  }

  async function registerRuleset() {
    setImportBusy(true);
    setImportError(null);
    try {
      const body: RegisterRulesetRequest = { source_url: importUrl.trim(), action: importAction };
      if (importRulesetName.trim()) body.name = importRulesetName.trim();
      const created = await api.post<RulesetView>('/api/v1/rulesets', body);
      setRulesets((current) => [...current.filter((ruleset) => ruleset.name !== created.name), created]);
      invalidate();
      closeImport();
    } catch (caught) {
      setImportError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setImportBusy(false);
    }
  }

  function mergeImport() {
    if (!importPreview) return;
    setDraft((current) => mergeImportedRules(current, importPreview));
    invalidate();
    closeImport();
  }

  function closeImport() {
    setImportOpen(false);
    setImportPreview(null);
    setImportError(null);
    setImportUrl('');
    setImportText('');
    setImportRulesetName('');
  }

  function changeImportMode(mode: 'url' | 'text') {
    setImportMode(mode);
    setImportPreview(null);
    setImportError(null);
  }

  return (
    <AppShell>
      <RulesHeader
        busy={busy} isDirty={isDirty} dryOk={dryOk} dry={dry} applyResult={applyResult}
        error={error} fixtures={fixtures} onRefresh={() => void refresh()} onAdd={addRule}
        onImport={() => setImportOpen(true)} onDryRun={() => void runDryRun()}
        onApply={() => void runApply()} onDismissError={() => setError(null)}
        onDismissResult={() => setApplyResult(null)}
      />
      <RulesEditor
        draft={draft} rulesets={rulesets} actionOptions={actionOptions} exits={exits}
        editing={editing} syncingRuleset={syncingRuleset} onEdit={setEditing}
        onPatch={patchRule} onMove={moveRule} onRemove={removeRule}
        onSyncRuleset={(name) => void syncRuleset(name)}
        onToggleRuleset={(name, enabled) => void toggleRuleset(name, enabled)}
        onRemoveRuleset={(name) => void removeRuleset(name)}
      />
      <RulesFixtures
        fixtures={fixtures} actionOptions={actionOptions} note={note} dry={dry}
        onAdd={addFixture} onPatch={patchFixture}
        onRemove={(key) => { setFixtures((current) => current.filter((fixture) => fixture._key !== key)); invalidate(); }}
        onNoteChange={setNote}
      />
      <RulesImportDialog
        open={importOpen} mode={importMode} url={importUrl} text={importText}
        action={importAction} name={importRulesetName} actionOptions={actionOptions}
        busy={importBusy} preview={importPreview} error={importError}
        onOpenChange={setImportOpen} onModeChange={changeImportMode}
        onUrlChange={setImportUrl} onTextChange={setImportText}
        onActionChange={setImportAction} onNameChange={setImportRulesetName}
        onPreview={() => void previewImport()} onBack={() => setImportPreview(null)}
        onRegister={() => void registerRuleset()} onMerge={mergeImport} onCancel={closeImport}
      />
    </AppShell>
  );
}
