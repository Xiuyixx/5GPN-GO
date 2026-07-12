import { describe, expect, it } from 'vitest';
import { isTGBotStepValid, parseAdminIds } from './validation';

describe('wizard Telegram validation', () => {
  it('requires an integer admin id whenever a token is supplied', () => {
    expect(isTGBotStepValid('secret', '')).toBe(false);
    expect(isTGBotStepValid('secret', '12.5 nope')).toBe(false);
    expect(isTGBotStepValid('secret', '-100123')).toBe(true);
  });

  it('allows skipping an untouched optional bot configuration', () => {
    expect(isTGBotStepValid('', '')).toBe(true);
    expect(parseAdminIds('1, -2 3')).toEqual([1, -2, 3]);
  });
});
