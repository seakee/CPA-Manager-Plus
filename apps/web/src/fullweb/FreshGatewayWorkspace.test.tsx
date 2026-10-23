import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { FreshGatewayWorkspace } from './FreshGatewayWorkspace';
import { createPublicApiClient } from './publicApi';
import { configurationText as c } from '../i18n/configurationWorkspace';

// Component-state tests only; real portal, keyboard and focus behaviour is
// exercised in browser acceptance, without changing the shared Modal.
vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    children,
    footer,
    title,
  }: {
    open: boolean;
    children: ReactNode;
    footer: ReactNode;
    title: ReactNode;
  }) =>
    open ? (
      <div role="dialog">
        <h2>{title}</h2>
        {children}
        {footer}
      </div>
    ) : null,
}));
const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };
function RouteProbe() {
  return <span data-testid="workspace-route">{useLocation().pathname}</span>;
}
describe('FreshGatewayWorkspace component', () => {
  let renderer: ReactTestRenderer;
  let count: number, interval: number, revision: number;
  const onUnauthorized = vi.fn(),
    onPending = vi.fn(),
    onDirtyChange = vi.fn();
  const transport = vi.fn<typeof fetch>();
  const expected = () => ({
    sourceId: 'isolated-test-source',
    generation: '1',
    revision,
    sha256: 'a'.repeat(64),
  });
  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0xcc) });
    count = 2;
    interval = 30;
    revision = 2;
    onUnauthorized.mockClear();
    onPending.mockClear();
    onDirtyChange.mockClear();
    transport.mockReset();
    transport.mockImplementation(async (url, init) => {
      const isInterval = String(url).includes('max-retry-interval');
      if (init?.method === 'PUT') {
        const body = JSON.parse(String(init.body));
        if (isInterval) interval = body.value;
        else count = body.value;
        revision++;
        return Response.json({
          operation: {
            ...expected(),
            requestId: body.requestId,
            value: body.value,
            state: 'persisted',
            ...(isInterval ? { field: 'max-retry-interval' } : {}),
          },
          effective: 'unknown',
        });
      }
      return Response.json({
        field: isInterval ? 'routing.retry.max-retry-interval' : 'routing.retry.request-retry',
        presence: 'present',
        value: isInterval ? interval : count,
        source: 'isolated-test',
        authority: 'host-runtime',
        editable: true,
        reason: '',
        persisted: 'persisted',
        effective: 'unknown',
        concurrentProtection: 'host-exclusive-runtime-reservation',
        expected: expected(),
      });
    });
  });
  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    await i18n.changeLanguage('zh-CN');
    vi.unstubAllGlobals();
  });
  async function mount(basePath = '') {
    await act(async () => {
      renderer = create(
        <MemoryRouter basename={basePath || '/'} initialEntries={[basePath + '/config']}>
          <RouteProbe />
          <FreshGatewayWorkspace
            client={createPublicApiClient(() => context, transport)}
            credential="test-credential"
            onUnauthorized={onUnauthorized}
            onPending={onPending}
            onDirtyChange={onDirtyChange}
          />
        </MemoryRouter>
      );
    });
  }
  const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
  const has = (id: string) => renderer.root.findAllByProps({ 'data-testid': id }).length > 0;
  const click = async (id: string) => {
    await act(async () => node(id).props.onClick());
  };
  const change = async (id: string, value: string) => {
    await act(async () => node(id).props.onChange({ target: { value } }));
  };
  const changedOnly = async () => {
    await act(async () =>
      node('gateway-changed-only').props.onChange({ target: { checked: true } })
    );
  };
  const discard = async () => {
    await click('discard-drafts-btn');
    await click('confirm-discard-btn');
  };

  it('renders the integrated overview and opens ownership details through Modal', async () => {
    await mount();
    expect(has('gateway-overview-content')).toBe(true);
    expect(node('gateway-search-input').props.value).toBe('');
    await click('view-owner-0');
    expect(has('owner-detail-modal')).toBe(true);
    await click('close-owner-btn');
    expect(has('owner-detail-modal')).toBe(false);
    await click('gateway-tab-routing');
    expect(has('section-routing')).toBe(true);
    await click('gateway-tab-retry');
    expect(node('retry-editor').props.hidden).toBe(false);
    expect(node('retry-interval-editor').props.hidden).toBe(false);
    expect(node('retry-editor').props.className).toContain('retry-editor-compact');
    expect(has('gateway-retry-group')).toBe(true);
  });
  it('supports arrow, Home and End tab navigation with one tab stop and the same retained draft', async () => {
    await mount();
    await change('retry-value', '4');
    const key = async (tab: string, value: string) => {
      const preventDefault = vi.fn();
      await act(async () =>
        node('gateway-tab-' + tab).props.onKeyDown({ key: value, preventDefault })
      );
      expect(preventDefault).toHaveBeenCalledOnce();
    };
    expect(node('gateway-tab-overview').props.tabIndex).toBe(0);
    expect(node('gateway-tab-common').props.tabIndex).toBe(-1);
    await key('overview', 'ArrowRight');
    expect(node('gateway-tab-common').props['aria-selected']).toBe(true);
    expect(node('gateway-tab-common').props.tabIndex).toBe(0);
    expect(node('gateway-tab-overview').props.tabIndex).toBe(-1);
    expect(renderer.root.findByProps({ role: 'tabpanel' }).props['aria-labelledby']).toBe(
      'gateway-tab-common'
    );
    await key('common', 'End');
    expect(node('gateway-tab-owners').props['aria-selected']).toBe(true);
    await key('owners', 'ArrowRight');
    expect(node('gateway-tab-overview').props['aria-selected']).toBe(true);
    await key('overview', 'ArrowLeft');
    expect(node('gateway-tab-owners').props['aria-selected']).toBe(true);
    await key('owners', 'Home');
    expect(node('gateway-tab-overview').props['aria-selected']).toBe(true);
    expect(node('retry-value').props.value).toBe('4');
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
  });
  it('shares dirty and validation metrics between Common and Retry, including a hidden invalid field', async () => {
    await mount();
    await click('gateway-tab-common');
    await change('retry-value', 'abc');
    for (const tab of ['common', 'retry']) {
      expect(node('gateway-tab-state-' + tab).props.children).toBe(c('validationIssues', 1));
      expect(node('gateway-tab-' + tab).props['aria-label']).toContain(c('unsaved', 1));
    }
    expect(node('retry-value').props['aria-describedby']).toContain('retry-input-error');
    await change('gateway-search-input', 'proxy');
    expect(node('fresh-managed-retry').props.hidden).toBe(true);
    expect(node('gateway-tab-state-common').props.children).toBe(c('validationIssues', 1));
    await change('retry-value', '4');
    await change('retry-interval-value', '301');
    expect(node('gateway-tab-state-common').props.children).toBe(c('validationIssues', 1));
    expect(node('gateway-tab-retry').props['aria-label']).toContain(c('unsaved', 2));
    await discard();
    expect(has('gateway-tab-state-common')).toBe(false);
    expect(has('gateway-tab-state-retry')).toBe(false);
  });
  it('links available owners within the router basename and closes the ownership dialog on navigation', async () => {
    await mount('/custom-console');
    const href = (id: string) => node(id).findByType('a').props.href;
    expect(href('view-owner-0-link')).toBe('/custom-console/api-keys');
    expect(href('view-owner-1-link')).toBe('/custom-console/ai-providers');
    expect(href('view-owner-3-link')).toBe('/custom-console/accounts');
    await change('retry-value', '4');
    await click('view-owner-0');
    const link = node('open-owner-workspace').findByType('a');
    expect(link.props.href).toBe('/custom-console/api-keys');
    await act(async () => link.props.onClick({ button: 0, preventDefault: vi.fn() }));
    expect(has('owner-detail-modal')).toBe(false);
    expect(node('workspace-route').props.children).toBe('/api-keys');
    expect(node('retry-value').props.value).toBe('4');
    expect(onUnauthorized).not.toHaveBeenCalled();
  });
  it('does not invent links for owners without an existing dedicated editor', async () => {
    await mount();
    expect(has('view-owner-2-link')).toBe(false);
    await click('view-owner-2');
    expect(has('open-owner-workspace')).toBe(false);
    expect(JSON.stringify(renderer.toJSON())).toContain(c('ownerUnavailable'));
  });
  it('reports local dirty state separately from operation pending and supports diff/discard', async () => {
    await mount();
    await change('retry-value', '4');
    expect(node('gateway-dirty-count').props.children).toBe(c('unsaved', 1));
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    expect(onPending.mock.calls.some(([pending]) => pending)).toBe(false);
    await click('view-changes-btn');
    expect(JSON.stringify(renderer.toJSON())).toContain('4');
    await click('close-diff-btn');
    expect(has('diff-modal')).toBe(false);
    await discard();
    expect(node('retry-value').props.value).toBe('2');
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });
  it('consumes each discard once and preserves later drafts through refresh', async () => {
    await mount();
    await change('retry-value', '3');
    await discard();
    await change('retry-value', '4');
    await click('retry-refresh');
    expect(node('retry-value').props.value).toBe('4');
    await discard();
    expect(node('retry-value').props.value).toBe('2');
  });
  it.each([false, true])(
    'preserves a sibling draft after commit (prior discard: %s)',
    async (priorDiscard) => {
      await mount();
      if (priorDiscard) {
        await change('retry-value', '3');
        await discard();
      }
      await change('retry-interval-value', '60');
      await change('retry-value', '4');
      await click('retry-save');
      expect(node('retry-value').props.value).toBe('4');
      expect(node('retry-interval-value').props.value).toBe('60');
      expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    }
  );
  it('does not erase the original UNKNOWN operation or replay it when discarding', async () => {
    await mount();
    await change('retry-value', '4');
    transport.mockRejectedValueOnce(new TypeError('response lost'));
    await click('retry-save');
    const id = node('retry-operation-id').props.children;
    await discard();
    await click('gateway-tab-requests');
    await click('gateway-tab-retry');
    expect(node('retry-operation-id').props.children).toBe(id);
    expect(node('retry-save').props.disabled).toBe(true);
    expect(onPending).toHaveBeenLastCalledWith(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });
  it.each(['overview', 'common', 'routing', 'owners'])('searches globally from %s', async (tab) => {
    await mount();
    await click('gateway-tab-' + tab);
    await change('gateway-search-input', 'bootstrap');
    expect(
      renderer.root.findAllByProps({ id: 'field-requests.streaming.bootstrap-retries' })
    ).toHaveLength(1);
    expect(node('fresh-managed-retry').props.hidden).toBe(true);
  });
  it('filters actual dirty paths rather than their entire category', async () => {
    await mount();
    await change('retry-value', '4');
    await click('gateway-tab-retry');
    await changedOnly();
    expect(node('retry-editor').props.hidden).toBe(false);
    expect(node('retry-interval-editor').props.hidden).toBe(true);
    expect(has('section-retry')).toBe(false);
    await click('gateway-tab-requests');
    expect(has('section-requests')).toBe(false);
    expect(node('fresh-managed-retry').props.hidden).toBe(true);
    expect(node('retry-value').props.value).toBe('4');
  });
  it('shows only the modified field when changed-only is enabled from Overview', async () => {
    await mount();
    await change('retry-value', '4');
    await changedOnly();
    expect(has('gateway-overview-content')).toBe(false);
    expect(node('retry-editor').props.hidden).toBe(false);
    expect(node('retry-interval-editor').props.hidden).toBe(true);
    expect(has('section-routing')).toBe(false);
  });
  it('matches translated labels, hides unrelated editors, and retains drafts while searching', async () => {
    await mount();
    await change('retry-value', '4');
    await change('gateway-search-input', '最大冷却');
    expect(node('retry-editor').props.hidden).toBe(true);
    expect(node('retry-interval-editor').props.hidden).toBe(false);
    await change('gateway-search-input', 'proxy');
    expect(node('fresh-managed-retry').props.hidden).toBe(true);
    await click('gateway-tab-retry');
    expect(node('retry-value').props.value).toBe('4');
  });
  it('never displays sample values or invented health after a failed read', async () => {
    transport.mockResolvedValue(new Response('', { status: 503 }));
    await mount();
    const text = JSON.stringify(renderer.toJSON());
    expect(text).not.toContain('Embedded');
    expect(text).not.toContain('草稿与服务端一致');
    expect(text).not.toContain('已开放写操作');
    await click('gateway-tab-media');
    const input = renderer.root.findByProps({ id: 'field-multimedia.gpt-image-2-base-model' });
    expect(input.props.value).toBe('');
    expect(input.props.placeholder).toBe(c('notRead'));
  });
  it.each(['en', 'zh-TW', 'ru'])(
    'translates the workspace, catalog and open dialog in %s without Chinese fallback',
    async (language) => {
      await mount();
      await change('retry-value', '4');
      await click('view-changes-btn');
      await act(async () => {
        await i18n.changeLanguage(language);
      });
      expect(node('gateway-workspace-title').props.children).toBe(c('gateway'));
      expect(JSON.stringify(renderer.toJSON())).not.toMatch(/未保存|额外|网关|配置归属/);
      expect(node('retry-value').props.value).toBe('4');
    }
  );
});
