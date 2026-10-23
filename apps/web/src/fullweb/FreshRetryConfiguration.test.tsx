import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { FreshRetryConfiguration } from './FreshRetryConfiguration';
import { createPublicApiClient } from './publicApi';

const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };
const t = (key: string) => i18n.t('fullweb.' + key);
const expected = {
  sourceId: 'isolated-source-1234',
  generation: '7',
  revision: 2,
  sha256: 'a'.repeat(64),
};
const credential = 'admin-only-in-memory';

function retryView(overrides: Record<string, unknown> = {}) {
  return {
    field: 'routing.retry.request-retry',
    presence: 'present',
    value: 2,
    source: 'private-embedded-cpa-v8.0.13',
    authority: 'host-runtime',
    editable: true,
    reason: '',
    persisted: 'persisted',
    effective: 'unknown',
    concurrentProtection: 'host-exclusive-runtime-reservation',
    expected,
    ...overrides,
  };
}

function operation(
  state: 'unknown' | 'persisted' | 'recovered',
  value = 3,
  requestId = 'ab'.repeat(16)
) {
  return {
    requestId,
    value,
    sourceId: expected.sourceId,
    generation: expected.generation,
    revision: expected.revision,
    sha256: expected.sha256,
    state,
  };
}

function operationResponse(next: ReturnType<typeof operation>) {
  return Response.json({ operation: next, effective: 'unknown' });
}

