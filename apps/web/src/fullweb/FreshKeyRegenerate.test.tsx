import type { ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { fullWebText as t } from '../i18n/fullweb';
import { createPublicApiClient } from './publicApi';
import { FreshKeyRegenerate } from './FreshKeyRegenerate';
import type { Key } from './freshKeyMetadata';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    closeDisabled,
    children,
    footer,
  }: {
    open: boolean;
    closeDisabled?: boolean;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div className="mock-modal" data-close-disabled={String(Boolean(closeDisabled))}>
        {children}
        {footer}
      </div>
    ) : null,
}));

const routingContext = () => ({ basePath: '/console', apiBase: '/console/api/v2', revision: 1 });

vi.mock('./FreshAccessInfo', () => ({
  AccessInfoSummary: ({
    testIdPrefix,
    defaultProtocolsCollapsed,
    clientOnly,
  }: {
    testIdPrefix?: string;
    defaultProtocolsCollapsed?: boolean;
    clientOnly?: boolean;
  }) => (
    <div
      data-testid={`${testIdPrefix || 'access'}-summary`}
      data-collapsed={String(Boolean(defaultProtocolsCollapsed))}
      data-client-only={String(Boolean(clientOnly))}
    />
  ),
}));

const targetKey: Key = {
  id: '11111111111111111111111111111111',
  requestId: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  maskedPreview: 'cpamp_sk_…1111',
  createdAtMs: 1700000000000,
  managerState: 'persisted',
  runtimeState: 'effective_observed',
  name: 'Prod-CLI',
  nameRevision: 2,
  lifecycle: 'active',
  origin: 'workspace',
};

const rotatedKey: Key = {
  ...targetKey,
  maskedPreview: 'cpamp_sk_…9f2a',
  lastRotatedAtMs: 1700000009000,
};

const newSecret = 'cpamp_sk_' + 'b'.repeat(60) + '9f2a';

let renderer: ReactTestRenderer;
const view = () => JSON.stringify(renderer.toJSON());

