import cases from '../../../../../tests/fixtures/xai-billing-zero.json';
import { describe, expect, it } from 'vitest';
import { isXaiValidatedZero } from './xaiBillingZero';
describe('validated xAI implicit zero', () => {
  it.each(cases)('$name', ({ text, start, end, now, want }) => {
    expect(isXaiValidatedZero(text, start, end, Date.parse(now))).toBe(want);
  });
});
