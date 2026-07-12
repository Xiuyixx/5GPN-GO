import { Trans, useTranslation } from 'react-i18next';
import { Button } from '../../components/ui/button';
import { Dialog, DialogActions, DialogBody, DialogDescription, DialogTitle } from '../../components/ui/dialog';
import { Field, FieldGroup, Fieldset, Label } from '../../components/ui/fieldset';
import { Input } from '../../components/ui/input';
import { Select } from '../../components/ui/select';
import { Textarea } from '../../components/ui/textarea';
import type { ImportRulesResponse } from '../../api/client';

interface Props {
  open: boolean;
  mode: 'url' | 'text';
  url: string;
  text: string;
  action: string;
  name: string;
  actionOptions: string[];
  busy: boolean;
  preview: ImportRulesResponse | null;
  error: string | null;
  onOpenChange: (open: boolean) => void;
  onModeChange: (mode: 'url' | 'text') => void;
  onUrlChange: (value: string) => void;
  onTextChange: (value: string) => void;
  onActionChange: (value: string) => void;
  onNameChange: (value: string) => void;
  onPreview: () => void;
  onBack: () => void;
  onRegister: () => void;
  onMerge: () => void;
  onCancel: () => void;
}

export function RulesImportDialog(props: Props) {
  const { t } = useTranslation();
  const canPreview = props.mode === 'url' ? Boolean(props.url.trim()) : Boolean(props.text.trim());
  return (
    <Dialog open={props.open} onClose={() => props.onOpenChange(false)} size="2xl">
      <DialogTitle>{t('rules.importRulesetTitle')}</DialogTitle>
      <DialogDescription><Trans i18nKey="rules.importDescription" components={{ code: <code /> }} /></DialogDescription>
      <DialogBody>
        <div className="mb-4 flex gap-2">
          <Button {...(props.mode === 'url' ? { color: 'zinc' as const } : { plain: true as const })} onClick={() => props.onModeChange('url')}>{t('rules.importUrlTab')}</Button>
          <Button {...(props.mode === 'text' ? { color: 'zinc' as const } : { plain: true as const })} onClick={() => props.onModeChange('text')}>{t('rules.importPasteTab')}</Button>
        </div>
        {props.mode === 'url' ? (
          <Fieldset><FieldGroup>
            <div className="mb-2 rounded-lg border border-indigo-500/30 bg-indigo-500/5 p-2 text-xs text-indigo-800 dark:text-indigo-200">{t('rules.importAsRulesetHelp')}</div>
            <Field><Label>{t('rules.importUrlLabel')}</Label><Input value={props.url} onChange={(event) => props.onUrlChange(event.target.value)} placeholder="https://example.com/proxy.txt" /><p className="mt-1 text-xs text-zinc-500">{t('rules.importUrlHelp')}</p></Field>
            <Field><Label>{t('rules.importRulesetNameLabel')}</Label><Input value={props.name} onChange={(event) => props.onNameChange(event.target.value)} placeholder="gfwlist" /><p className="mt-1 text-xs text-zinc-500">{t('rules.importRulesetNameHelp')}</p></Field>
          </FieldGroup></Fieldset>
        ) : (
          <Fieldset><FieldGroup><Field><Label>{t('rules.importPasteLabel')}</Label><Textarea rows={10} value={props.text} onChange={(event) => props.onTextChange(event.target.value)} placeholder="DOMAIN-SUFFIX,google.com,Proxy&#10;DOMAIN-KEYWORD,openai,Proxy&#10;IP-CIDR,10.0.0.0/8,DIRECT&#10;FINAL,Proxy" /></Field></FieldGroup></Fieldset>
        )}
        <div className="mt-4"><Fieldset><FieldGroup><Field><Label>{t('rules.rewritePoliciesLabel')} <span className="text-xs text-zinc-500">{t('rules.rewritePoliciesHint')}</span></Label><Select value={props.action} onChange={(event) => props.onActionChange(event.target.value)}>{!props.actionOptions.includes(props.action) && <option value={props.action}>{t('rules.unknownDryRunWillFlag', { action: props.action })}</option>}{props.actionOptions.map((action) => <option key={action} value={action}>{action}</option>)}</Select></Field></FieldGroup></Fieldset></div>
        {props.error && <div className="mt-4 rounded-lg border border-red-400/40 bg-red-500/10 px-3 py-2 text-sm text-red-800 dark:text-red-200">{props.error}</div>}
        {props.preview && <div className="mt-4 rounded-xl border border-emerald-400/40 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-800 dark:text-emerald-200"><div className="font-semibold">{t('rules.previewLabel')}</div><div className="mt-1">{t('rules.previewRulesReady', { count: props.preview.rules.length })} · {t('rules.previewDropped', { count: props.preview.dropped })}{props.preview.categories.length > 0 && <> · {t('rules.previewCategories', { list: props.preview.categories.slice(0, 5).join(', ') + (props.preview.categories.length > 5 ? '…' : '') })}</>}</div>{props.preview.source_url && <div className="mt-1 text-xs opacity-75">{t('rules.previewSource', { url: props.preview.source_url })}</div>}</div>}
      </DialogBody>
      <DialogActions>
        <Button plain onClick={props.onCancel}>{t('common.cancel')}</Button>
        {!props.preview && <Button color="zinc" onClick={props.onPreview} disabled={props.busy || !canPreview}>{props.busy ? t('rules.fetching') : t('rules.preview')}</Button>}
        {props.preview && <><Button plain onClick={props.onBack}>{t('rules.back')}</Button>{props.mode === 'url' ? <Button color="indigo" onClick={props.onRegister} disabled={props.preview.rules.length === 0 || props.busy}>{props.busy ? t('rules.rulesetRegisterInProgress') : t('rules.rulesetRegister')}</Button> : <Button color="indigo" onClick={props.onMerge} disabled={props.preview.rules.length === 0}>{t('rules.mergeRules', { count: props.preview.rules.length })}</Button>}</>}
      </DialogActions>
    </Dialog>
  );
}
