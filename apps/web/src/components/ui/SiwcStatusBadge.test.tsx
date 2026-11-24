import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const translations: Record<string, string> = {
  'siwc.active': 'Active',
  'siwc.invalid': 'Invalid',
  'siwc.bound': 'Bound · inactive',
  'siwc.unbound': 'Not bound',
};

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => translations[key] ?? key,
  }),
}));

import { SiwcStatusBadge } from './SiwcStatusBadge';

const readText = (node: unknown): string => {
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(readText).join('');
  if (node && typeof node === 'object' && 'children' in node) {
    return readText((node as { children?: unknown }).children);
  }
  return '';
};

describe('SiwcStatusBadge', () => {
  it.each([
    ['active', 'Active', 'SIWC · Active'],
    ['invalid', 'Invalid', 'SIWC · Invalid'],
    ['bound', 'Bound · inactive', 'SIWC · Bound · inactive'],
    ['unbound', 'Not bound', 'SIWC · Not bound'],
  ] as const)('renders %s state with optional prefix and title', (status, plain, prefixed) => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<SiwcStatusBadge status={status} title="Expires 2027-06-01" />);
    });
    const json = renderer.toJSON() as { props?: { title?: string }; children?: unknown };
    expect(json.props?.title).toBe('Expires 2027-06-01');
    expect(readText(json)).toBe(plain);

    act(() => {
      renderer.update(<SiwcStatusBadge status={status} showPrefix />);
    });
    expect(readText(renderer.toJSON())).toBe(prefixed);

    act(() => {
      renderer.unmount();
    });
  });
});
