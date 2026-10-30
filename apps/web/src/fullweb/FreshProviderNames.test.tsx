import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '../components/ui/Button';
import { FreshProviderNames } from './FreshProviderNames';
import { parseProviderNames } from './providerNamesClient';
import type { PublicApiClient } from './publicApi';

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }));
const translate = (key: string) => key;
const snapshot = {
  generation: '7',
  etag: '"v1"',
  writable: true,
  groups: [{ provider: 'codex', index: 0, name: 'old@example.com' }],
};
let renderer: ReactTestRenderer;
const unauthorized = vi.fn();
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(async () => {
  if (renderer) await act(async () => renderer.unmount());
});
function button(label: string) {
  return renderer.root
    .findAllByType(Button)
    .find((b) => b.props.children === `provider_names.${label}`)!;
}
function client() {
  return {
    get: vi.fn().mockResolvedValue(snapshot),
    put: vi.fn().mockResolvedValue({ saved: true }),
    post: vi.fn(),
  };
}
async function mount(api: ReturnType<typeof client>) {
  await act(async () => {
    renderer = create(
      <FreshProviderNames
        client={api as PublicApiClient}
        credential="admin-example"
        onUnauthorized={unauthorized}
      />
    );
  });
}
async function edit(name: string) {
  await act(async () => button('edit').props.onClick());
  await act(async () =>
    renderer.root.findByType('input').props.onChange({ target: { value: name } })
  );
}
async function save() {
  await act(async () =>
    renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() })
  );
}
describe('native CPA provider group names', () => {
  it('writes only name metadata and confirms persisted value by readback', async () => {
    const api = client();
    await mount(api);
    await edit('new@example.com');
    api.get.mockResolvedValue({
      ...snapshot,
      etag: '"v2"',
      groups: [{ ...snapshot.groups[0], name: 'new@example.com' }],
    });
    await save();
    expect(api.put).toHaveBeenCalledExactlyOnceWith(
      'providers/names',
      {
        generation: '7',
        etag: '"v1"',
        provider: 'codex',
        index: 0,
        name: 'new@example.com',
      },
      { kind: 'admin', value: 'admin-example' }
    );
    expect(renderer.root.findAllByType('input')).toHaveLength(0);
    expect(JSON.stringify(renderer.toJSON())).toContain('new@example.com');
  });
  it('keeps draft after an unknown write and blocks replay until explicit refresh', async () => {
    const api = client();
    await mount(api);
    await edit('draft@example.com');
    api.put.mockRejectedValue(new Error('lost response'));
    await save();
    expect(renderer.root.findByType('input').props.value).toBe('draft@example.com');
    expect(button('save').props.disabled).toBe(true);
    await save();
    expect(api.put).toHaveBeenCalledTimes(1);
    await act(async () => button('refresh').props.onClick());
    expect(renderer.root.findAllByType('input')).toHaveLength(0);
    expect(button('edit').props.disabled).toBe(false);
  });
  it('ignores a delayed read from a previous authenticated scope', async () => {
    const api = client();
    let resolve!: (v: unknown) => void;
    api.get.mockReturnValueOnce(
      new Promise((r) => {
        resolve = r;
      })
    );
    await mount(api);
    const next = client();
    next.get.mockResolvedValue({ ...snapshot, groups: [] });
    await act(async () =>
      renderer.update(
        <FreshProviderNames
          client={next as PublicApiClient}
          credential="other-admin"
          onUnauthorized={unauthorized}
        />
      )
    );
    await act(async () => resolve(snapshot));
    expect(JSON.stringify(renderer.toJSON())).not.toContain('old@example.com');
  });
  it('does not accept a malformed projection', () => {
    expect(() =>
      parseProviderNames({ ...snapshot, groups: [{ ...snapshot.groups[0], index: -1 }] })
    ).toThrow();
    expect(() => parseProviderNames({ ...snapshot, groups: null })).toThrow();
  });
});
