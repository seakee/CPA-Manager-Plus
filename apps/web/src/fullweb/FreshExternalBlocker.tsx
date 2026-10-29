import { FullWebIcon } from './FullWebIcon';
import { externalWorkspaceText as x, versionSupportText } from '../i18n/freshExternal';
import type { ExternalBlocker } from './externalState';

/**
 * One banner for every typed External state. The action is the state's own: a
 * refused key offers a rebind, a moved target offers no write action at all, and
 * an unsupported version keeps reads and only names the version. A page must
 * never offer a retry the server would refuse.
 */
export function FreshExternalBlocker({
  state,
  onRebind,
  onReload,
  onDismiss,
}: {
  state: ExternalBlocker;
  onRebind?: () => void;
  onReload?: () => void;
  onDismiss?: () => void;
}) {
  const title =
    state.kind === 'key_rejected'
      ? x('blockerKeyRejectedTitle')
      : state.kind === 'target_moved'
        ? x('blockerTargetMovedTitle')
        : state.kind === 'version_unsupported'
          ? x('blockerVersionUnsupportedTitle')
          : state.kind === 'legacy_write_replaced'
            ? x('blockerLegacyReplacedTitle')
            : state.kind === 'rebind_refused'
              ? x('rebindRefusedHelp')
              : state.kind === 'rebind_stale'
                ? x('rebindStaleHelp')
                : x('rebindUnavailableHelp');
  const help =
    state.kind === 'key_rejected'
      ? state.detail || x('blockerKeyRejectedHelp')
      : state.kind === 'target_moved'
        ? x('blockerTargetMovedHelp')
        : state.kind === 'version_unsupported'
          ? x('blockerVersionUnsupportedHelp')
          : state.kind === 'legacy_write_replaced'
            ? x('blockerLegacyReplacedHelp')
            : '';
  return (
    <section
      className="key-notice key-notice-warning fresh-external-blocker"
      data-testid={`external-blocker-${state.kind}`}
      data-blocker={state.kind}
      role="alert"
    >
      <FullWebIcon name="alert" />
      <div className="fresh-external-blocker-body">
        <strong>{title}</strong>
        {help && <p>{help}</p>}
        {state.kind === 'version_unsupported' && (
          <p className="fresh-external-blocker-version" data-testid="external-blocker-version">
            {x('blockerVersionLabel')}: {state.version || x('blockerVersionUnknown')} ·{' '}
            {versionSupportText(state.support)}
          </p>
        )}
      </div>
      <div className="fresh-external-blocker-actions">
        {state.kind === 'key_rejected' && onRebind && (
          <button
            className="btn btn-primary btn-sm"
            onClick={onRebind}
            data-testid="external-blocker-rebind"
          >
            {x('rebindAction')}
          </button>
        )}
        {state.kind === 'rebind_stale' && onReload && (
          <button
            className="btn btn-secondary btn-sm"
            onClick={onReload}
            data-testid="external-blocker-reload"
          >
            {x('reload')}
          </button>
        )}
        {onDismiss && (
          <button
            className="btn btn-secondary btn-sm"
            onClick={onDismiss}
            data-testid="external-blocker-dismiss"
          >
            {x('blockerDismiss')}
          </button>
        )}
      </div>
    </section>
  );
}