describe('Full Web request retry editor', () => {
  let renderer: ReactTestRenderer;
  let pendingEvents: boolean[];

  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    pendingEvents = [];
    vi.stubGlobal('window', { location: new URL('https://manager.example/console') });
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => bytes.fill(0xab),
    });
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.unstubAllGlobals();
  });

  async function mount(transport: ReturnType<typeof vi.fn<typeof fetch>>, intervalTransport?: ReturnType<typeof vi.fn<typeof fetch>>) {
    await act(async () => {
      renderer = create(
        <FreshRetryConfiguration
          client={createPublicApiClient(() => context, ((url, init) =>
            (String(url).includes('/gateway/retry/max-retry-interval') ||
              String(url).includes('/gateway/retry/interval-operations/'))
              ? (intervalTransport
                  ? intervalTransport(url, init)
                  : Promise.resolve(Response.json(retryView({
                      field: 'routing.retry.max-retry-interval',
                      value: 0,
                    }))))
              : transport(url, init)) as typeof fetch)}
          credential={credential}
          onUnauthorized={vi.fn()}
          onPending={(value) => pendingEvents.push(value)}
        />
      );
    });
  }

  function input() {
    return renderer.root.findByProps({ 'data-testid': 'retry-value' });
  }

  function button(testId: string) {
    return renderer.root.findByProps({ 'data-testid': testId });
  }

  async function change(value: string) {
    await act(async () => input().props.onChange({ target: { value } }));
  }

  async function click(testId: string) {
    await act(async () => {
      button(testId).props.onClick();
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
    });
  }


  it.each([
    ['missing', null, ''],
    ['null', null, ''],
    ['present', 0, '0'],
    ['present', 300, '300'],
  ] as const)('B3 preserves %s presence and explicit zero', async (presence, value, draft) => {
    const b2 = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView()));
    const b3 = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView({
      field: 'routing.retry.max-retry-interval', presence, value,
    })));
    await mount(b2, b3);
    expect(renderer.root.findByProps({'data-testid':'retry-interval-value'}).props.value).toBe(draft);
    expect(b2.mock.calls.every(([url]) => String(url).includes('/request-retry'))).toBe(true);
  });

  it('B3 saves 300 exactly once without changing B2 editor or claiming effective', async () => {
    const b2 = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView()));
    const b3 = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView({
        field: 'routing.retry.max-retry-interval', presence: 'missing', value: null,
      })))
      .mockResolvedValueOnce(Response.json({ operation: {
        ...operation('persisted', 300), field: 'max-retry-interval',
      }, effective: 'unknown' }))
      .mockResolvedValueOnce(Response.json(retryView({
        field: 'routing.retry.max-retry-interval', value: 300,
      })));
    await mount(b2, b3);
    await act(async () => renderer.root.findByProps({'data-testid':'retry-interval-value'}).props.onChange({target:{value:'300'}}));
    await click('retry-interval-save');
    expect(b3.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(JSON.parse(String(b3.mock.calls.find(([, init]) => init?.method === 'PUT')?.[1]?.body)).value).toBe(300);
    expect(renderer.root.findByProps({'data-testid':'retry-value'}).props.value).toBe('2');
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryEffectiveUnknown'));
  });

  it('B3 rejects invalid input and preserves an unknown operation without replay', async () => {
    const b2 = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView()));
    const b3 = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView({
        field: 'routing.retry.max-retry-interval', value: 2,
      })))
      .mockRejectedValueOnce(new Error('lost response'))
      .mockResolvedValueOnce(Response.json({ operation: {
        ...operation('unknown', 5), field: 'max-retry-interval',
      }, effective: 'unknown' }));
    await mount(b2, b3);
    const input = renderer.root.findByProps({'data-testid':'retry-interval-value'});
    for (const invalid of ['-1', '1.5', '301', 'xyz']) {
      await act(async () => input.props.onChange({target:{value:invalid}}));
      expect(button('retry-interval-save').props.disabled).toBe(true);
    }
    await act(async () => input.props.onChange({target:{value:'5'}}));
    await click('retry-interval-save');
    expect(button('retry-interval-save').props.disabled).toBe(true);
    expect(button('retry-save').props.disabled).toBe(true);
    await click('retry-interval-check-operation');
    expect(b3.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });
  it('B3 reconciles only its original interval operation after a lost PUT reply', async () => {
    const id = 'ab'.repeat(16);
    const b2 = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView()));
    const intervalOperation = (state: 'unknown' | 'persisted') => ({
      operation: { ...operation(state, 5, id), field: 'max-retry-interval' },
      effective: 'unknown',
    });
    const b3 = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView({
        field: 'routing.retry.max-retry-interval', value: 2,
      })))
      .mockRejectedValueOnce(new Error('lost PUT reply'))
      .mockResolvedValueOnce(Response.json(intervalOperation('unknown')))
      .mockResolvedValueOnce(Response.json(intervalOperation('persisted')))
      .mockResolvedValueOnce(Response.json(retryView({
        field: 'routing.retry.max-retry-interval', value: 5,
        operation: intervalOperation('persisted').operation,
      })));
    await mount(b2, b3);
    await act(async () => renderer.root.findByProps({ 'data-testid': 'retry-interval-value' }).props.onChange({ target: { value: '5' } }));
    await click('retry-interval-save');
    expect(button('retry-save').props.disabled).toBe(true);
    await click('retry-interval-check-operation');
    expect(button('retry-save').props.disabled).toBe(true);
    await click('retry-interval-reconcile');
    expect(renderer.root.findByProps({ 'data-testid': 'retry-interval-value' }).props.value).toBe('5');
    expect(pendingEvents[pendingEvents.length - 1]).toBe(false);
    expect(b3.mock.calls.map(([url, init]) => [String(url).split('/gateway/retry/')[1], init?.method])).toEqual([
      ['max-retry-interval', 'GET'],
      ['max-retry-interval', 'PUT'],
      [`interval-operations/${id}`, 'GET'],
      [`interval-operations/${id}/reconcile`, 'POST'],
      ['max-retry-interval', 'GET'],
    ]);
    expect(b2.mock.calls.every(([url]) => String(url).includes('/request-retry'))).toBe(true);
    expect(b3.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it.each([
    ['missing', null, '', 'retryPresenceMissing'],
    ['null', null, '', 'retryPresenceNull'],
    ['present', 0, '0', 'retryPresencePresent'],
    ['present', 8, '8', 'retryPresencePresent'],
  ] as const)(
    'preserves %s/null/zero/present display semantics',
    async (presence, value, draft, label) => {
      const transport = vi
        .fn<typeof fetch>()
        .mockResolvedValue(Response.json(retryView({ presence, value })));
      await mount(transport);
      expect(input().props.value).toBe(draft);
      expect(renderer.root.findAllByProps({ className: 'info-val' })[1].props.children).toBe(
        presence === 'present'
          ? String(value)
          : t(presence === 'missing' ? 'retryPresenceMissing' : 'retryPresenceNull')
      );
      expect(JSON.stringify(renderer.toJSON())).toContain(t(label));
    }
  );

  it('shows the writer-isolation lock and prevents saving when Administrator is read-only', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        Response.json(
          retryView({ editable: false, reason: 'config_key_writer_not_isolated', value: 0 })
        )
      );
    await mount(transport);
    expect(input().props.value).toBe('0');
    expect(input().props.disabled).toBe(true);
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryWriterNotIsolated'));
    expect(button('retry-save').props.disabled).toBe(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
  });

  it('shows the private-source lock and prevents saving when the source is unavailable', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        retryView({
          editable: false,
          reason: 'private_source_unavailable',
          value: null,
          presence: 'unknown',
        })
      )
    );
    await mount(transport);
    expect(input().props.value).toBe('');
    expect(input().props.disabled).toBe(true);
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retrySourceUnavailable'));
    expect(button('retry-save').props.disabled).toBe(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
  });

  it('keeps an unknown field state read-only even if an inconsistent response says editable', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        Response.json(
          retryView({ editable: true, presence: 'unknown', persisted: 'unknown', value: null })
        )
      );
    await mount(transport);
    expect(input().props.value).toBe('');
    expect(input().props.disabled).toBe(true);
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryPresenceUnknown'));
    expect(button('retry-save').props.disabled).toBe(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
  });

  it('rejects blank, decimal, negative, exponent, string-like and out-of-range drafts before PUT', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json(retryView()));
    await mount(transport);
    for (const value of ['', '-1', '1.5', '1e2', '11', '0.0', ' 1', '+1', 'text']) {
      await change(value);
      expect(button('retry-save').props.disabled).toBe(true);
    }
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
  });

  it('saves zero and the complete latest expected source object exactly once', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView({ value: 2 })))
      .mockResolvedValueOnce(operationResponse(operation('persisted', 0)))
      .mockResolvedValueOnce(
        Response.json(retryView({ value: 0, operation: operation('persisted', 0) }))
      );
    await mount(transport);
    await change('0');
    await click('retry-save');
    const writes = transport.mock.calls.filter(([, init]) => init?.method === 'PUT');
    expect(writes).toHaveLength(1);
    expect(JSON.parse(String(writes[0][1]?.body))).toEqual({
      requestId: 'ab'.repeat(16),
      value: 0,
      expected,
    });
    expect(input().props.value).toBe('0');
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryEffectiveUnknown'));
  });

  it.each([409, 503])('retains the original ID and never repeats PUT after %s', async (status) => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockResolvedValueOnce(new Response('', { status }));
    await mount(transport);
    await change('3');
    await click('retry-save');
    expect(renderer.root.findByProps({ 'data-testid': 'retry-operation-id' }).props.children).toBe(
      'ab'.repeat(16)
    );
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryOperationNoPutReplay'));
  });

  it('releases a request rejected by a real HTTP 400 without inventing an unknown operation', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockResolvedValueOnce(
        Response.json(
          { code: 'invalid_retry_request', message: 'private details' },
          { status: 400 }
        )
      );
    await mount(transport);
    await change('3');
    await click('retry-save');
    expect(pendingEvents.slice(-2)).toEqual([true, false]);
    expect(renderer.root.findAllByProps({ 'data-testid': 'retry-operation-id' })).toHaveLength(0);
    expect(button('retry-save').props.disabled).toBe(false);
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retrySaveRejected'));
    expect(JSON.stringify(renderer.toJSON())).not.toContain('private details');
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it.each([400, 409, 503])(
    'keeps uncertain HTTP %s locked unless it is a confirmed validation rejection',
    async (status) => {
      const transport = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json(retryView()))
        .mockResolvedValueOnce(
          Response.json(
            { code: status === 400 ? 'private unknown code' : 'invalid_retry_request' },
            { status }
          )
        );
      await mount(transport);
      await change('3');
      await click('retry-save');
      expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
      expect(button('retry-save').props.disabled).toBe(true);
      expect(
        renderer.root.findByProps({ 'data-testid': 'retry-operation-id' }).props.children
      ).toBe('ab'.repeat(16));
      expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    }
  );

  it.each(['initial read', 'refresh'])(
    'shows a sanitized conflict lock on %s and forbids writes',
    async (stage) => {
      const transport = vi.fn<typeof fetch>();
      if (stage === 'refresh') transport.mockResolvedValueOnce(Response.json(retryView()));
      transport.mockResolvedValueOnce(
        Response.json(
          {
            code: 'config_key_writer_not_isolated',
            message: 'secret upstream error',
          },
          { status: 409 }
        )
      );
      await mount(transport);
      if (stage === 'refresh') await click('retry-refresh');
      expect(
        renderer.root.findByProps({ 'data-testid': 'retry-read-lock-reason' }).props.children
      ).toBe(t('retryWriterNotIsolated'));
      expect(JSON.stringify(renderer.toJSON())).toContain(t('retryReadConflict'));
      expect(JSON.stringify(renderer.toJSON())).not.toContain('secret upstream error');
      if (stage === 'refresh') {
        expect(input().props.disabled).toBe(true);
        await change('3');
        await click('retry-save');
        expect(button('retry-save').props.disabled).toBe(true);
      }
      expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
    }
  );

  it('retains the original operation after a real transport abort timeout without another PUT', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockImplementationOnce(
        (_url, init) =>
          new Promise((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () =>
              reject(new Error('private timeout detail'))
            );
          })
      );
    await mount(transport);
    await change('3');
    vi.useFakeTimers();
    try {
      await act(async () => {
        button('retry-save').props.onClick();
      });
      expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(15000);
      });
      expect(
        renderer.root.findByProps({ 'data-testid': 'retry-operation-id' }).props.children
      ).toBe('ab'.repeat(16));
      expect(button('retry-save').props.disabled).toBe(true);
      expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
      expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
      expect(JSON.stringify(renderer.toJSON())).not.toContain('private timeout detail');
    } finally {
      vi.useRealTimers();
    }
  });

  it('retains the original ID and never repeats PUT after a network failure', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockRejectedValueOnce(new Error('connection lost'));
    await mount(transport);
    await change('3');
    await click('retry-save');
    expect(renderer.root.findByProps({ 'data-testid': 'retry-operation-id' }).props.children).toBe(
      'ab'.repeat(16)
    );
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryOperationNoPutReplay'));
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it('ignores a duplicate save click while the original PUT is in flight', async () => {
    let resolvePut!: (response: Response) => void;
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockReturnValueOnce(new Promise<Response>((resolve) => (resolvePut = resolve)));
    await mount(transport);
    await change('3');
    await act(async () => {
      button('retry-save').props.onClick();
      button('retry-save').props.onClick();
      await Promise.resolve();
    });
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    resolvePut(new Response('', { status: 503 }));
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
    });
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryOperationNoPutReplay'));
  });

  it('does not adopt a mismatched operation identity from a failed response', async () => {
    const foreign = operation('unknown', 3, 'cd'.repeat(16));
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView()))
      .mockResolvedValueOnce(
        Response.json(
          {
            code: 'source_or_operation_conflict',
            result: { operation: foreign, effective: 'unknown' },
          },
          { status: 409 }
        )
      );
    await mount(transport);
    await change('3');
    await click('retry-save');
    expect(renderer.root.findByProps({ 'data-testid': 'retry-operation-id' }).props.children).toBe(
      'ab'.repeat(16)
    );
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryOperationNoPutReplay'));
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it.each(['network', '409', '503', '400'])(
    'isolates a second save from the previous persisted operation after %s',
    async (failure) => {
      const firstId = 'ab'.repeat(16);
      const secondId = 'cd'.repeat(16);
      let allocated = 0;
      vi.stubGlobal('crypto', {
        getRandomValues: (bytes: Uint8Array) => bytes.fill(allocated++ === 0 ? 0xab : 0xcd),
      });
      const first = operation('persisted', 3, firstId);
      const recovered = operation('recovered', 0, secondId);
      const latestExpected = { ...expected, revision: 3, sha256: 'b'.repeat(64) };
      let finishSecond!: () => void;
      const transport = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json(retryView()))
        .mockResolvedValueOnce(operationResponse(first))
        .mockResolvedValueOnce(
          Response.json(retryView({ value: 3, expected: latestExpected, operation: first }))
        )
        .mockImplementationOnce(
          () =>
            new Promise<Response>((resolve, reject) => {
              finishSecond = () => {
                if (failure === 'network') reject(new Error('private lost response detail'));
                else
                  resolve(
                    Response.json(
                      { code: failure === '400' ? 'invalid_retry_request' : 'journal_unknown' },
                      { status: Number(failure) }
                    )
                  );
              };
            })
        )
        .mockResolvedValueOnce(operationResponse(operation('unknown', 0, secondId)))
        .mockResolvedValueOnce(operationResponse(recovered))
        .mockResolvedValueOnce(Response.json(retryView({ value: 7, operation: recovered })));
      await mount(transport);
      await change('3');
      await click('retry-save');
      expect(button('retry-operation-id').props.children).toBe(firstId);
      expect(button('retry-operation').findAllByType('p')[1].children).toContain(
        t('retryOperationPersisted')
      );
      await change('0');
      await click('retry-save');
      expect(renderer.root.findAllByProps({ 'data-testid': 'retry-operation-id' })).toHaveLength(0);
      expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
      expect(button('retry-save').props.disabled).toBe(true);
      await act(async () => {
        finishSecond();
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
      });
      const puts = transport.mock.calls.filter(([, init]) => init?.method === 'PUT');
      expect(puts).toHaveLength(2);
      expect(JSON.parse(String(puts[1][1]?.body))).toEqual({
        requestId: secondId,
        value: 0,
        expected: latestExpected,
      });
      if (failure === '400') {
        expect(renderer.root.findAllByProps({ 'data-testid': 'retry-operation' })).toHaveLength(0);
        expect(JSON.stringify(renderer.toJSON())).toContain(t('retrySaveRejected'));
        expect(pendingEvents[pendingEvents.length - 1]).toBe(false);
        expect(button('retry-save').props.disabled).toBe(false);
        return;
      }
      expect(button('retry-operation-id').props.children).toBe(secondId);
      const unknownResult = button('retry-operation').findAllByType('p')[1].children.join('');
      expect(unknownResult).toContain(t('retryOperationUnknown'));
      expect(unknownResult).not.toContain(t('retryOperationPersisted'));
      expect(JSON.stringify(renderer.toJSON())).not.toContain('private lost response detail');
      expect(pendingEvents[pendingEvents.length - 1]).toBe(true);
      expect(button('retry-save').props.disabled).toBe(true);
      await click('retry-check-operation');
      expect(button('retry-operation-id').props.children).toBe(secondId);
      expect(transport.mock.calls[4][0]).toBe(
        `https://manager.example${context.apiBase}/gateway/retry/operations/${secondId}`
      );
      expect(transport.mock.calls[4][1]?.method).toBe('GET');
      await click('retry-reconcile');
      expect(transport.mock.calls[5][0]).toBe(
        `https://manager.example${context.apiBase}/gateway/retry/operations/${secondId}/reconcile`
      );
      expect(transport.mock.calls[5][1]?.method).toBe('POST');
      expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(2);
      expect(button('retry-operation-id').props.children).toBe(secondId);
      expect(input().props.value).toBe('7');
      expect(JSON.stringify(renderer.toJSON())).toContain(t('retryRecovered'));
      expect(JSON.stringify(renderer.toJSON())).toContain(t('retryEffectiveUnknown'));
      expect(pendingEvents[pendingEvents.length - 1]).toBe(false);
    }
  );

  it('checks and reconciles the original unknown operation, then reloads the actual recovered value', async () => {
    const recovered = operation('recovered', 3);
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(retryView({ value: 1 })))
      .mockResolvedValueOnce(operationResponse(operation('unknown', 3)))
      .mockResolvedValueOnce(operationResponse(operation('unknown', 3)))
      .mockResolvedValueOnce(operationResponse(recovered))
      .mockResolvedValueOnce(Response.json(retryView({ value: 7, operation: recovered })));
    await mount(transport);
    await change('3');
    await click('retry-save');
    await click('retry-check-operation');
    await click('retry-reconcile');
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(input().props.value).toBe('7');
    expect(JSON.stringify(renderer.toJSON())).toContain(t('retryRecovered'));
    expect(pendingEvents[pendingEvents.length - 1]).toBe(false);
  });
});