describe('FreshKeyRegenerate (Option B in-place rotation)', () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
    });
    let seq = 0x10;
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.fill(seq);
        seq += 1;
        return bytes;
      },
    });
    vi.stubGlobal('navigator', {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
  });

  afterEach(async () => {
    await act(async () => renderer?.unmount());
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('rotates secret atomically via POST api-keys/{id}/regenerate preserving identity and name with compact access summary', async () => {
    const onChanged = vi.fn();
    const onPending = vi.fn();
    const onLatestOperation = vi.fn();
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      Response.json({
        key: rotatedKey,
        createdSecret: newSecret,
      })
    );
    const client = createPublicApiClient(routingContext, fetchMock);

    await act(async () => {
      renderer = create(
        <FreshKeyRegenerate
          open={true}
          target={targetKey}
          client={client}
          credential="admin-test-only"
          accessInfo={{
            mode: 'new_managed',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          }}
          onUnauthorized={vi.fn()}
          onPending={onPending}
          onLatestOperation={onLatestOperation}
          onChanged={onChanged}
          onClose={vi.fn()}
        />
      );
    });

    expect(
      renderer.root.findByProps({ 'data-testid': 'key-regenerate-target-card' }).findByType('code')
        .props.children
    ).toBe(targetKey.maskedPreview);

    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [reqUrl, reqInit] = fetchMock.mock.calls[0];
    expect(String(reqUrl)).toContain(`/api-keys/${targetKey.id}/regenerate`);
    expect(reqInit?.method).toBe('POST');
    expect(JSON.parse(String(reqInit?.body))).toEqual({
      requestId: '10'.repeat(16),
    });

    expect(onPending).toHaveBeenLastCalledWith(false);
    expect(onLatestOperation).toHaveBeenLastCalledWith(null);
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(view()).toContain(k('regenerateSuccessTitle'));
    expect(view()).toContain('Prod-CLI');
    expect(view()).toContain(newSecret);
    expect(
      renderer.root.findByProps({ 'data-testid': 'regenerate-access-info-summary' }).props[
        'data-collapsed'
      ]
    ).toBe('true');
    expect(
      renderer.root.findByProps({ 'data-testid': 'regenerate-access-info-summary' }).props[
        'data-client-only'
      ]
    ).toBe('true');

    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'copy-regenerated-client-secret' })
        .props.onClick();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(newSecret);
    expect(view()).toContain(t('keysCopied'));
  });

  it('enters recovery on unsettled rotate 503, keeps requestId across retries, forwards blockers, and copies new secret after continue', async () => {
    const opId = 'a'.repeat(32);
    const unknownRotateOp = {
      id: opId,
      targetId: targetKey.id,
      keeperId: '',
      mode: 'rotate' as const,
      outcome: 'unknown' as const,
      revision: 1,
      steps: [{ name: 'rotate' as const, state: 'attempted' as const, returned: false }],
    };
    const committedRotateOp = {
      ...unknownRotateOp,
      outcome: 'committed' as const,
      revision: 2,
      steps: [{ name: 'rotate' as const, state: 'observed' as const, returned: true }],
    };
    const onPending = vi.fn();
    const onChanged = vi.fn();
    const onBlocker = vi.fn();
    const onExternalError = vi.fn();
    const onLatestOperation = vi.fn();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json(
          {
            code: 'external_management_key_rejected',
            detail: '401 upstream',
            key: { ...targetKey, runtimeState: 'effect_unknown', operationId: opId },
            result: {
              operation: unknownRotateOp,
              nextStep: 'rotate',
              runtimeState: 'effect_unknown',
              expiresAtMs: 0,
            },
          },
          { status: 409 }
        )
      )
      .mockResolvedValueOnce(
        Response.json({
          operation: unknownRotateOp,
          nextStep: 'rotate',
          runtimeState: 'effect_unknown',
          expiresAtMs: 0,
        })
      )
      .mockResolvedValueOnce(
        Response.json({
          operation: committedRotateOp,
          nextStep: '',
          runtimeState: 'effective_observed',
          expiresAtMs: 0,
        })
      )
      .mockResolvedValueOnce(Response.json(rotatedKey))
      .mockResolvedValueOnce(Response.json({ secret: newSecret }));
    const client = createPublicApiClient(routingContext, fetchMock);

    await act(async () => {
      renderer = create(
        <FreshKeyRegenerate
          open={true}
          target={targetKey}
          client={client}
          credential="admin-test-only"
          onUnauthorized={vi.fn()}
          onPending={onPending}
          onLatestOperation={onLatestOperation}
          onBlocker={onBlocker}
          onExternalError={onExternalError}
          onChanged={onChanged}
          onClose={vi.fn()}
        />
      );
    });

    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });

    expect(onBlocker).toHaveBeenCalledWith('external_management_key_rejected', '401 upstream');
    expect(onExternalError).toHaveBeenCalledWith(
      'external_management_key_rejected',
      '401 upstream'
    );
    expect(onPending).toHaveBeenLastCalledWith(true);
    expect(onLatestOperation).toHaveBeenLastCalledWith(
      expect.objectContaining({
        operation: unknownRotateOp,
      })
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'regenerate-recovery-state' })
    ).toHaveLength(1);
    // Recovery allows closing the modal once the request settled (not busy).
    expect(renderer.root.findByProps({ className: 'mock-modal' }).props['data-close-disabled']).toBe(
      'false'
    );

    // Check and continue settles the rotate operation.
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'regenerate-check-continue' })
        .props.onClick();
    });
    expect(String(fetchMock.mock.calls[1][0])).toContain(`/api-keys/operations/${opId}`);
    expect(String(fetchMock.mock.calls[2][0])).toContain(`/api-keys/operations/${opId}/continue`);
    expect(onPending).toHaveBeenLastCalledWith(false);
    expect(view()).toContain(k('regenerateSuccessTitle'));

    // Replay/recovery done state retrieves new secret on demand.
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'fetch-regenerated-client-secret' })
        .props.onClick();
    });
    expect(String(fetchMock.mock.calls[3][0])).toContain(`/api-keys/${targetKey.id}`);
    expect(String(fetchMock.mock.calls[4][0])).toContain(`/api-keys/${targetKey.id}/secret`);
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(newSecret);
    expect(view()).toContain(t('keysCopied'));
  });

  it('keeps the same requestId on transport error and rotates to a fresh requestId after 400 or aborted outcome', async () => {
    const opId = 'b'.repeat(32);
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new Error('network drop'))
      .mockResolvedValueOnce(Response.json({ code: 'key_operation_not_found' }, { status: 404 }))
      .mockResolvedValueOnce(Response.json({ code: 'bad_request' }, { status: 400 }))
      .mockResolvedValueOnce(
        Response.json(
          {
            code: 'key_operation_conflict',
            result: {
              operation: {
                id: opId,
                targetId: targetKey.id,
                keeperId: '',
                mode: 'rotate',
                outcome: 'aborted',
                revision: 2,
                steps: [{ name: 'rotate', state: 'unattempted', returned: false }],
              },
              nextStep: '',
              runtimeState: 'not_applied',
              expiresAtMs: 0,
            },
          },
          { status: 409 }
        )
      )
      .mockResolvedValueOnce(
        Response.json({ key: rotatedKey, createdSecret: newSecret })
      );
    const client = createPublicApiClient(routingContext, fetchMock);

    await act(async () => {
      renderer = create(
        <FreshKeyRegenerate
          open={true}
          target={targetKey}
          client={client}
          credential="admin-test-only"
          onUnauthorized={vi.fn()}
          onPending={vi.fn()}
          onChanged={vi.fn()}
          onClose={vi.fn()}
        />
      );
    });

    // 1. Network error -> enters unverified reconciliation; 404 proves unstarted and preserves exact requestId ('10'.repeat(16)).
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'regenerate-check-continue' })
        .props.onClick();
    });
    expect(String(fetchMock.mock.calls[1][0])).toContain(`/api-keys/operations/${'10'.repeat(16)}`);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });
    expect(fetchMock.mock.calls[0][1]?.body).toBe(fetchMock.mock.calls[2][1]?.body);

    // 2. Previous call got 400 -> next attempt gets fresh requestId ('11'.repeat(16)).
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });
    expect(JSON.parse(String(fetchMock.mock.calls[3][1]?.body)).requestId).toBe('11'.repeat(16));

    // 3. Previous call returned aborted -> next attempt gets fresh requestId ('12'.repeat(16)).
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
    });
    expect(JSON.parse(String(fetchMock.mock.calls[4][1]?.body)).requestId).toBe('12'.repeat(16));
    expect(view()).toContain(k('regenerateSuccessTitle'));
  });

  it('retains observed journal recovery steps when POST continue fails with a transport error and ignores 400 reset in recovery stage', async () => {
    const pendingRotate = {
      id: '10'.repeat(16),
      targetId: targetKey.id,
      keeperId: '',
      mode: 'rotate' as const,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'rotate' as const, state: 'unattempted' as const, returned: false }],
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      // 1. Initial POST /regenerate drops on network -> unverified recovery stage.
      .mockRejectedValueOnce(new Error('network drop'))
      // 2. GET /operations/<id> returns journaled pending operation.
      .mockResolvedValueOnce(
        Response.json({
          operation: pendingRotate,
          nextStep: 'rotate',
          runtimeState: 'effect_unknown',
          expiresAtMs: 0,
        })
      )
      // 3. POST /operations/<id>/continue drops on network -> journal steps MUST remain visible!
      .mockRejectedValueOnce(new Error('continue network drop'))
      // 4. Subsequent GET /operations/<id> + POST /continue returns unexpected 400 -> must NOT release onPending or wipe recovery stage.
      .mockResolvedValueOnce(
        Response.json({
          operation: pendingRotate,
          nextStep: 'rotate',
          runtimeState: 'effect_unknown',
          expiresAtMs: 0,
        })
      )
      .mockResolvedValueOnce(Response.json({ code: 'invalid_request' }, { status: 400 }));
    const onPending = vi.fn();
    const client = createPublicApiClient(routingContext, fetchMock);

    await act(async () => {
      renderer = create(
        <FreshKeyRegenerate
          open={true}
          target={targetKey}
          client={client}
          credential="admin-test-only"
          onUnauthorized={vi.fn()}
          onPending={onPending}
          onChanged={vi.fn()}
          onClose={vi.fn()}
        />
      );
    });

    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'confirm-regenerate-key' }).props.onClick();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'regenerate-check-continue' })
        .props.onClick();
    });
    // Observed journal step must stay rendered even though POST /continue threw!
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'regenerate-step-rotate' })
    ).toHaveLength(1);

    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'regenerate-check-continue' })
        .props.onClick();
    });
    expect(onPending).not.toHaveBeenLastCalledWith(false);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'regenerate-step-rotate' })
    ).toHaveLength(1);
  });
});
