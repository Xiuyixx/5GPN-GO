import { useTranslation } from 'react-i18next';
import { Badge } from '../../components/ui/badge';
import { Button } from '../../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../../components/ui/fieldset';
import { Heading } from '../../components/ui/heading';
import { Input } from '../../components/ui/input';
import { Select } from '../../components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../../components/ui/table';
import { Text } from '../../components/ui/text';
import type { DryRunResponse } from '../../api/client';
import type { FixtureDraft } from './model';

interface Props {
  fixtures: FixtureDraft[];
  actionOptions: string[];
  note: string;
  dry: DryRunResponse | null;
  onAdd: () => void;
  onPatch: (key: string, changes: Partial<FixtureDraft>) => void;
  onRemove: (key: string) => void;
  onNoteChange: (note: string) => void;
}

export function RulesFixtures(props: Props) {
  const { t } = useTranslation();
  return (
    <>
      <div className="glass mt-6 p-6">
        <div className="mb-3 flex items-center justify-between">
          <div><Heading level={2}>{t('rules.dryRunFixturesHeading')}</Heading><Text className="mt-1 text-sm">{t('rules.dryRunFixturesHelp')}</Text></div>
          <Button plain onClick={props.onAdd}>{t('rules.addFixture')}</Button>
        </div>
        {props.fixtures.length === 0 ? <div className="rounded border border-zinc-200/50 bg-zinc-50/40 px-4 py-3 text-sm text-zinc-600 dark:border-zinc-700/50 dark:bg-zinc-800/40 dark:text-zinc-400">{t('rules.noFixtures')}</div> : (
          <Table>
            <TableHead><TableRow><TableHeader>{t('rules.thDomain')}</TableHeader><TableHeader className="w-56">{t('rules.thExpectedExit')}</TableHeader><TableHeader className="w-16 text-right">{t('rules.thRemove')}</TableHeader></TableRow></TableHead>
            <TableBody>{props.fixtures.map((fixture) => <TableRow key={fixture._key} className="align-middle"><TableCell><Input value={fixture.domain} onChange={(event) => props.onPatch(fixture._key, { domain: event.target.value })} placeholder={t('rules.domainPlaceholder')} /></TableCell><TableCell><Select value={fixture.expected_exit} onChange={(event) => props.onPatch(fixture._key, { expected_exit: event.target.value })}>{!props.actionOptions.includes(fixture.expected_exit) && <option value={fixture.expected_exit}>{t('rules.unknownAction', { action: fixture.expected_exit })}</option>}{props.actionOptions.map((action) => <option key={action} value={action}>{action}</option>)}</Select></TableCell><TableCell className="text-right"><Button plain aria-label={t('rules.removeFixture')} onClick={() => props.onRemove(fixture._key)}>✕</Button></TableCell></TableRow>)}</TableBody>
          </Table>
        )}
      </div>
      <form className="glass mt-6 p-6" onSubmit={(event) => event.preventDefault()}>
        <Fieldset><Legend>{t('rules.applyMetadata')}</Legend><FieldGroup><Field><Label>{t('rules.noteLabel')}</Label><Input value={props.note} onChange={(event) => props.onNoteChange(event.target.value)} placeholder={t('rules.notePlaceholder')} /></Field></FieldGroup></Fieldset>
      </form>
      {props.dry && (
        <div className="mt-8">
          <Heading level={2}>{t('rules.dryRunResults', { passed: props.dry.passed, failed: props.dry.failed })}</Heading>
          <div className="glass mt-4 p-2"><Table><TableHead><TableRow><TableHeader>{t('rules.thDomain')}</TableHeader><TableHeader>{t('rules.thMatchedRule')}</TableHeader><TableHeader>{t('rules.thKind')}</TableHeader><TableHeader>{t('rules.thActual')}</TableHeader><TableHeader>{t('rules.thExpected')}</TableHeader><TableHeader>{t('rules.thStatus')}</TableHeader></TableRow></TableHead><TableBody>{props.dry.results.map((result) => <TableRow key={result.domain}><TableCell className="font-medium">{result.domain}</TableCell><TableCell>{result.matched_rule || '—'}</TableCell><TableCell>{result.matched_kind || '—'}</TableCell><TableCell>{result.actual_exit || '—'}</TableCell><TableCell>{result.expected_exit}</TableCell><TableCell><Badge color={result.pass ? 'lime' : 'red'}>{result.pass ? t('rules.pass') : result.failure_reason ?? t('rules.fail')}</Badge></TableCell></TableRow>)}</TableBody></Table></div>
        </div>
      )}
    </>
  );
}
