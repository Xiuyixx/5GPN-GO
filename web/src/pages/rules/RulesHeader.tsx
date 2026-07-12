import { Trans, useTranslation } from 'react-i18next';
import { Button } from '../../components/ui/button';
import { Heading } from '../../components/ui/heading';
import { Text } from '../../components/ui/text';
import type { ApplyResponse, DryRunResponse } from '../../api/client';
import type { FixtureDraft } from './model';

interface Props {
  busy: 'idle' | 'dryrun' | 'apply';
  isDirty: boolean;
  dryOk: boolean;
  dry: DryRunResponse | null;
  applyResult: ApplyResponse | null;
  error: string | null;
  fixtures: FixtureDraft[];
  onRefresh: () => void;
  onAdd: () => void;
  onImport: () => void;
  onDryRun: () => void;
  onApply: () => void;
  onDismissError: () => void;
  onDismissResult: () => void;
}

export function RulesHeader(props: Props) {
  const { t } = useTranslation();
  const failed = props.dry?.results.filter((result) => !result.pass) ?? [];
  return (
    <>
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div><Heading>{t('nav.rules')}</Heading><Text className="mt-1">{t('rules.tagline')}</Text></div>
        <div className="flex flex-wrap items-center gap-2">
          <Button plain onClick={props.onRefresh}>{t('rules.reset')}</Button>
          <Button plain onClick={props.onAdd}>{t('rules.addRule')}</Button>
          <Button plain onClick={props.onImport}>{t('rules.importRuleset')}</Button>
          <Button color="zinc" onClick={props.onDryRun} disabled={props.busy !== 'idle'}>{props.busy === 'dryrun' ? t('rules.running') : t('rules.dryRun')}</Button>
          <Button color="indigo" onClick={props.onApply} disabled={props.busy !== 'idle' || !props.isDirty || !props.dryOk}>{props.busy === 'apply' ? t('rules.applying') : t('rules.apply')}</Button>
        </div>
      </div>
      {props.error && (
        <div className="mb-4 flex items-start justify-between gap-3 rounded-xl border border-red-400/40 bg-red-500/10 px-4 py-3 text-sm text-red-800 dark:text-red-200">
          <div><div className="font-semibold">{t('rules.errorTitle')}</div><div className="mt-0.5">{props.error}</div></div>
          <button type="button" onClick={props.onDismissError} aria-label={t('rules.dismissError')} className="shrink-0 rounded px-2 py-0.5 text-red-700/70 hover:text-red-900 dark:text-red-300/70 dark:hover:text-red-100">✕</button>
        </div>
      )}
      {props.applyResult && (
        <div className={`mb-4 flex items-start justify-between gap-3 rounded-xl border px-4 py-3 text-sm ${props.applyResult.rolled_back ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200' : 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200'}`}>
          <div>
            <div className="font-semibold">{props.applyResult.rolled_back ? t('rules.applyRolledBack') : t('rules.applySucceeded')}</div>
            <div className="mt-0.5">{t(props.applyResult.rolled_back ? 'rules.applyResultDetailRolledBack' : 'rules.applyResultDetail', { id: props.applyResult.snapshot_id, health: props.applyResult.health })}</div>
          </div>
          <button type="button" onClick={props.onDismissResult} aria-label={t('rules.dismissApplyResult')} className="shrink-0 rounded px-2 py-0.5 opacity-70 hover:opacity-100">✕</button>
        </div>
      )}
      {props.isDirty && !props.applyResult && (
        <div className={`mb-4 rounded-xl border px-4 py-3 text-sm ${props.dryOk ? 'border-emerald-400/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200' : props.dry ? 'border-red-400/40 bg-red-500/10 text-red-800 dark:text-red-200' : 'border-amber-400/40 bg-amber-500/10 text-amber-800 dark:text-amber-200'}`}>
          <div className="font-semibold">{t('rules.unsavedChanges')}</div>
          <div className="mt-0.5">
            {props.dryOk
              ? t('rules.dryRunPassed', { count: props.dry?.passed ?? 0 })
              : props.dry
                ? t('rules.dryRunFailed', { count: props.dry.results.length, failed: failed.length })
                : props.fixtures.some((fixture) => fixture.domain.trim()) ? t('rules.runDryRunHint') : t('rules.addFixtureHint')}
          </div>
          {props.dry && !props.dryOk && failed.length > 0 && (
            <ul className="mt-2 space-y-1 text-xs">
              {failed.slice(0, 5).map((result) => (
                <li key={result.domain}>
                  <code className="rounded bg-red-500/10 px-1 py-0.5">{result.domain}</code>{' '}
                  <Trans i18nKey="rules.fixtureFailedLine" values={{ expected: result.expected_exit, actual: result.actual_exit || '—' }} components={{ strong: <strong /> }} />
                  {' '}{result.matched_rule ? t('rules.matchedRuleParen', { rule: result.matched_rule }) : t('rules.noRuleMatchedParen')}
                </li>
              ))}
              {failed.length > 5 && <li className="text-red-700/70 dark:text-red-300/70">{t('rules.andMoreSeeTable', { count: failed.length - 5 })}</li>}
            </ul>
          )}
        </div>
      )}
    </>
  );
}
