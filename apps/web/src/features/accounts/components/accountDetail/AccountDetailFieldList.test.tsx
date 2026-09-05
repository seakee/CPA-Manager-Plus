import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { AccountDetailFieldValue } from './AccountDetailFieldList';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en-US' },
    }),
  };
});

const renderNumber = (value: number): string =>
  renderToStaticMarkup(
    <AccountDetailFieldValue
      field={{
        key: 'test-number',
        labelKey: 'accounts.test_number',
        value,
        valueKind: 'number',
      }}
    />
  );

describe('AccountDetailFieldValue', () => {
  it.each([
    [12.5, '12.5'],
    [999.99, '999.99'],
    [1_000, '1.0K'],
    [12_500, '12.5K'],
    [1_000_190_000, '1.0B'],
  ])('preserves generic number presentation for %s', (value, expected) => {
    expect(renderNumber(value)).toBe(expected);
  });
});
