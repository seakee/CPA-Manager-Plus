import type { ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import { CPAUpdateCard } from './CPAUpdateCard';
import type { CPAUpdateFlow } from './useCPAUpdateFlow';
import type { CPAUpdateState } from './cpaUpdateApi';
import { originalArtifact, updateIntent, updateStatus } from './cpaUpdateTestFixtures';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const value = key
        .split('.')
        .reduce<unknown>(
          (source, part) =>
            source && typeof source === 'object'
              ? (source as Record<string, unknown>)[part]
              : undefined,
          en
        );
      let result = typeof value === 'string' ? value : String(options?.defaultValue ?? key);
      for (const [name, replacement] of Object.entries(options ?? {})) {
        result = result.split('{{' + name + '}}').join(String(replacement));
      }
      return result;
    },
    i18n: { language: 'en' },
  }),
}));
vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
  }: {
    open: boolean;
    title: ReactNode;
    children: ReactNode;
    footer: ReactNode;
  }) =>
    open ? (
      <div role="dialog">
        <h2>{title}</h2>
        {children}
        {footer}
      </div>
    ) : null,
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let renderer: ReactTestRenderer | undefined;
const text = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : text(child))).join('');
const button = (label: string) =>
  renderer!.root.find((node) => node.type === 'button' && text(node) === label);
const flowState = (patch: Partial<CPAUpdateFlow> = {}): CPAUpdateFlow => ({
  status: updateStatus(),
  statusError: false,
  intent: null,
  recoveryRecord: { raw: JSON.stringify(updateIntent()) },
  stage: 'idle',
  initialized: true,
  busy: false,
  storageError: null,
  available: true,
  demo: false,
  scope: 'scope-a',
  unresolved: false,
  canCheck: true,
  canPrepare: true,
  canActivate: false,
  canRetry: false,
  refresh: vi.fn(),
  check: vi.fn(),
  prepare: vi.fn(),
  activate: vi.fn(),
  retry: vi.fn(),
  forget: vi.fn(),
  ...patch,
});
const render = async (flow: CPAUpdateFlow) => {
  await act(async () => {
    if (renderer) renderer.update(<CPAUpdateCard flow={flow} />);
    else renderer = create(<CPAUpdateCard flow={flow} />);
  });
};
const click = async (label: string) => {
  await act(async () => {
    button(label).props.onClick();
  });
};
afterEach(async () => {
  if (renderer)
    await act(async () => {
      renderer?.unmount();
    });
  renderer = undefined;
});

