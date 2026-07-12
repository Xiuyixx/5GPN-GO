export function parseAdminIds(raw: string): number[] {
  return raw
    .split(/[,\s]+/)
    .map((value) => Number(value.trim()))
    .filter((value) => Number.isSafeInteger(value) && value !== 0);
}

export function isTGBotStepValid(token: string, adminIdsText: string): boolean {
  if (!token.trim()) return true;
  return parseAdminIds(adminIdsText).length > 0;
}
