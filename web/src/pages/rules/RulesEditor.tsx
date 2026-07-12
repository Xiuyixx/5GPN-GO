import { Trans, useTranslation } from 'react-i18next';
import { Badge } from '../../components/ui/badge';
import { Button } from '../../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../../components/ui/fieldset';
import { Heading } from '../../components/ui/heading';
import { Input } from '../../components/ui/input';
import { Select } from '../../components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../../components/ui/table';
import { Text } from '../../components/ui/text';
import type { Rule, RuleKind, RulesetView } from '../../api/client';
import { RULE_KINDS, type Draft } from './model';

interface Props {
  draft: Draft[];
  rulesets: RulesetView[];
  actionOptions: string[];
  exits: string[];
  editing: string | null;
  syncingRuleset: string | null;
  onEdit: (key: string | null) => void;
  onPatch: (key: string, changes: Partial<Rule>) => void;
  onMove: (key: string, direction: -1 | 1) => void;
  onRemove: (key: string) => void;
  onSyncRuleset: (name: string) => void;
  onToggleRuleset: (name: string, enabled: boolean) => void;
  onRemoveRuleset: (name: string) => void;
}

export function RulesEditor(props: Props) {
  const { t } = useTranslation();
  const managedGroups = new Set(props.rulesets.map((ruleset) => ruleset.name));
  const manual = props.draft.map((rule, index) => ({ rule, index })).filter(
    ({ rule }) => !rule.group_id || !managedGroups.has(rule.group_id),
  );
  const selected = props.draft.find((rule) => rule._key === props.editing);

  return (
    <>
      <div className="glass mb-4 p-4">
        <div className="mb-3"><Heading level={2}>{t('rules.sectionRulesets')}</Heading><Text className="mt-1 text-xs">{t('rules.sectionRulesetsHelp')}</Text></div>
        {props.rulesets.length === 0 ? <Text className="text-xs text-zinc-500">{t('rules.rulesetsEmpty')}</Text> : (
          <div className="space-y-3">
            {props.rulesets.map((ruleset) => (
              <div key={ruleset.name} className={`rounded-xl border p-3 ${ruleset.enabled ? 'border-indigo-500/30 bg-indigo-500/5 dark:bg-indigo-500/10' : 'border-zinc-400/30 bg-zinc-100/40 dark:bg-zinc-800/40'}`}>
                <div className="flex flex-wrap items-center gap-3">
                  <span className="font-mono text-sm text-indigo-700 dark:text-indigo-300">{ruleset.name}</span>
                  <Badge color={ruleset.enabled ? 'indigo' : 'zinc'}>{t('rules.groupHeaderCount', { count: ruleset.rule_count })}</Badge>
                  <span className="text-xs text-zinc-500">{t('rules.rulesetActionLabel')}: <code className="text-xs">{ruleset.action}</code></span>
                  <div className="ml-auto flex flex-wrap gap-1">
                    <Button plain onClick={() => props.onSyncRuleset(ruleset.name)} disabled={props.syncingRuleset === ruleset.name}>{props.syncingRuleset === ruleset.name ? t('rules.rulesetSyncing') : t('rules.rulesetSyncNow')}</Button>
                    <Button plain onClick={() => props.onToggleRuleset(ruleset.name, !ruleset.enabled)}>{ruleset.enabled ? t('rules.groupDisableAll') : t('rules.groupEnableAll')}</Button>
                    <Button plain onClick={() => { if (window.confirm(t('rules.rulesetDeleteConfirm', { name: ruleset.name }))) props.onRemoveRuleset(ruleset.name); }}>{t('rules.groupDeleteAll')}</Button>
                  </div>
                </div>
                <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs">
                  <dt className="text-zinc-500">{t('rules.rulesetSourceLabel')}</dt><dd className="truncate font-mono text-[11px]">{ruleset.source_url}</dd>
                  <dt className="text-zinc-500">{t('rules.rulesetLastSyncedLabel')}</dt><dd>{ruleset.last_synced_at ? new Date(ruleset.last_synced_at * 1000).toLocaleString() : t('rules.rulesetNever')}</dd>
                  {ruleset.last_error && <><dt className="text-red-600 dark:text-red-400">{t('rules.errorTitle')}</dt><dd className="text-red-600 dark:text-red-400">{t('rules.rulesetSyncError', { err: ruleset.last_error })}</dd></>}
                </dl>
              </div>
            ))}
          </div>
        )}
      </div>
      <div className="glass p-2">
        <div className="mb-2 px-4 pt-3"><Heading level={2}>{t('rules.sectionRules')}</Heading></div>
        {manual.length === 0 ? <div className="p-6 text-center"><Text><Trans i18nKey="rules.noRulesYet" components={{ strong: <strong /> }} /></Text></div> : (
          <Table>
            <TableHead><TableRow><TableHeader className="w-16">{t('rules.thPriority')}</TableHeader><TableHeader>{t('rules.thId')}</TableHeader><TableHeader>{t('rules.thKind')}</TableHeader><TableHeader>{t('rules.thPattern')}</TableHeader><TableHeader>{t('rules.thAction')}</TableHeader><TableHeader className="w-24">{t('rules.thStatus')}</TableHeader><TableHeader className="w-40 text-right">{t('rules.thActions')}</TableHeader></TableRow></TableHead>
            <TableBody>
              {manual.map(({ rule, index }) => (
                <TableRow key={rule._key} className="align-middle">
                  <TableCell className="metric text-zinc-500">{rule.priority}</TableCell>
                  <TableCell className="font-medium">{rule.id}</TableCell>
                  <TableCell><Badge color="zinc">{rule.kind}</Badge></TableCell>
                  <TableCell className="text-zinc-600 dark:text-zinc-300">{rule.pattern ? <code className="text-xs">{rule.pattern}</code> : <span className="text-zinc-400">—</span>}</TableCell>
                  <TableCell>{props.actionOptions.includes(rule.action) ? <code className="text-xs">{rule.action}</code> : <span className="text-xs text-red-600 dark:text-red-400">{t('rules.unknownAction', { action: rule.action })}</span>}</TableCell>
                  <TableCell><button type="button" onClick={() => props.onPatch(rule._key, { enabled: !rule.enabled })} className="cursor-pointer" aria-label={rule.enabled ? t('rules.disableRule') : t('rules.enableRule')}><Badge color={rule.enabled ? 'lime' : 'zinc'}>{rule.enabled ? t('rules.statusOn') : t('rules.statusOff')}</Badge></button></TableCell>
                  <TableCell className="text-right"><div className="flex justify-end gap-1"><Button plain aria-label={t('rules.moveUp')} onClick={() => props.onMove(rule._key, -1)} disabled={index === 0}>↑</Button><Button plain aria-label={t('rules.moveDown')} onClick={() => props.onMove(rule._key, 1)} disabled={index === props.draft.length - 1}>↓</Button><Button plain onClick={() => props.onEdit(props.editing === rule._key ? null : rule._key)}>{props.editing === rule._key ? t('common.close') : t('rules.editButton')}</Button><Button plain aria-label={t('rules.deleteAria')} onClick={() => props.onRemove(rule._key)}>✕</Button></div></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
      {selected && (
        <div className="glass fade-up mt-4 p-6">
          <div className="mb-4 flex items-center justify-between"><Heading level={2}>{t('rules.editRule')}</Heading><Button plain onClick={() => props.onEdit(null)}>{t('common.close')}</Button></div>
          <Fieldset><Legend className="sr-only">{t('rules.fieldsLegend')}</Legend><FieldGroup><div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field><Label>{t('rules.idLabel')}</Label><Input value={selected.id} onChange={(event) => props.onPatch(selected._key, { id: event.target.value })} /></Field>
            <Field><Label>{t('rules.priorityLabel')}</Label><Input type="number" value={selected.priority} onChange={(event) => props.onPatch(selected._key, { priority: Number(event.target.value) })} /></Field>
            <Field><Label>{t('rules.kindLabel')}</Label><Select value={selected.kind} onChange={(event) => props.onPatch(selected._key, { kind: event.target.value as RuleKind })}>{RULE_KINDS.map((kind) => <option key={kind} value={kind}>{kind}</option>)}</Select></Field>
            <Field><Label>{t('rules.actionLabel')} <span className="text-xs text-zinc-500">{t('rules.actionHint')}</span></Label><Select value={selected.action} onChange={(event) => props.onPatch(selected._key, { action: event.target.value })}>{!props.actionOptions.includes(selected.action) && <option value={selected.action}>{t('rules.unknownWillFailDryRun', { action: selected.action })}</option>}{props.actionOptions.map((action) => <option key={action} value={action}>{action}</option>)}</Select>{props.exits.length === 0 && <p className="mt-1 text-xs text-amber-700 dark:text-amber-300"><Trans i18nKey="rules.noCustomExits" components={{ exitsLink: <a className="underline" href="/exits">{t('nav.exits')}</a> }} /></p>}</Field>
            <Field className="sm:col-span-2"><Label>{t('rules.patternLabel')} {selected.kind === 'MATCH' && <span className="text-zinc-500">{t('rules.patternDisabledForMatch')}</span>}</Label><Input value={selected.pattern} onChange={(event) => props.onPatch(selected._key, { pattern: event.target.value })} disabled={selected.kind === 'MATCH'} placeholder={selected.kind === 'DOMAIN-SUFFIX' ? 'example.com' : selected.kind === 'IP-CIDR' ? '10.0.0.0/8' : selected.kind === 'GEOSITE' ? 'cn' : t('rules.patternPlaceholder')} /></Field>
          </div></FieldGroup></Fieldset>
          <div className="mt-4 flex justify-between"><Button plain onClick={() => props.onRemove(selected._key)}>{t('rules.deleteRule')}</Button><Button color="zinc" onClick={() => props.onEdit(null)}>{t('rules.done')}</Button></div>
        </div>
      )}
    </>
  );
}
