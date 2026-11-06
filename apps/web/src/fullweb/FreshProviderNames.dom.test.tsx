// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { FreshProviderNames } from './FreshProviderNames';
import type { PublicApiClient } from './publicApi';

const translate = (key: string) => key;
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }));
let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});
function button(label: string) {
  return [...host.querySelectorAll('button')].find(
    (node) => node.textContent === `provider_names.${label}`
  )!;
}
it('DOM Cancel discards an edited draft without submitting; Save really submits', async () => {
  const snapshot = {
    generation: '7',
    etag: '"v1"',
    writable: true,
    groups: [{ provider: 'codex', index: 0, name: 'old@example.com' }],
  };
  const api = {
    get: vi.fn().mockResolvedValue(snapshot),
    put: vi.fn().mockResolvedValue({ saved: true }),
    post: vi.fn(),
  };
  await act(async () =>
    root.render(
      <FreshProviderNames
        client={api as PublicApiClient}
        credential="synthetic-admin"
        onUnauthorized={vi.fn()}
      />
    )
  );
  await act(async () => button('edit').click());
  const input = host.querySelector('input')!;
  await act(async () => {
    // Use the native value setter so React sees an actual DOM input change.
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(
      input,
      'draft@example.com'
    );
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  expect(button('save').disabled).toBe(false);
  const submit = vi.fn();
  host.querySelector('form')!.addEventListener('submit', submit);
  expect(button('cancel').type).toBe('button');
  await act(async () => button('cancel').click());
  expect(submit).not.toHaveBeenCalled();
  expect(api.put).not.toHaveBeenCalled();
  expect(host.querySelector('input')).toBeNull();
  // Positive control: the same DOM submits through Save, so this test detects
  // native form behavior instead of calling React handler props directly.
  await act(async () => button('edit').click());
  const nextInput = host.querySelector('input')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(
      nextInput,
      'saved@example.com'
    );
    nextInput.dispatchEvent(new Event('input', { bubbles: true }));
  });
  api.get.mockResolvedValue({
    ...snapshot,
    etag: '"v2"',
    groups: [{ ...snapshot.groups[0], name: 'saved@example.com' }],
  });
  host.querySelector('form')!.addEventListener('submit', submit);
  await act(async () => button('save').click());
  expect(submit).toHaveBeenCalledTimes(1);
  expect(api.put).toHaveBeenCalledTimes(1);
  expect(host.querySelector('input')).toBeNull();
});
