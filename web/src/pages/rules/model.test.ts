import { describe, expect, it } from 'vitest';
import { mergeImportedRules, type Draft } from './model';

describe('mergeImportedRules', () => {
  it('turns pasted imports into visible manual rules without group metadata', () => {
    const existing: Draft[] = [{
      _key: 'existing', id: 'imp-1', kind: 'DOMAIN', pattern: 'old.test',
      action: 'direct', priority: 10, enabled: true,
    }];
    const merged = mergeImportedRules(existing, {
      rules: [{
        id: 'imp-1', kind: 'DOMAIN-SUFFIX', pattern: 'example.com', action: 'direct',
        priority: 200, enabled: true, group_id: 'imp-text-hidden',
      }],
      converted: 1,
      dropped: 0,
      categories: [],
      source_kind: 'text',
      group_id: 'imp-text-hidden',
    });
    expect(merged[1]).toMatchObject({ id: 'imp-1-2', priority: 20, pattern: 'example.com' });
    expect(merged[1]).not.toHaveProperty('group_id');
  });
});
