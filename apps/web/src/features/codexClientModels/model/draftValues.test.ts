import { describe, expect, it } from 'vitest';
import {
  formatArrayText,
  formatPlainText,
  parseJsonArrayText,
  parseNumberText,
  parseStringListText,
} from './draftValues';

describe('draftValues', () => {
  it('keeps real newlines when formatting multi-line text for an editor', () => {
    const text = formatPlainText('line one\nline two');
    expect(text).toContain('\n');
    expect(text).not.toContain('\\n');
  });

  it('formats string arrays one item per line and other arrays as JSON', () => {
    expect(formatArrayText(['plus', 'pro'])).toBe('plus\npro');
    expect(formatArrayText([{ effort: 'low' }])).toContain('"effort": "low"');
  });

  it('parses a string list per line and drops blank lines', () => {
    expect(parseStringListText('text\n\n image \n')).toEqual({
      ok: true,
      value: ['text', 'image'],
    });
  });

  it('reports a number field that is empty or not a number', () => {
    expect(parseNumberText('  ')).toEqual({ ok: false, error: { reason: 'empty' } });
    expect(parseNumberText('abc')).toEqual({ ok: false, error: { reason: 'number' } });
    expect(parseNumberText(' 272000 ')).toEqual({ ok: true, value: 272000 });
  });

  it('rejects JSON that is not an array', () => {
    expect(parseJsonArrayText('{"a":1}')).toEqual({ ok: false, error: { reason: 'array' } });
    expect(parseJsonArrayText('[1, 2]')).toEqual({ ok: true, value: [1, 2] });
    expect(parseJsonArrayText('[').ok).toBe(false);
  });
});
