import type { ImportRulesResponse, Rule, RuleKind } from '../../api/client';

export const RULE_KINDS: RuleKind[] = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD',
  'GEOSITE', 'GEOIP', 'IP-CIDR', 'RULE-SET', 'MATCH',
];

export const SPECIAL_ACTIONS = ['direct', 'block'];

export interface Draft extends Rule {
  _key: string;
}

export interface FixtureDraft {
  _key: string;
  domain: string;
  expected_exit: string;
}

let keyCounter = 0;

export function makeKey(): string {
  keyCounter += 1;
  return `k${Date.now().toString(36)}-${keyCounter}`;
}

export function toDraft(rule: Rule): Draft {
  return { ...rule, _key: makeKey() };
}

export function stripDrafts(list: Draft[]): Rule[] {
  return list.map(({ _key, ...rule }) => rule);
}

export function mergeImportedRules(draft: Draft[], imported: ImportRulesResponse): Draft[] {
  const existingIds = new Set(draft.map((rule) => rule.id));
  let priority = draft.length ? Math.max(...draft.map((rule) => rule.priority)) : 0;
  const additions = imported.rules.map((rule) => {
    const manualRule = { ...rule };
    delete manualRule.group_id;
    let id = rule.id;
    let suffix = 2;
    while (existingIds.has(id)) id = `${rule.id}-${suffix++}`;
    existingIds.add(id);
    priority += 10;
    return { ...manualRule, id, priority, _key: makeKey() };
  });
  return [...draft, ...additions];
}