describe('CPA update product controls', () => {
  it.each([
    'never_checked',
    'up_to_date',
    'update_available',
    'ahead_of_stable',
    'unknown_version',
    'unsupported',
    'managed_externally',
  ] satisfies CPAUpdateState[])(
    'presents %s independently without exposing artifact identity',
    async (state) => {
      await render(flowState({ status: updateStatus({ state }) }));
      const content = text(renderer!.root);
      expect(content).toContain(en.cpa_updates[state]);
      expect(content).toContain('CPA Engine');
      expect(content).toContain('7.1.0');
      expect(content).not.toContain(originalArtifact);
      expect(renderer!.root.findAllByProps({ 'aria-live': 'polite' }).length).toBeGreaterThan(0);
    }
  );

  it('shows External management without check, prepare, activate, or retry actions', async () => {
    await render(
      flowState({
        status: updateStatus({ mode: 'external', state: 'managed_externally' }),
        intent: updateIntent({ phase: 'activate' }),
        stage: 'not_found',
        unresolved: true,
        canRetry: true,
      })
    );
    const labels = renderer!.root.findAllByType('button').map(text);
    expect(text(renderer!.root)).toContain(en.cpa_updates.managed_externally);
    expect(labels).not.toContain(en.cpa_updates.check);
    expect(labels).not.toContain(en.cpa_updates.prepare_action);
    expect(labels).not.toContain(en.cpa_updates.activate_action);
    expect(labels).not.toContain(en.cpa_updates.retry_same);
  });

  it('requires activation confirmation and supports cancel before submitting', async () => {
    const intent = updateIntent({ client_stage: 'prepared' });
    const flow = flowState({
      intent,
      stage: 'prepared',
      unresolved: true,
      canCheck: false,
      canPrepare: false,
      canActivate: true,
    });
    await render(flow);
    expect(button(en.cpa_updates.check).props.disabled).toBe(true);
    await click(en.cpa_updates.activate_action);
    expect(flow.activate).not.toHaveBeenCalled();
    expect(text(renderer!.root.findByProps({ role: 'dialog' }))).toContain(
      'existing requests may be briefly interrupted'
    );
    await click(en.common.cancel);
    expect(renderer!.root.findAllByProps({ role: 'dialog' })).toHaveLength(0);
    expect(flow.activate).not.toHaveBeenCalled();
    await click(en.cpa_updates.activate_action);
    await click(en.cpa_updates.activate_confirm);
    expect(flow.activate).toHaveBeenCalledExactlyOnceWith(intent);
  });

  it('confirms that forgetting does not cancel Runtime and may lose uncertain correlation', async () => {
    const flow = flowState({
      intent: updateIntent(),
      stage: 'outcome_unknown',
      unresolved: true,
      canPrepare: false,
      canCheck: false,
    });
    await render(flow);
    await click(en.cpa_updates.forget);
    expect(flow.forget).not.toHaveBeenCalled();
    const dialog = text(renderer!.root.findByProps({ role: 'dialog' }));
    expect(dialog).toContain('does not cancel');
    expect(dialog).toContain('lose the link to this uncertain operation');
    await click(en.cpa_updates.forget_confirm);
    expect(flow.forget).toHaveBeenCalledExactlyOnceWith(flow.recoveryRecord);
  });

  it.each(['prepared', 'corrupt'] as const)(
    'binds Forget to the original %s record across same-scope changes',
    async (kind) => {
      const original = {
        raw:
          kind === 'corrupt'
            ? 'invalid record'
            : JSON.stringify(updateIntent({ client_stage: 'prepared' })),
      };
      const flow = flowState({
        intent: kind === 'corrupt' ? null : updateIntent({ client_stage: 'prepared' }),
        recoveryRecord: original,
        unresolved: true,
      });
      await render(flow);
      await click(en.cpa_updates.forget);
      await render({
        ...flow,
        intent: updateIntent({ phase: 'activate' }),
        recoveryRecord: { raw: JSON.stringify(updateIntent({ phase: 'activate' })) },
      });
      await click(en.cpa_updates.forget_confirm);
      expect(flow.forget).toHaveBeenCalledExactlyOnceWith(original);
    }
  );

  it.each(['prepare', 'activate'] as const)(
    'offers explicit same-request replay for missing %s with activation confirmation when needed',
    async (phase) => {
      const intent = updateIntent({ phase });
      const flow = flowState({
        intent,
        stage: 'not_found',
        unresolved: true,
        canCheck: false,
        canPrepare: false,
        canRetry: true,
      });
      await render(flow);
      expect(text(renderer!.root)).toContain(en.cpa_updates.flow.not_found);
      expect(flow.retry).not.toHaveBeenCalled();
      await click(en.cpa_updates.retry_same);
      if (phase === 'activate') {
        expect(flow.retry).not.toHaveBeenCalled();
        expect(renderer!.root.findByProps({ role: 'dialog' })).toBeDefined();
        await click(en.cpa_updates.activate_confirm);
      }
      expect(flow.retry).toHaveBeenCalledExactlyOnceWith(intent);
    }
  );

  it('withholds activation for an uncertain prepare even when the current version matches', async () => {
    const intent = updateIntent();
    await render(
      flowState({
        intent,
        stage: 'outcome_unknown',
        unresolved: true,
        canPrepare: false,
        canCheck: false,
        status: updateStatus({ state: 'up_to_date', current_version: intent.target_version }),
      })
    );
    expect(text(renderer!.root)).toContain(en.cpa_updates.flow.outcome_unknown);
    expect(renderer!.root.findAllByType('button').map(text)).not.toContain(
      en.cpa_updates.activate_action
    );
    expect(text(renderer!.root)).not.toContain(en.cpa_updates.flow.already_applied);
  });

  it('presents already_applied as a distinct terminal result', async () => {
    await render(
      flowState({
        intent: updateIntent({ phase: 'activate' }),
        stage: 'already_applied',
        status: updateStatus({ state: 'up_to_date' }),
        canPrepare: false,
      })
    );
    expect(text(renderer!.root)).toContain(en.cpa_updates.flow.already_applied);
    expect(text(renderer!.root)).not.toContain(en.cpa_updates.flow.succeeded);
  });

  it('closes stale confirmation after auth/service scope changes', async () => {
    const flow = flowState({
      intent: updateIntent({ client_stage: 'prepared' }),
      stage: 'prepared',
      canActivate: true,
      unresolved: true,
    });
    await render(flow);
    await click(en.cpa_updates.activate_action);
    await render(flowState({ scope: 'scope-b' }));
    expect(renderer!.root.findAllByProps({ role: 'dialog' })).toHaveLength(0);
    expect(flow.activate).not.toHaveBeenCalled();
  });

  it('announces blocked storage without displaying backend error messages', async () => {
    await render(
      flowState({
        storageError: 'corrupt',
        canPrepare: false,
        canCheck: false,
        status: updateStatus({ last_error: 'private runtime path and token' }),
      })
    );
    expect(text(renderer!.root.findByProps({ role: 'alert' }))).toBe(
      en.cpa_updates.storage_corrupt
    );
    expect(text(renderer!.root)).not.toContain('private runtime path and token');
  });

  it('has complete, nonempty matching translation keys in all four locales', () => {
    const flatten = (source: Record<string, unknown>, prefix = ''): Record<string, string> =>
      Object.fromEntries(
        Object.entries(source).flatMap(([key, value]) =>
          typeof value === 'string'
            ? [[prefix + key, value]]
            : Object.entries(flatten(value as Record<string, unknown>, prefix + key + '.'))
        )
      );
    const reference = flatten(en.cpa_updates);
    for (const locale of [zhCN, zhTW, en, ru]) {
      const entries = flatten(locale.cpa_updates);
      expect(Object.keys(entries).sort()).toEqual(Object.keys(reference).sort());
      expect(Object.values(entries).every((value) => value.trim().length > 0)).toBe(true);
      expect(locale.manager_updates.product).toBe('CPAMP');
      expect(locale.manager_updates.title).toBeTruthy();
    }
  });
});
