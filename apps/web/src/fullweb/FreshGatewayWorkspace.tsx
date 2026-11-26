import {
  Component,
  Suspense,
  lazy,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { UNSAFE_LocationContext } from 'react-router-dom';
import { FloatingActionDock } from '@/components/common/FloatingActionDock';
import { PageControlBar } from '@/components/common/PageControlBar';
import { SectionActionFooter } from '@/components/common/SectionActionFooter';
import { PluginRuntimeSwitch } from '@/features/plugins/PluginRuntimeSwitch';
import {
  PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS,
  notifyPluginResourcesChanged,
} from '@/features/plugins/pluginResources';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useConfigStore } from '@/stores';
import { isRecord } from '../utils/helpers';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs, type SegmentedTabItem } from '../components/ui/SegmentedTabs';
import { IconRefreshCw, IconSearch } from '../components/ui/icons';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r } from '../i18n/configurationReference';
import type { PublicApiClient } from './publicApi';
import { PublicApiError } from './publicApi';
import {
  COMMON_GATEWAY_PATHS,
  GATEWAY_TABS,
  UPSTREAM_PROVIDERS,
  gatewayFamilies,
  gatewayGroups,
  type GatewayFamilyMeta,
  type GatewayFieldMeta,
  type GatewayGroupMeta,
  type GatewayTabKey,
  type UpstreamProviderKey,
} from './gatewayCatalog';
import {
  allowsDefaultEdit,
  boolDraft,
  boolState,
  draftEquals,
  draftFor,
  draftIssue,
  editFor,
  isInheritable,
  limitIssue,
  newGatewayRequestId,
  readGatewayOperation,
  readGatewaySource,
  readGatewayView,
  saveGatewayFamily,
  saveGatewayGroup,
  saveGatewaySource,
  type GatewayDraft,
  type GatewayDraftIssue,
  type GatewayEdit,
  type GatewayFamilyView,
  type GatewayFieldView,
  type GatewayGroupView,
  type GatewayOperation,
  type GatewayPresence,
  type GatewaySourceView,
  type GatewayView,
} from './gatewayConfigClient';
import { GatewayRuleEditor } from './GatewayRuleEditor';
import { ruleSummary } from './gatewayRuleForm';
import { FreshExternalBlocker } from './FreshExternalBlocker';
import { blockerOf, type ExternalBlocker } from './externalState';
import type { GatewaySourceEditorHandle, GatewaySourceEditorProps } from './GatewaySourceEditor';

type GatewaySourceEditorModule = { default: ComponentType<GatewaySourceEditorProps> };

const loadGatewaySourceEditor = (): Promise<GatewaySourceEditorModule> =>
  import('./GatewaySourceEditor');
const EDITOR_MODE_KEY = 'cpamp_gateway_editor_mode';

interface SourceEditorErrorBoundaryProps {
  resetKey: number;
  fallbackMessage: string;
  retryLabel: string;
  onRetry: () => void;
  onError?: () => void;
  children: ReactNode;
}

class SourceEditorErrorBoundary extends Component<
  SourceEditorErrorBoundaryProps,
  { hasError: boolean }
> {
  state = { hasError: false };

  static getDerivedStateFromError(): { hasError: boolean } {
    return { hasError: true };
  }

  componentDidCatch(): void {
    this.props.onError?.();
  }

  componentDidUpdate(prevProps: SourceEditorErrorBoundaryProps): void {
    if (this.state.hasError && prevProps.resetKey !== this.props.resetKey) {
      this.setState({ hasError: false });
    }
  }

  render(): ReactNode {
    if (!this.state.hasError) {
      return this.props.children;
    }
    return (
      <div className="alert alert-warn" role="alert" data-testid="gateway-source-editor-error">
        <p>{this.props.fallbackMessage}</p>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          onClick={() => {
            this.setState({ hasError: false });
            this.props.onRetry();
          }}
        >
          {this.props.retryLabel}
        </button>
      </div>
    );
  }
}

function readSavedEditorMode(): 'visual' | 'source' {
  try {
    if (typeof window !== 'undefined' && window.localStorage?.getItem(EDITOR_MODE_KEY) === 'source') {
      return 'source';
    }
  } catch {
    // ignore storage errors
  }
  return 'visual';
}

interface DiffLine {
  kind: 'same' | 'add' | 'del';
  text: string;
}

function computeLineDiff(beforeText: string, afterText: string): DiffLine[] {
  const a = beforeText.split('\n');
  const b = afterText.split('\n');
  if (a.length * b.length > 250000) {
    return [
      ...a.map((text): DiffLine => ({ kind: 'del', text })),
      ...b.map((text): DiffLine => ({ kind: 'add', text })),
    ];
  }
  const m = a.length;
  const n = b.length;
  const dp = Array.from({ length: m + 1 }, () => new Uint32Array(n + 1));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? 1 + dp[i + 1][j + 1] : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      out.push({ kind: 'same', text: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ kind: 'del', text: a[i] });
      i++;
    } else {
      out.push({ kind: 'add', text: b[j] });
      j++;
    }
  }
  while (i < m) out.push({ kind: 'del', text: a[i++] });
  while (j < n) out.push({ kind: 'add', text: b[j++] });
  return out;
}

// The gateway configuration page. One write is one page group: the page collects
// every edited leaf of a group into a single PATCH, restores a leaf to CPA's own
// default with one DELETE, writes a rule family with one PUT, and never sends a
// second write for an unresolved one.

// A named code that only says "the outcome is not known". A network failure, the
// client abort and a code-less 5xx are treated the same way by classification
// below, because none of them can prove the write did not land.
const GATEWAY_SAVE_UNKNOWN_CODES: Record<string, true> = {
  configuration_unavailable: true,
  config_authority_unavailable: true,
};

// The journal key of one payload rule family. Every family shares CPA's `rules`
// group journal, but a page error belongs to the family card the user saved.
const familyScope = (family: string) => `rules:${family}`;

/**
 * Whether a failed save may still have reached CPA. A network failure, the 15 s
 * client abort (publicApi.ts:104) and a 5xx without a recognised code all land
 * here: the page must neither claim the document is unchanged nor resend.
 */
function isUnknownResult(error: unknown): boolean {
  if (!(error instanceof PublicApiError)) return true;
  if (error.kind === 'network') return true;
  if (error.code && GATEWAY_SAVE_UNKNOWN_CODES[error.code]) return true;
  return error.kind === 'http' && !error.code && (error.status ?? 0) >= 500;
}
interface KvDraftRow {
  id: string;
  key: string;
  value: string;
}

function kvRowsIssue(rows: KvDraftRow[] | undefined): 'duplicate' | 'keyRequired' | null {
  if (!rows) return null;
  const seen = new Set<string>();
  for (const row of rows) {
    const trimmedKey = row.key.trim();
    if (!trimmedKey) {
      if (row.value !== '') return 'keyRequired';
      continue;
    }
    if (seen.has(trimmedKey)) return 'duplicate';
    seen.add(trimmedKey);
  }
  return null;
}

function draftIssueMessage(issue: GatewayDraftIssue): string {
  switch (issue) {
    case 'required':
      return c('valueRequired');
    case 'integer':
      return c('valueInteger');
    case 'duplicate':
      return c('kvDuplicateKey');
    case 'keyRequired':
      return c('kvKeyRequired');
  }
}

function toExternalBlocker(error: unknown): ExternalBlocker | null {
  if (!(error instanceof PublicApiError)) return null;
  const typed = blockerOf(error.code);
  if (!typed) return null;
  return typed.kind === 'key_rejected' && error.detail
    ? { ...typed, detail: error.detail }
    : typed;
}

function initialKvRows(path: string, value: unknown): KvDraftRow[] {
  if (!isRecord(value)) return [];
  return Object.entries(value as Record<string, unknown>).map(([k, v], idx) => ({
    id: `kv-seed-${path}-${idx}`,
    key: k,
    value: typeof v === 'string' ? v : String(v ?? ''),
  }));
}


export function FreshGatewayWorkspace({
  client,
  credential,
  onUnauthorized,
  onPending,
  onDirtyChange,
  onRebind,
  reloadToken = 0,
  loadSourceEditor = loadGatewaySourceEditor,
  visible,
}: {
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  onDirtyChange?: (dirty: boolean) => void;
  onRebind?: () => void;
  /** Bumped by the shell after a successful rebind to re-read this page. */
  reloadToken?: number;
  loadSourceEditor?: () => Promise<GatewaySourceEditorModule>;
  visible?: boolean;
}) {
  // Subscribe to language changes just like the original Console pages.
  const { i18n } = useTranslation();
  const language = i18n.resolvedLanguage;
  const locationContext = useContext(UNSAFE_LocationContext);
  const isRouteVisible =
    visible ??
    (locationContext
      ? /(?:^|\/)(?:config|gateway)\/?$/.test(locationContext.location.pathname)
      : true);
  const [view, setView] = useState<GatewayView | null>(null);
  const [loading, setLoading] = useState(true);
  const [visualLoadFailed, setVisualLoadFailed] = useState(false);
  const [sourceLoadFailed, setSourceLoadFailed] = useState(false);
  // A typed External blocker from the load, kept separate from the generic
  // load failure: the page names the state and offers its own way out instead
  // of a bare "could not read".
  const [visualBlocker, setVisualBlocker] = useState<ExternalBlocker | null>(null);
  const [sourceBlocker, setSourceBlocker] = useState<ExternalBlocker | null>(null);
  const [writeBlocker, setWriteBlocker] = useState<ExternalBlocker | null>(null);
  const [currentTab, setCurrentTab] = useState<GatewayTabKey>('common');
  const [activeUpstreamProvider, setActiveUpstreamProvider] = useState<UpstreamProviderKey>('codex');
  const [searchText, setSearchText] = useState('');
  const [changedOnly, setChangedOnly] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, GatewayDraft>>({});
  const [issues, setIssues] = useState<Record<string, string>>({});
  const [kvRowsByPath, setKvRowsByPath] = useState<Record<string, KvDraftRow[]>>({});
  const [groupErrors, setGroupErrors] = useState<Record<string, string>>({});
  const [familyDrafts, setFamilyDrafts] = useState<Record<string, unknown[]>>({});
  const [saving, setSaving] = useState('');
  const [reconciling, setReconciling] = useState('');
  const [conflictGroup, setConflictGroup] = useState('');
  // The baseline the user was editing against, captured before a conflict
  // re-read replaces it. Without it the page can only show new → draft.
  const [conflictSnapshot, setConflictSnapshot] = useState<{
    scope: string;
    fields: Record<string, { presence: GatewayPresence; value: unknown }>;
  } | null>(null);
  const [diffOpen, setDiffOpen] = useState(false);
  const [discardGroup, setDiscardGroup] = useState<string | null>(null);
  const [discardAll, setDiscardAll] = useState(false);
  const [ruleFamily, setRuleFamily] = useState<GatewayFamilyMeta | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Record<string, boolean>>({});
  const [savingAll, setSavingAll] = useState(false);
  const [editorMode, setEditorMode] = useState<'visual' | 'source'>(readSavedEditorMode);
  const [sourceView, setSourceView] = useState<GatewaySourceView | null>(null);
  const [sourceDraft, setSourceDraft] = useState<string | null>(null);
  const [sourceSyncKey, setSourceSyncKey] = useState(0);
  const [sourceEditorRetryKey, setSourceEditorRetryKey] = useState(0);
  const [sourceLoading, setSourceLoading] = useState(false);
  const sourceEditorFailedRef = useRef(false);
  const LazyGatewaySourceEditor = useMemo(() => {
    void sourceEditorRetryKey;
    return lazy(async () => {
      const mod = await loadSourceEditor();
      const ResolvedEditor = mod.default;
      function SourceEditorLoadedWrapper(props: GatewaySourceEditorProps) {
        useEffect(() => {
          sourceEditorFailedRef.current = false;
        }, []);
        return <ResolvedEditor {...props} />;
      }
      return { default: SourceEditorLoadedWrapper };
    });
  }, [loadSourceEditor, sourceEditorRetryKey]);
  const [sourceSearchText, setSourceSearchText] = useState('');
  const [sourceSearchStats, setSourceSearchStats] = useState<{ current: number; total: number }>({
    current: 0,
    total: 0,
  });
  const [sourceDiffOpen, setSourceDiffOpen] = useState(false);
  const [modeSwitchError, setModeSwitchError] = useState('');
  const sourceEditorRef = useRef<GatewaySourceEditorHandle | null>(null);
  const issued = useRef<Set<string>>(new Set());
  const pendingWrite = useRef<Record<string, string>>({});
  const [pendingUnknownScopes, setPendingUnknownScopes] = useState<Record<string, string>>({});
  const mountedRef = useRef(true);
  const visualLoadSeqRef = useRef(0);
  const sourceLoadSeqRef = useRef(0);
  const visualStaleRef = useRef(false);
  const pendingUnknownScopesRef = useRef<Record<string, string>>({});
  pendingUnknownScopesRef.current = pendingUnknownScopes;
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const blocker = writeBlocker ?? (editorMode === 'source' ? sourceBlocker : visualBlocker);
  const loadFailed = editorMode === 'source' ? sourceLoadFailed : visualLoadFailed;
  const nextKvRowId = useRef(0);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const groups = useMemo(() => gatewayGroups(), [language]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const families = useMemo(() => gatewayFamilies(), [language]);
  const handleError = useCallback(
    (error: unknown) => {
      if (error instanceof PublicApiError && error.kind === 'authentication') onUnauthorized();
    },
    [onUnauthorized]
  );

  const clearConflictState = useCallback(() => {
    setConflictGroup('');
    setConflictSnapshot(null);
  }, []);
  const clearPendingUnknownScope = useCallback((scope: string) => {
    delete pendingWrite.current[scope];
    if (pendingUnknownScopesRef.current[scope]) {
      const next = { ...pendingUnknownScopesRef.current };
      delete next[scope];
      pendingUnknownScopesRef.current = next;
    }
    setPendingUnknownScopes((current) => {
      if (!current[scope]) return current;
      const next = { ...current };
      delete next[scope];
      return next;
    });
  }, []);

  const markPendingUnknownScope = useCallback((scope: string, requestId: string) => {
    if (!requestId) return;
    pendingWrite.current[scope] = requestId;
    if (pendingUnknownScopesRef.current[scope] !== requestId) {
      pendingUnknownScopesRef.current = { ...pendingUnknownScopesRef.current, [scope]: requestId };
    }
    setPendingUnknownScopes((current) =>
      current[scope] === requestId ? current : { ...current, [scope]: requestId }
    );
  }, []);

  const load = useCallback(
    async (options: { keepDrafts?: boolean } = {}) => {
      const seq = ++visualLoadSeqRef.current;
      setLoading(true);
      try {
        const next = await readGatewayView(client, { kind: 'admin', value: credential });
        if (!mountedRef.current || visualLoadSeqRef.current !== seq) return;
        visualStaleRef.current = false;
        setView(next);
        setVisualBlocker(null);
        setWriteBlocker(null);
        setVisualLoadFailed(false);
        const hasAnyVisualUnknown =
          (next.groups ?? []).some((g) => g.operation?.state === 'unknown') ||
          (next.families ?? []).some((f) => f.operation?.state === 'unknown');
        const nextPendingScopes = { ...pendingUnknownScopesRef.current };
        let pendingChanged = false;
        for (const [scope, pendingId] of Object.entries(nextPendingScopes)) {
          if (scope === 'source') continue;
          const familyView = scope.startsWith('rules:')
            ? next.families?.find((f) => familyScope(f.family) === scope)
            : undefined;
          const op = scope.startsWith('rules:')
            ? familyView?.operation
            : next.groups?.find((g) => g.id === scope)?.operation;
          const matchesTarget =
            !scope.startsWith('rules:') ||
            !op?.prefix ||
            !familyView?.path ||
            op.prefix === familyView.path;
          if (op?.state === 'unknown' && op.requestId && matchesTarget) {
            if (nextPendingScopes[scope] !== op.requestId) {
              nextPendingScopes[scope] = op.requestId;
              pendingWrite.current[scope] = op.requestId;
              pendingChanged = true;
            }
            continue;
          }
          if (op && op.requestId === pendingId && op.state !== 'unknown') {
            delete pendingWrite.current[scope];
            delete nextPendingScopes[scope];
            pendingChanged = true;
          }
        }
        if (pendingChanged) {
          pendingUnknownScopesRef.current = nextPendingScopes;
          setPendingUnknownScopes(nextPendingScopes);
        }
        if (!options.keepDrafts) {
          setDrafts({});
          setIssues({});
          setGroupErrors({});
          setFamilyDrafts({});
          setKvRowsByPath({});
          clearConflictState();
        } else {
          const blockedNotice = c('reasonUnknownOriginal');
          const unknownNotice = c('groupSaveUnknown');
          setGroupErrors((current) => {
            let changed = false;
            const copy = { ...current };
            for (const [scope, message] of Object.entries(copy)) {
              if (scope === 'source') continue;
              if (message === blockedNotice) {
                if (!hasAnyVisualUnknown && !nextPendingScopes[scope]) {
                  delete copy[scope];
                  changed = true;
                }
                continue;
              }
              if (message === unknownNotice) {
                const familyView = scope.startsWith('rules:')
                  ? next.families?.find((f) => familyScope(f.family) === scope)
                  : undefined;
                const op = scope.startsWith('rules:')
                  ? familyView?.operation
                  : next.groups?.find((g) => g.id === scope)?.operation;
                const hasOwnUnknown =
                  op?.state === 'unknown' &&
                  (!scope.startsWith('rules:') ||
                    !op.prefix ||
                    !familyView?.path ||
                    op.prefix === familyView.path);
                if (!hasOwnUnknown && !nextPendingScopes[scope]) {
                  delete copy[scope];
                  changed = true;
                }
              }
            }
            return changed ? copy : current;
          });
        }
      } catch (error) {
        if (!mountedRef.current || visualLoadSeqRef.current !== seq) return;
        handleError(error);
        // A typed External code is the page's own contract: it names the state
        // and its way out, so the generic failure notice stays hidden.
        setVisualBlocker(toExternalBlocker(error));
        setVisualLoadFailed(true);
      } finally {
        if (mountedRef.current && visualLoadSeqRef.current === seq) {
          setLoading(false);
        }
      }
    },
    [clearConflictState, client, credential, handleError]
  );

  useEffect(() => {
    void load();
  }, [load, reloadToken]);
  const loadSource = useCallback(
    async (options: { keepDraft?: boolean } = {}) => {
      const seq = ++sourceLoadSeqRef.current;
      setSourceLoading(true);
      try {
        const next = await readGatewaySource(client, { kind: 'admin', value: credential });
        if (!mountedRef.current || sourceLoadSeqRef.current !== seq) return;
        setSourceView(next);
        setSourceBlocker(null);
        setWriteBlocker(null);
        setSourceLoadFailed(false);
        if (
          next.operation?.state === 'unknown' &&
          next.operation.requestId &&
          next.operation.group === 'source' &&
          (pendingUnknownScopesRef.current.source || pendingWrite.current.source)
        ) {
          markPendingUnknownScope('source', next.operation.requestId);
        } else if (
          next.operation &&
          next.operation.requestId === (pendingWrite.current.source ?? '') &&
          next.operation.state !== 'unknown'
        ) {
          clearPendingUnknownScope('source');
        }
        if (!options.keepDraft) {
          setSourceDraft(null);
          setSourceSyncKey((n) => n + 1);
          setGroupErrors((current) => {
            if (!current.source) return current;
            const copy = { ...current };
            delete copy.source;
            return copy;
          });
          clearConflictState();
        } else if (next.operation?.state !== 'unknown') {
          const blockedNotice = c('reasonUnknownOriginal');
          setGroupErrors((current) => {
            if (current.source !== blockedNotice) return current;
            const copy = { ...current };
            delete copy.source;
            return copy;
          });
        }
      } catch (error) {
        if (!mountedRef.current || sourceLoadSeqRef.current !== seq) return;
        handleError(error);
        setSourceBlocker(toExternalBlocker(error));
        setSourceLoadFailed(true);
      } finally {
        if (mountedRef.current && sourceLoadSeqRef.current === seq) {
          setSourceLoading(false);
        }
      }
    },
    [clearConflictState, clearPendingUnknownScope, client, credential, handleError, markPendingUnknownScope]
  );

  useEffect(() => {
    if (editorMode === 'source') {
      void loadSource();
    }
  }, [editorMode, loadSource, reloadToken]);

  const fieldViews = useMemo(() => {
    const index = new Map<string, GatewayFieldView>();
    for (const field of view?.fields ?? []) index.set(field.path, field);
    return index;
  }, [view]);

  const groupViews = useMemo(() => {
    const index = new Map<string, GatewayGroupView>();
    for (const group of view?.groups ?? []) index.set(group.id, group);
    return index;
  }, [view]);

  const familyViews = useMemo(() => {
    const index = new Map<string, GatewayFamilyView>();
    for (const family of view?.families ?? []) index.set(family.family, family);
    return index;
  }, [view]);

  const resolveDraftIssue = useCallback(
    (field: GatewayFieldView, draft: GatewayDraft): GatewayDraftIssue | null => {
      if (field.type === 'kv-map' && draft.action !== 'default') {
        const rowIssue = kvRowsIssue(kvRowsByPath[field.path]);
        if (rowIssue) return rowIssue;
      }
      return draftIssue(field, draft);
    },
    [kvRowsByPath]
  );

  const dirtyEdits = useMemo(() => {
    const edits: GatewayEdit[] = [];
    for (const field of view?.fields ?? []) {
      const draft = drafts[field.path];
      if (!draft) continue;
      const hasKvIssue =
        field.type === 'kv-map' &&
        draft.action !== 'default' &&
        kvRowsIssue(kvRowsByPath[field.path]) !== null;
      if (!hasKvIssue && draftEquals(field, draft)) continue;
      edits.push({ ...editFor(field, draft), path: field.path });
    }
    return edits;
  }, [drafts, kvRowsByPath, view]);

  const dirtyByGroup = useMemo(() => {
    const index = new Map<string, GatewayEdit[]>();
    for (const edit of dirtyEdits) {
      const field = fieldViews.get(edit.path);
      if (!field) continue;
      const list = index.get(field.group) ?? [];
      list.push(edit);
      index.set(field.group, list);
    }
    return index;
  }, [dirtyEdits, fieldViews]);

  const dirtyFamilies = useMemo(() => {
    const index = new Map<string, unknown[]>();
    for (const family of families) {
      const draft = familyDrafts[family.family];
      if (!draft) continue;
      const server = familyViews.get(family.family)?.rules ?? [];
      if (JSON.stringify(draft) === JSON.stringify(server)) continue;
      index.set(family.family, draft);
    }
    return index;
  }, [families, familyDrafts, familyViews]);

  const dirtyCount = dirtyEdits.length + dirtyFamilies.size;

  // The discard dialog names how many drafts it throws away: every dirty leaf
  // and rule family, or just the group the user asked to reset.
  const discardCount = discardAll
    ? dirtyCount
    : (dirtyByGroup.get(discardGroup ?? '')?.length ?? 0);

  // One badge per tab: validation errors win over a plain unsaved count, so a
  // tab that cannot be saved never looks like a tab that merely has drafts.
  const tabMetrics = useMemo(() => {
    const metrics: Partial<Record<GatewayTabKey, { dirty: number; errors: number }>> = {};
    const bump = (key: GatewayTabKey, dirty: number, errors: number) => {
      const current = metrics[key] ?? { dirty: 0, errors: 0 };
      metrics[key] = { dirty: current.dirty + dirty, errors: current.errors + errors };
    };
    for (const group of groups) {
      const dirty = dirtyByGroup.get(group.id)?.length ?? 0;
      const errors = (dirtyByGroup.get(group.id) ?? []).filter((edit) => {
        const field = fieldViews.get(edit.path);
        const draft = drafts[edit.path];
        return !!field && !!draft && resolveDraftIssue(field, draft) !== null;
      }).length;
      if (!dirty && !errors) continue;
      bump(group.tab, dirty, errors);
    }
    for (const field of view?.fields ?? []) {
      const draft = drafts[field.path];
      if (!draft) continue;
      const issue = resolveDraftIssue(field, draft);
      if (draftEquals(field, draft) && issue === null) continue;
      if (COMMON_GATEWAY_PATHS[field.path] === true) bump('common', 1, issue ? 1 : 0);
    }
    for (const family of families) {
      if (!dirtyFamilies.has(family.family)) continue;
      bump('payload', 1, 0);
    }
    // A conflict is an error: the group cannot be saved again as it stands.
    const errorScopes = new Set(Object.keys(groupErrors));
    for (const [scope, pendingId] of Object.entries(pendingUnknownScopes)) {
      if (scope === 'source') continue;
      const op = scope.startsWith('rules:')
        ? view?.families?.find((f) => familyScope(f.family) === scope)?.operation
        : view?.groups?.find((g) => g.id === scope)?.operation;
      if (!op || op.requestId !== pendingId || op.state === 'unknown') {
        errorScopes.add(scope);
      }
    }
    for (const scope of errorScopes) {
      if (scope === 'source') continue;
      if (scope.startsWith('rules:') || scope === 'rules') {
        bump('payload', 0, 1);
        continue;
      }
      const owning = groups.find((group) => group.id === scope);
      bump(owning?.tab ?? 'common', 0, 1);
    }
    return metrics;
  }, [drafts, dirtyByGroup, dirtyFamilies, families, fieldViews, groups, groupErrors, pendingUnknownScopes, resolveDraftIssue, view]);
  const upstreamMetrics = useMemo(() => {
    const metrics: Record<UpstreamProviderKey, { dirty: number; errors: number }> = {
      codex: { dirty: 0, errors: 0 },
      claude: { dirty: 0, errors: 0 },
      antigravity: { dirty: 0, errors: 0 },
      devin: { dirty: 0, errors: 0 },
      other: { dirty: 0, errors: 0 },
      oauth: { dirty: 0, errors: 0 },
    };
    for (const group of groups) {
      if (group.tab !== 'upstream' || !group.provider) continue;
      const edits = dirtyByGroup.get(group.id) ?? [];
      const fieldErrs = edits.filter((edit) => {
        const field = fieldViews.get(edit.path);
        const draft = drafts[edit.path];
        return !!field && !!draft && resolveDraftIssue(field, draft) !== null;
      }).length;
      const op = view?.groups?.find((g) => g.id === group.id)?.operation;
      const pendingId = pendingUnknownScopes[group.id];
      const pendingUnknown =
        Boolean(pendingId) && (!op || op.requestId !== pendingId || op.state === 'unknown');
      const groupErr = groupErrors[group.id] || pendingUnknown ? 1 : 0;
      metrics[group.provider].dirty += edits.length;
      metrics[group.provider].errors += fieldErrs + groupErr;
    }
    return metrics;
  }, [dirtyByGroup, drafts, fieldViews, groupErrors, groups, pendingUnknownScopes, resolveDraftIssue, view]);
  const sourceValue = sourceDraft ?? sourceView?.yaml ?? '';
  const sourceDirty = sourceDraft !== null && sourceDraft !== (sourceView?.yaml ?? '');

  useEffect(() => {
    onDirtyChange?.(dirtyCount > 0 || sourceDirty);
  }, [dirtyCount, onDirtyChange, sourceDirty]);

  const triggerWorkspaceReload = useCallback(() => {
    void load();
    if (editorMode === 'source') {
      if (sourceEditorFailedRef.current) {
        sourceEditorFailedRef.current = false;
        setSourceEditorRetryKey((n) => n + 1);
      }
      void loadSource();
    }
  }, [editorMode, load, loadSource]);

  useHeaderRefresh(
    triggerWorkspaceReload,
    isRouteVisible &&
      !loading &&
      !sourceLoading &&
      saving === '' &&
      !savingAll &&
      reconciling === '' &&
      dirtyCount === 0 &&
      !sourceDirty
  );

  // A journaled unknown is still an unanswered write, so it keeps the exit lock
  // exactly as the per-field editor's own unknown result did: leaving to a
  // signed-out page would abandon an operation nobody can reconcile any more.
  const unresolvedOperation = [
    ...groups.map((group) => group.id),
    ...families.map((family) => familyScope(family.family)),
    'source',
  ].some((scope) => needsReconcile(scope) && !!reconcileTarget(scope));

  useEffect(() => {
    onPending(!!saving || !!reconciling || unresolvedOperation);
  }, [onPending, reconciling, saving, unresolvedOperation]);

  useEffect(() => {
    if (
      (!dirtyCount && !sourceDirty) ||
      typeof window === 'undefined' ||
      typeof window.addEventListener !== 'function'
    )
      return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirtyCount, sourceDirty]);

  const metaOf = useCallback(
    (path: string): GatewayFieldMeta | undefined => {
      for (const group of groups) {
        const found = group.fields.find((field) => field.path === path);
        if (found) return found;
      }
      return undefined;
    },
    [groups]
  );

  function formatted(presence: string, path: string, value: unknown): string {
    if (presence === 'missing') return c('valueMissing');
    if (presence === 'null') return c('valueNull');
    if (presence === 'unknown') return c('valueUnknown');
    const meta = metaOf(path);
    if (typeof value === 'boolean') return value ? c('valueOn') : c('valueOff');
    const option = meta?.options?.find((entry) => String(entry.value) === String(value));
    if (option) return r(option.labelKey);
    if (Array.isArray(value)) {
      if (value.length === 0) return c('listEmpty');
      return value
        .map((item) =>
          typeof item === 'string'
            ? item
            : isRecord(item) && typeof item.pattern === 'string'
              ? `${item.pattern} → ${String(item.replacement ?? '')}`
              : JSON.stringify(item)
        )
        .join(', ');
    }
    if (isRecord(value)) {
      const entries = Object.entries(value);
      if (entries.length === 0) return c('listEmpty');
      return entries.map(([k, v]) => `${k} → ${String(v ?? '')}`).join(', ');
    }
    if (typeof value === 'string' && value === '') return c('valueEmpty');
    return String(value);
  }
  function setDraft(path: string, draft: GatewayDraft) {
    setDrafts((current) => ({ ...current, [path]: draft }));
    setIssues((current) => {
      if (!current[path]) return current;
      const next = { ...current };
      delete next[path];
      return next;
    });
  }


  function clearGroupDrafts(groupId: string | null) {
    const paths = new Set(
      groupId === null
        ? dirtyEdits.map((edit) => edit.path)
        : (dirtyByGroup.get(groupId) ?? []).map((edit) => edit.path)
    );
    setDrafts((current) => {
      const next = { ...current };
      for (const path of paths) delete next[path];
      return next;
    });
    if (groupId === null) {
      setIssues({});
      setKvRowsByPath({});
      setFamilyDrafts({});
      setGroupErrors({});
      clearConflictState();
    } else {
      const groupFieldPaths = new Set([
        ...paths,
        ...(groups.find((entry) => entry.id === groupId)?.fields.map((field) => field.path) ?? []),
        ...(view?.fields.filter((field) => field.group === groupId).map((field) => field.path) ?? []),
      ]);
      setIssues((current) => {
        let changed = false;
        const next = { ...current };
        for (const path of groupFieldPaths) {
          if (path in next) {
            delete next[path];
            changed = true;
          }
        }
        return changed ? next : current;
      });
      setKvRowsByPath((current) => {
        let changed = false;
        const next = { ...current };
        for (const path of groupFieldPaths) {
          if (path in next) {
            delete next[path];
            changed = true;
          }
        }
        return changed ? next : current;
      });
      setGroupErrors((current) => {
        const next = { ...current };
        delete next[groupId];
        return next;
      });
      if (conflictGroup === groupId || conflictSnapshot?.scope === groupId) {
        clearConflictState();
      }
    }
  }

  function clearFamilyDraft(family: string) {
    setFamilyDrafts((current) => {
      const next = { ...current };
      delete next[family];
      return next;
    });
    setGroupErrors((current) => {
      const next = { ...current };
      delete next[familyScope(family)];
      return next;
    });
    const scope = familyScope(family);
    if (
      conflictGroup === scope ||
      conflictGroup === family ||
      conflictSnapshot?.scope === scope ||
      conflictSnapshot?.scope === family
    ) {
      clearConflictState();
    }
  }

  function clearScopeDrafts(scope: string | null) {
    if (scope === null) {
      clearGroupDrafts(null);
      setSourceDraft(null);
      setSourceSyncKey((n) => n + 1);
      return;
    }
    if (scope === 'source') {
      setSourceDraft(null);
      setSourceSyncKey((n) => n + 1);
      setGroupErrors((current) => {
        if (!current.source) return current;
        const next = { ...current };
        delete next.source;
        return next;
      });
      if (conflictGroup === 'source' || conflictSnapshot?.scope === 'source') {
        clearConflictState();
      }
      return;
    }
    if (scope.startsWith('rules:')) {
      clearFamilyDraft(scope.slice('rules:'.length));
      return;
    }
    clearGroupDrafts(scope);
  }

  /**
   * The operation the Manager journaled for one scope. It survives a page
   * reload, which is why reconcile never depends on what this tab remembers.
   */
  function scopeOperation(scope: string): GatewayOperation | undefined {
    if (scope === 'source') return sourceView?.operation;
    if (scope.startsWith('rules:')) return familyViews.get(scope.slice('rules:'.length))?.operation;
    return groupViews.get(scope)?.operation;
  }

  function hasPendingUnknownWrite(scope: string): boolean {
    const pending = pendingUnknownScopes[scope];
    if (!pending) return false;
    const operation = scopeOperation(scope);
    if (operation && operation.requestId === pending && operation.state !== 'unknown') {
      return false;
    }
    return pending !== operation?.requestId;
  }

  /** The ID to reconcile with: an active unknown journal entry outranks tab memory. */
  function reconcileTarget(scope: string): string {
    const operation = scopeOperation(scope);
    if (operation && operation.state === 'unknown' && operation.requestId) {
      return operation.requestId;
    }
    if (hasPendingUnknownWrite(scope)) {
      return pendingUnknownScopes[scope] ?? pendingWrite.current[scope] ?? '';
    }
    return (
      operation?.requestId ??
      pendingUnknownScopes[scope] ??
      pendingWrite.current[scope] ??
      ''
    );
  }

  /**
   * Capture the observations this page was editing against, before a conflict
   * re-read overwrites them, so the conflict view can show old and new.
   */
  function rememberConflictBaseline(scope: string) {
    const fields: Record<string, { presence: GatewayPresence; value: unknown }> = {};
    if (scope !== 'source') {
      for (const field of view?.fields ?? []) {
        fields[field.path] = { presence: field.presence, value: field.value };
      }
    }
    setConflictSnapshot({ scope, fields });
  }

  function needsReconcile(scope: string): boolean {
    // A journaled operation outranks this tab's message: once the re-read shows
    // it settled, the button goes away even if the old warning is still shown.
    if (hasPendingUnknownWrite(scope)) return true;
    const operation = scopeOperation(scope);
    return operation?.state === 'unknown';
  }

  function reportWriteError(scope: string, error: unknown) {
    handleError(error);
    if (error instanceof PublicApiError && error.kind === 'authentication') {
      clearPendingUnknownScope(scope);
      return;
    }
    const externalBlocker = toExternalBlocker(error);
    if (externalBlocker) {
      clearPendingUnknownScope(scope);
      setWriteBlocker(externalBlocker);
      setGroupErrors((current) => {
        if (!current[scope]) return current;
        const next = { ...current };
        delete next[scope];
        return next;
      });
      return;
    }
    const code = error instanceof PublicApiError ? error.code ?? '' : '';
    const message = error instanceof PublicApiError ? (error.detail ?? '') : '';
    const field = error instanceof PublicApiError ? error.field : undefined;
    const reject = (key: Parameters<typeof c>[0], detail = '') =>
      setGroupErrors((current) => ({
        ...current,
        [scope]: detail ? `${c(key)} ${detail}` : c(key),
      }));
    if (code === 'source_or_operation_conflict') {
      // The baseline moved. Keep the draft and show old, new and draft side by
      // side: the user decides what to do against the new value.
      clearPendingUnknownScope(scope);
      rememberConflictBaseline(scope);
      setConflictGroup(scope);
      reject('groupSaveConflict');
      if (scope === 'source') {
        visualStaleRef.current = true;
        void loadSource({ keepDraft: true });
        void load({ keepDrafts: true });
      } else {
        void load({ keepDrafts: true });
      }
      return;
    }
    if (code === 'generation_mismatch') {
      // A typed conflict: this page read a different CPA target than it writes.
      rememberConflictBaseline(scope);
      clearPendingUnknownScope(scope);
      setConflictGroup(scope);
      reject('groupSaveGenerationMismatch');
      if (scope === 'source') {
        visualStaleRef.current = true;
        void loadSource({ keepDraft: true });
        void load({ keepDrafts: true });
      } else {
        void load({ keepDrafts: true });
      }
      return;
    }
    if (code === 'legacy_operation_unresolved') {
      clearPendingUnknownScope(scope);
      // Blocked until the earlier operation is settled; the group stays locked.
      reject('groupSaveLegacyBlocked');
      if (scope === 'source') {
        visualStaleRef.current = true;
        void loadSource({ keepDraft: true });
        void load({ keepDrafts: true });
      } else {
        void load({ keepDrafts: true });
      }
      return;
    }
    if (code === 'original_operation_unknown') {
      // Deterministic 409: this write's new requestId was rejected without entering
      // the server journal. Drop the rejected ID, show the conflict notice, and
      // refresh so the server's actual blocking operation.requestId takes effect.
      const ownOp = scopeOperation(scope);
      const journaledId = ownOp?.state === 'unknown' ? ownOp.requestId : '';
      delete pendingWrite.current[scope];
      if (!journaledId || pendingUnknownScopesRef.current[scope] !== journaledId) {
        clearPendingUnknownScope(scope);
      }
      reject('reasonUnknownOriginal');
      if (scope === 'source') {
        visualStaleRef.current = true;
        void loadSource({ keepDraft: true });
        void load({ keepDrafts: true });
      } else {
        void load({ keepDrafts: true });
      }
      return;
    }
    if (isUnknownResult(error)) {
      const ownOp = scopeOperation(scope);
      // A network failure, client abort, unresolved write, or code-less 5xx:
      // keep a reconcile handle when the server journal could hold our write.
      markPendingUnknownScope(
        scope,
        (ownOp?.state === 'unknown' ? ownOp.requestId : '') ||
          pendingWrite.current[scope] ||
          pendingUnknownScopes[scope] ||
          ownOp?.requestId ||
          ''
      );
      reject('groupSaveUnknown');
      if (scope === 'source') {
        visualStaleRef.current = true;
        void loadSource({ keepDraft: true });
      } else {
        void load({ keepDrafts: true });
      }
      return;
    }
    clearPendingUnknownScope(scope);
    if (code === 'invalid_source_yaml') {
      const detail = [field, message].filter(Boolean).join(' · ');
      reject('sourceInvalidYaml', detail);
      return;
    }
    if (code === 'protected_source_path') {
      reject('sourceProtectedPath', field ?? message);
      return;
    }
    if (code === 'source_no_changes') {
      reject('sourceNoChanges');
      return;
    }
    // Every other named rejection leaves the document unchanged, and the field
    // the Manager named is where the user must look. The two Manager-owned
    // codes are refusals decided before CPA was asked, so they never borrow
    // CPA's wording: each states its own rule instead.
    if (field) {
      const fieldKey =
        code === 'proxy_url_credentials'
          ? 'fieldProxyCredentials'
          : code === 'invalid_config'
            ? 'fieldPrivateRange'
            : 'fieldCpaRejected';
      setIssues((current) => ({ ...current, [field]: c(fieldKey) }));
    }
    if (code === 'cpa_rejected_config') {
      reject('groupSaveRejectedDetail', message);
      return;
    }
    if (code === 'invalid_config') {
      // A private-profile value outside its range: the Manager decided this
      // before CPA was asked, so the group names the Manager and keeps the
      // message it sent (the range it refused) instead of a generic failure.
      reject(message ? 'groupSavePrivateRangeDetail' : 'groupSavePrivateRange', message);
      return;
    }
    if (code === 'proxy_url_credentials') {
      // Also decided before CPA was asked; the leaf text names the credential
      // rule, so the group only says who refused and what unblocks the save.
      reject('groupSaveProxyCredentials');
      return;
    }
    if (code === 'invalid_config_request') {
      reject('groupSaveInvalidRequest');
      return;
    }
    if (code === 'payload_rules_read_only' || code === 'gateway_config_read_only') {
      reject('groupSaveReadOnly');
      return;
    }
    reject('groupSaveFailed');
  }

  async function saveGroup(group: GatewayGroupMeta): Promise<boolean> {
    const groupView = groupViews.get(group.id);
    const edits = dirtyByGroup.get(group.id) ?? [];
    if (!groupView || !edits.length || !groupView.writable || needsReconcile(group.id)) return false;
    // The private H1 profile writes one field per operation and has no delete,
    // so the Manager refuses both shapes; the page must never build them.
    if (limitIssue(view?.profileLimits, edits.length)) return false;
    if (!allowsDefaultEdit(view?.profileLimits) && edits.some((edit) => edit.action === 'default')) {
      setGroupErrors((current) => ({ ...current, [group.id]: c('groupSaveNoDefault') }));
      return false;
    }
    const problems: Record<string, string> = {};
    for (const edit of edits) {
      const field = fieldViews.get(edit.path);
      const draft = drafts[edit.path];
      if (!field || !draft) continue;
      const issue = resolveDraftIssue(field, draft);
      if (issue) problems[edit.path] = draftIssueMessage(issue);
    }
    if (Object.keys(problems).length) {
      setIssues((current) => ({ ...current, ...problems }));
      return false;
    }
    const requestId = newGatewayRequestId(issued.current);
    pendingWrite.current[group.id] = requestId;
    setSaving(group.id);
    setGroupErrors((current) => {
      const next = { ...current };
      delete next[group.id];
      return next;
    });
    clearConflictState();
    try {
      const result = await saveGatewayGroup(client, { kind: 'admin', value: credential }, group.id, {
        requestId,
        expected: { generation: view?.generation ?? '', baseline: groupView.baseline },
        edits,
      });
      clearPendingUnknownScope(group.id);
      if (result.operation.state === 'failed') {
        reportWriteError(group.id, new PublicApiError('http', 422, undefined, 'cpa_rejected_config'));
        await load({ keepDrafts: true });
        return false;
      }
      clearGroupDrafts(group.id);
      await load({ keepDrafts: true });
      return true;
    } catch (error) {
      reportWriteError(group.id, error);
      return false;
    } finally {
      setSaving('');
    }
  }

  async function saveFamily(family: GatewayFamilyMeta): Promise<boolean> {
    const rules = dirtyFamilies.get(family.family);
    const familyView = familyViews.get(family.family);
    const scope = familyScope(family.family);
    if (!rules || !familyView || !familyView.writable || needsReconcile(scope)) return false;
    const requestId = newGatewayRequestId(issued.current);
    pendingWrite.current[scope] = requestId;
    setSaving(scope);
    setGroupErrors((current) => {
      const next = { ...current };
      delete next[scope];
      return next;
    });
    clearConflictState();
    try {
      const result = await saveGatewayFamily(client, { kind: 'admin', value: credential }, family.family, {
        requestId,
        expected: { generation: view?.generation ?? '', baseline: familyView.baseline },
        rules,
      });
      clearPendingUnknownScope(scope);
      if (result.operation.state === 'failed') {
        reportWriteError(scope, new PublicApiError('http', 422, undefined, 'cpa_rejected_config'));
        await load({ keepDrafts: true });
        return false;
      }
      clearFamilyDraft(family.family);
      await load({ keepDrafts: true });
      return true;
    } catch (error) {
      reportWriteError(scope, error);
      return false;
    } finally {
      setSaving('');
    }
  }

  async function saveAllDirty() {
    if (saving !== '' || savingAll) return;
    setSavingAll(true);
    try {
      for (const group of groups) {
        const edits = dirtyByGroup.get(group.id) ?? [];
        const groupView = groupViews.get(group.id);
        if (!edits.length || !groupView?.writable || needsReconcile(group.id)) continue;
        if (limitIssue(view?.profileLimits, edits.length)) return;
        const ok = await saveGroup(group);
        if (!ok) return;
      }
      for (const family of families) {
        const familyView = familyViews.get(family.family);
        if (!dirtyFamilies.has(family.family) || !familyView?.writable || needsReconcile(familyScope(family.family))) continue;
        const ok = await saveFamily(family);
        if (!ok) return;
      }
    } finally {
      setSavingAll(false);
    }
  }
  async function saveSource(): Promise<boolean> {
    if (!sourceView || !sourceView.sourceWritable || !sourceDirty || saving !== '' || needsReconcile('source')) return false;
    const requestId = newGatewayRequestId(issued.current);
    pendingWrite.current.source = requestId;
    setSaving('source');
    setModeSwitchError('');
    setGroupErrors((current) => {
      const next = { ...current };
      delete next.source;
      return next;
    });
    clearConflictState();
    try {
      const result = await saveGatewaySource(client, { kind: 'admin', value: credential }, {
        requestId,
        expected: { generation: sourceView.generation, baseline: sourceView.baseline },
        yaml: sourceValue,
      });
      clearPendingUnknownScope('source');
      if (result.operation.state === 'failed') {
        reportWriteError('source', new PublicApiError('http', 422, undefined, 'cpa_rejected_config'));
        await loadSource({ keepDraft: true });
        return false;
      }
      setSourceDraft(null);
      setSourceSyncKey((n) => n + 1);
      clearConflictState();
      visualStaleRef.current = true;
      await Promise.all([loadSource(), load({ keepDrafts: true })]);
      return true;
    } catch (error) {
      reportWriteError('source', error);
      return false;
    } finally {
      setSaving('');
    }
  }

  function switchEditorMode(next: 'visual' | 'source') {
    if (next === editorMode) return;
    if (next === 'source' && dirtyCount > 0) {
      setModeSwitchError(c('sourceSwitchVisualDirty'));
      return;
    }
    if (next === 'visual' && sourceDirty) {
      setModeSwitchError(c('sourceSwitchSourceDirty'));
      return;
    }
    if (next === 'visual' && !sourceDirty) {
      clearConflictState();
      if (sourceDraft !== null) {
        setSourceDraft(null);
      }
      setGroupErrors((current) => {
        if (!current.source) return current;
        const copy = { ...current };
        delete copy.source;
        return copy;
      });
    }
    if (next === 'source' && sourceEditorFailedRef.current) {
      sourceEditorFailedRef.current = false;
      setSourceEditorRetryKey((n) => n + 1);
    }
    setModeSwitchError('');
    try {
      if (typeof window !== 'undefined' && window.localStorage) {
        window.localStorage.setItem(EDITOR_MODE_KEY, next);
      }
    } catch {
      // ignore storage errors
    }
    setEditorMode(next);
    if (next === 'visual') {
      if (visualStaleRef.current) {
        visualStaleRef.current = false;
      }
      void load({ keepDrafts: true });
    }
  }

  function runSourceSearch(direction: 'next' | 'prev' = 'next') {
    if (!sourceSearchText) {
      setSourceSearchStats({ current: 0, total: 0 });
      return;
    }
    const stats = sourceEditorRef.current?.search(sourceSearchText, direction) ?? { current: 0, total: 0 };
    setSourceSearchStats(stats);
  }
  async function queryOriginal(scope: string) {
    const requestId = reconcileTarget(scope);
    if (!requestId) {
      await Promise.all([loadSource({ keepDraft: true }), load({ keepDrafts: true })]);
      return;
    }
    setReconciling(scope);
    try {
      const result = await readGatewayOperation(
        client,
        { kind: 'admin', value: credential },
        requestId,
        true
      );
      const resolvedScope = (() => {
        if (result.operation.group === 'source') return 'source';
        if (result.operation.target === 'family') {
          if (result.operation.prefix) {
            const matchedFamily = [...familyViews.values()].find(
              (family) => family.path === result.operation.prefix
            );
            return matchedFamily ? familyScope(matchedFamily.family) : '';
          }
          return scope.startsWith('rules:') ? scope : '';
        }
        return result.operation.group;
      })();
      const targetFamilyPath = scope.startsWith('rules:')
        ? (familyViews.get(scope.slice('rules:'.length))?.path ??
          families.find((family) => family.family === scope.slice('rules:'.length))?.path ??
          '')
        : '';
      const matchesScope =
        scope === 'source'
          ? result.operation.group === 'source'
          : scope.startsWith('rules:')
            ? result.operation.target === 'family' &&
              (!result.operation.prefix ||
                !targetFamilyPath ||
                result.operation.prefix === targetFamilyPath)
            : result.operation.group === scope;
      if (result.operation.state !== 'unknown') {
        const scopesToClear = Array.from(
          new Set([scope, resolvedScope].filter((entry) => Boolean(entry)))
        );
        for (const targetScope of scopesToClear) {
          clearPendingUnknownScope(targetScope);
        }
        // The journal is now authoritative, so earlier unknown warnings on either
        // the clicked card or the operation's true owning scope must not outlive
        // the evidence that answered them (while preserving non-unknown errors).
        const unknownNotice = c('groupSaveUnknown');
        const blockedNotice = c('reasonUnknownOriginal');
        setGroupErrors((current) => {
          let changed = false;
          const next = { ...current };
          for (const targetScope of scopesToClear) {
            if (next[targetScope] === unknownNotice || next[targetScope] === blockedNotice) {
              delete next[targetScope];
              changed = true;
            }
          }
          return changed ? next : current;
        });
      } else {
        const unresolvedId = result.operation.requestId || requestId;
        markPendingUnknownScope(scope, unresolvedId);
        setGroupErrors((current) => ({ ...current, [scope]: c('groupSaveUnknown') }));
      }
      clearConflictState();
      const persistedInScope = result.operation.state === 'persisted' && matchesScope;
      if (persistedInScope) {
        clearScopeDrafts(scope);
      }
      const keepSourceDraft = scope !== 'source' || !persistedInScope;
      await Promise.all([
        loadSource({ keepDraft: keepSourceDraft }),
        load({ keepDrafts: true }),
      ]);
    } catch (error) {
      handleError(error);
      if (error instanceof PublicApiError && error.kind === 'authentication') return;
      const externalBlocker = toExternalBlocker(error);
      if (externalBlocker) {
        setWriteBlocker(externalBlocker);
        return;
      }
      if (error instanceof PublicApiError && error.kind === 'http') {
        if (error.code === 'original_operation_unknown') {
          const rawOp =
            isRecord(error.operationResult) && isRecord(error.operationResult.operation)
              ? error.operationResult.operation
              : undefined;
          const opState = rawOp && typeof rawOp.state === 'string' ? rawOp.state : '';
          const opRequestId = rawOp && typeof rawOp.requestId === 'string' ? rawOp.requestId : '';
          const unresolvedId = (opState === 'unknown' ? opRequestId : '') || requestId;
          if (unresolvedId) {
            markPendingUnknownScope(scope, unresolvedId);
          }
          setGroupErrors((current) => ({ ...current, [scope]: c('groupSaveUnknown') }));
          if (scope === 'source') {
            visualStaleRef.current = true;
            await Promise.all([loadSource({ keepDraft: true }), load({ keepDrafts: true })]);
          } else {
            await load({ keepDrafts: true });
          }
          return;
        }
        reportWriteError(scope, error);
        if (error.status === 404 || error.code === 'operation_not_found') {
          if (scope === 'source') {
            visualStaleRef.current = true;
            await Promise.all([loadSource({ keepDraft: true }), load({ keepDrafts: true })]);
          } else {
            await load({ keepDrafts: true });
          }
        }
        return;
      }
      setGroupErrors((current) => ({ ...current, [scope]: c('groupSaveUnknown') }));
    } finally {
      setReconciling('');
    }
  }

  const query = searchText.trim().toLowerCase();
  const isFiltering = Boolean(query) || changedOnly;
  const visibleGroups = useMemo(() => {
    const matches = (group: GatewayGroupMeta, field: GatewayFieldMeta) => {
      if (query) {
        const haystack = [
          group.titleKey ? c(group.titleKey) : '',
          r(field.labelKey),
          r(field.helpKey),
          field.path,
          ...(field.keywords ?? []),
        ]
          .join(' ')
          .toLowerCase();
        if (!haystack.includes(query)) return false;
      }
      if (changedOnly) {
        return (dirtyByGroup.get(group.id) ?? []).some((edit) => edit.path === field.path);
      }
      if (query) return true;
      if (currentTab === 'common') return COMMON_GATEWAY_PATHS[field.path] === true;
      if (currentTab === 'upstream') {
        return group.tab === 'upstream' && group.provider === activeUpstreamProvider;
      }
      return group.tab === currentTab;
    };
    return groups
      .map((group) => ({ ...group, fields: group.fields.filter((field) => matches(group, field)) }))
      .filter((group) => group.fields.length > 0);
  }, [activeUpstreamProvider, changedOnly, currentTab, dirtyByGroup, groups, query]);

  // The payload rule lists live on the request tab, exactly where the accepted
  // design placed them; a global filter still reaches them from anywhere.
  const visibleFamilies = useMemo(
    () =>
      families.filter((family) => {
        if (query && ![c(family.labelKey), family.path].join(' ').toLowerCase().includes(query)) {
          return false;
        }
        if (changedOnly) return dirtyFamilies.has(family.family);
        if (query) return true;
        return currentTab === 'payload';
      }),
    [changedOnly, currentTab, dirtyFamilies, families, query]
  );
  const capability = view?.capability;
  const pageReason = capability?.reason;
  const pageReadOnlyNotice = capability && !capability.writable ? c(reasonKey(pageReason)) : '';
  const rulesReadOnlyNotice =
    capability && capability.writable && !capability.rulesWritable ? c(reasonKey(capability.reason)) : '';
  const rulesReadOnlyDetail =
    capability && capability.writable && !capability.rulesWritable && capability.reason === 'external_rule_write_unproven'
      ? c('reasonExternalRulesDetail')
      : '';
  /** Old (this tab's baseline) and new (the re-read value) for a conflict. */
  function conflictRows() {
    if (conflictGroup === 'source') return [];
    const snapshot = conflictSnapshot?.fields ?? {};
    return dirtyEdits
      .map((edit) => {
        const field = fieldViews.get(edit.path);
        if (!field) return null;
        const before = snapshot[edit.path];
        return {
          path: edit.path,
          oldValue: before ? formatted(before.presence, edit.path, before.value) : c('valueUnknown'),
          newValue: formatted(field.presence, field.path, field.value),
          draftValue:
            edit.action === 'default'
              ? c('valueMissing')
              : formatted('present', edit.path, edit.value),
        };
      })
      .filter((row): row is NonNullable<typeof row> => row !== null);
  }

  function renderField(field: GatewayFieldView) {
    const meta = metaOf(field.path);
    const draft = drafts[field.path] ?? draftFor(field);
    const liveKvIssue =
      field.type === 'kv-map' && draft.action !== 'default'
        ? kvRowsIssue(kvRowsByPath[field.path])
        : null;
    const dirty = !draftEquals(field, draft) || liveKvIssue !== null;
    const disabled = !field.editable || saving !== '';
    const dependsOn = meta?.depends ? fieldViews.get(meta.depends) : undefined;
    const dependsDraft = dependsOn ? drafts[dependsOn.path] : undefined;
    const dependencyOff = dependsOn
      ? dependsDraft
        ? dependsDraft.action === 'set'
          ? dependsDraft.value === false
          : isInheritable(dependsOn)
            ? dependsOn.inherit === false
            : dependsOn.type === 'bool'
        : isInheritable(dependsOn)
          ? dependsOn.presence === 'present'
            ? dependsOn.value === false
            : dependsOn.inherit === false
          : dependsOn.type === 'bool'
            ? dependsOn.presence !== 'present' || dependsOn.value === false
            : dependsOn.presence === 'present' && dependsOn.value === false
      : false;
    const locked = !field.editable || dependencyOff;
    const label = meta ? r(meta.labelKey) : field.path;
    const help = meta ? r(meta.helpKey) : '';
    const issue = issues[field.path] ?? (liveKvIssue ? draftIssueMessage(liveKvIssue) : undefined);
    // leaf, so restoring a default is not offered on it at all.
    const canUseDefault = allowsDefaultEdit(view?.profileLimits);
    // A `*bool` leaf whose unset state resolves to a CPA default is edited as
    // inherit / on / off: an explicit false is a real change, not "no change".
    const inheritable = isInheritable(field);
    const state = boolState(draft);
    const testId = `gateway-field-${field.path}`;
    const isCompound =
      field.type === 'string-list' || field.type === 'kv-map' || field.type === 'pattern-list';
    return (
      <div
        className={
          'config-field' +
          (isCompound ? ' field-compound' : '') +
          (dirty ? ' field-dirty' : '') +
          (locked ? ' field-locked' : '')
        }
        key={field.path}
        data-testid={testId}
        data-dirty={dirty ? 'true' : 'false'}
      >
        <div className="field-info">
          <div className="field-head">
            <span className="field-label-text">{label}</span>
            {meta?.risk ? <span className="badge warn">{c('risk')}</span> : null}
            {dirty ? <span className="gateway-edited-pill">{c('fieldDirtyBadge')}</span> : null}
            <code className="field-path">{field.path}</code>
          </div>
          <p className="detail field-help">{help}</p>
          <p className="detail field-meta">
            <span>
              {c('fieldCurrentValue')}{' '}
              <strong>{formatted(field.presence, field.path, field.value)}</strong>
            </span>
            {dirty ? (
              <span className="gateway-draft-diff">
                {' · '}
                {c('fieldDraftValue')}{' '}
                <strong>
                  {draft.action === 'default'
                    ? inheritable
                      ? c(field.inherit === true ? 'valueInheritOn' : 'valueInheritOff')
                      : c('valueMissing')
                    : formatted('present', field.path, draft.value)}
                </strong>
              </span>
            ) : null}
          </p>
          {field.reason && !field.editable ? (
            <p className="detail field-reason">{c(reasonKey(field.reason))}</p>
          ) : null}
          {dependencyOff ? <p className="detail field-reason">{c('fieldDependencyOff')}</p> : null}
          {issue ? (
            <p className="field-error" role="alert" data-testid={`${testId}-error`}>
              {issue}
            </p>
          ) : null}
        </div>
        <div className="field-side">
          <div className="field-control">
            {field.type === 'bool' && inheritable ? (
              <div className="state-group" role="radiogroup" aria-label={label}>
                {(
                  [
                    ['inherit', c(field.inherit === true ? 'valueInheritOn' : 'valueInheritOff')],
                    ['on', c('valueOn')],
                    ['off', c('valueOff')],
                  ] as const
                ).map(([option, optionLabel]) => (
                  <button
                    key={option}
                    type="button"
                    role="radio"
                    aria-checked={state === option}
                    data-testid={`${testId}-${option}`}
                    className={'btn btn-secondary btn-sm' + (state === option ? ' active' : '')}
                    disabled={disabled || dependencyOff}
                    onClick={() => setDraft(field.path, boolDraft(option))}
                  >
                    {optionLabel}
                  </button>
                ))}
              </div>
            ) : field.type === 'bool' ? (
              <label className="switch">
                <input
                  type="checkbox"
                  aria-label={label}
                  disabled={disabled || dependencyOff}
                  checked={draft.action === 'set' && draft.value === true}
                  onChange={(event) => setDraft(field.path, { action: 'set', value: event.target.checked })}
                />
                <span className="track" aria-hidden="true">
                  <span className="thumb" />
                </span>
                <span className="label">
                  {draft.action === 'set' && draft.value === true ? c('valueOn') : c('valueOff')}
                </span>
              </label>
            ) : field.kind === 'select' ? (
              (() => {
                const selectValue = draft.action === 'default' ? '' : String(draft.value ?? '');
                const fieldRawValue = String(field.value ?? '');
                const hasEmptyOption = Boolean(meta?.options?.some((option) => option.value === ''));
                const needsEmptyOption =
                  !hasEmptyOption &&
                  (draft.action === 'default' || selectValue === '' || fieldRawValue === '');
                const hasCustomFieldOption =
                  fieldRawValue !== '' &&
                  !meta?.options?.some((option) => option.value === fieldRawValue);
                return (
                  <select
                    className="input"
                    aria-label={label}
                    disabled={disabled || dependencyOff}
                    value={selectValue}
                    onChange={(event) => {
                      if (event.target.value === '') {
                        setDraft(field.path, { action: 'default', value: '' });
                        return;
                      }
                      setDraft(field.path, { action: 'set', value: event.target.value });
                    }}
                  >
                    {needsEmptyOption ? (
                      <option value="">
                        {draft.action === 'default'
                          ? c('valueMissing')
                          : formatted(field.presence, field.path, field.value)}
                      </option>
                    ) : null}
                    {/* An image-mode value CPA already persists is shown, never offered
                        as a new draft: "passthrough" postdates the accepted inventory. */}
                    {meta?.options?.map((option) =>
                      option.readOnly && option.value !== fieldRawValue ? null : (
                        <option key={option.value} value={option.value} disabled={option.readOnly}>
                          {r(option.labelKey)}
                        </option>
                      )
                    )}
                    {hasCustomFieldOption ? (
                      <option value={fieldRawValue}>
                        {formatted(field.presence, field.path, field.value)}
                      </option>
                    ) : null}
                  </select>
                );
              })()
            ) : field.type === 'string-list' ? (
              <div className="gateway-compound-editor" data-testid={`${testId}-list`}>
                {(Array.isArray(draft.value) && draft.action !== 'default'
                  ? (draft.value as string[])
                  : []
                ).map((item, idx) => (
                  <div className="gateway-compound-row" key={idx}>
                    <input
                      className="input rule-mono-input"
                      aria-label={`${label} #${idx + 1}`}
                      disabled={disabled || dependencyOff}
                      placeholder={c('listPlaceholder')}
                      value={item}
                      onChange={(event) => {
                        const list = Array.isArray(draft.value) ? [...(draft.value as string[])] : [];
                        list[idx] = event.target.value;
                        setDraft(field.path, { action: 'set', value: list });
                      }}
                    />
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      disabled={disabled || dependencyOff}
                      onClick={() => {
                        const list = Array.isArray(draft.value) ? [...(draft.value as string[])] : [];
                        list.splice(idx, 1);
                        setDraft(field.path, { action: 'set', value: list });
                      }}
                    >
                      {c('ruleRemove')}
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  disabled={disabled || dependencyOff}
                  data-testid={`${testId}-add`}
                  onClick={() => {
                    const list =
                      draft.action === 'default' || !Array.isArray(draft.value)
                        ? []
                        : [...(draft.value as string[])];
                    list.push('');
                    setDraft(field.path, { action: 'set', value: list });
                  }}
                >
                  {c('listAddItem')}
                </button>
              </div>
            ) : field.type === 'kv-map' ? (
              (() => {
                const kvRows =
                  draft.action === 'default'
                    ? []
                    : (kvRowsByPath[field.path] ?? initialKvRows(field.path, draft.value));
                const commitKvRows = (nextRows: KvDraftRow[]) => {
                  setKvRowsByPath((current) => ({ ...current, [field.path]: nextRows }));
                  const nextRecord: Record<string, string> = {};
                  for (const entry of nextRows) {
                    if (Object.prototype.hasOwnProperty.call(nextRecord, entry.key)) continue;
                    nextRecord[entry.key] = entry.value;
                  }
                  setDraft(field.path, { action: 'set', value: nextRecord });
                };
                return (
                  <div className="gateway-compound-editor" data-testid={`${testId}-kv`}>
                    {kvRows.map((row, idx) => (
                      <div className="gateway-compound-row is-pair" key={row.id}>
                        <input
                          className="input rule-mono-input"
                          aria-label={`${label} key #${idx + 1}`}
                          disabled={disabled || dependencyOff}
                          placeholder={c('kvKeyPlaceholder')}
                          value={row.key}
                          onChange={(event) => {
                            const nextRows = kvRows.map((item) =>
                              item.id === row.id ? { ...item, key: event.target.value } : item
                            );
                            commitKvRows(nextRows);
                          }}
                        />
                        <input
                          className="input rule-mono-input"
                          aria-label={`${label} value #${idx + 1}`}
                          disabled={disabled || dependencyOff}
                          placeholder={c('kvValuePlaceholder')}
                          value={row.value}
                          onChange={(event) => {
                            const nextRows = kvRows.map((item) =>
                              item.id === row.id ? { ...item, value: event.target.value } : item
                            );
                            commitKvRows(nextRows);
                          }}
                        />
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          disabled={disabled || dependencyOff}
                          onClick={() => {
                            const nextRows = kvRows.filter((item) => item.id !== row.id);
                            commitKvRows(nextRows);
                          }}
                        >
                          {c('ruleRemove')}
                        </button>
                      </div>
                    ))}
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      disabled={disabled || dependencyOff}
                      data-testid={`${testId}-add`}
                      onClick={() => {
                        const nextRows = [
                          ...kvRows,
                          { id: `kv-row-${++nextKvRowId.current}`, key: '', value: '' },
                        ];
                        commitKvRows(nextRows);
                      }}
                    >
                      {c('kvAddPair')}
                    </button>
                  </div>
                );
              })()
            ) : field.type === 'pattern-list' ? (
              <div className="gateway-compound-editor" data-testid={`${testId}-patterns`}>
                {(Array.isArray(draft.value) && draft.action !== 'default'
                  ? (draft.value as Array<{ pattern?: string; replacement?: string }>)
                  : []
                ).map((item, idx) => (
                  <div className="gateway-compound-row is-pair" key={idx}>
                    <input
                      className="input rule-mono-input"
                      aria-label={`${label} pattern #${idx + 1}`}
                      disabled={disabled || dependencyOff}
                      placeholder={c('patternPlaceholder')}
                      value={String(item.pattern ?? '')}
                      onChange={(event) => {
                        const list = Array.isArray(draft.value)
                          ? [...(draft.value as Array<{ pattern?: string; replacement?: string }>)]
                          : [];
                        list[idx] = { pattern: event.target.value, replacement: String(item.replacement ?? '') };
                        setDraft(field.path, { action: 'set', value: list });
                      }}
                    />
                    <input
                      className="input rule-mono-input"
                      aria-label={`${label} replacement #${idx + 1}`}
                      disabled={disabled || dependencyOff}
                      placeholder={c('kvValuePlaceholder')}
                      value={String(item.replacement ?? '')}
                      onChange={(event) => {
                        const list = Array.isArray(draft.value)
                          ? [...(draft.value as Array<{ pattern?: string; replacement?: string }>)]
                          : [];
                        list[idx] = { pattern: String(item.pattern ?? ''), replacement: event.target.value };
                        setDraft(field.path, { action: 'set', value: list });
                      }}
                    />
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      disabled={disabled || dependencyOff}
                      onClick={() => {
                        const list = Array.isArray(draft.value)
                          ? [...(draft.value as Array<{ pattern?: string; replacement?: string }>)]
                          : [];
                        list.splice(idx, 1);
                        setDraft(field.path, { action: 'set', value: list });
                      }}
                    >
                      {c('ruleRemove')}
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  disabled={disabled || dependencyOff}
                  data-testid={`${testId}-add`}
                  onClick={() => {
                    const list =
                      draft.action === 'default' || !Array.isArray(draft.value)
                        ? []
                        : [...(draft.value as Array<{ pattern?: string; replacement?: string }>)];
                    list.push({ pattern: '', replacement: '' });
                    setDraft(field.path, { action: 'set', value: list });
                  }}
                >
                  {c('patternAddRule')}
                </button>
              </div>
            ) : (
              <input
                className="input"
                aria-label={label}
                type={field.type === 'int' ? 'number' : 'text'}
                inputMode={field.type === 'int' ? 'numeric' : undefined}
                disabled={disabled || dependencyOff}
                placeholder={formatted(field.presence, field.path, field.value)}
                value={draft.action === 'default' ? '' : String(draft.value ?? '')}
                onChange={(event) => {
                  const value = event.target.value;
                  const alreadyDefault = draft.action === 'default';
                  if (alreadyDefault && value === '') return;
                  setDraft(field.path, { action: 'set', value });
                }}
              />
            )}
            {!inheritable && canUseDefault && draft.action !== 'default' && field.presence !== 'missing' ? (
              <button
                type="button"
                className="btn btn-secondary btn-sm gateway-default-btn"
                disabled={disabled || dependencyOff}
                onClick={() => {
                  setKvRowsByPath((current) => {
                    if (!(field.path in current)) return current;
                    const next = { ...current };
                    delete next[field.path];
                    return next;
                  });
                  setDraft(field.path, { action: 'default', value: '' });
                }}
              >
                {c('fieldUseDefault')}
              </button>
            ) : null}
          </div>
        </div>
      </div>
    );
  }

  function renderGroupCard(group: GatewayGroupMeta) {
    const groupView = groupViews.get(group.id);
    const edits = dirtyByGroup.get(group.id) ?? [];
    const operation = groupView?.operation;
    const error = groupErrors[group.id];
    // An unresolved operation is reconciled from the journaled request ID, so
    // the control survives a reload instead of depending on this tab's memory.
    const reconcileId = reconcileTarget(group.id);
    const blocked = needsReconcile(group.id);
    // `group` here is the filtered projection: an alias view never renders a
    // leaf the current tab, search or changed-only filter hides.
    const members = group.fields;
    const fullCount = groups.find((entry) => entry.id === group.id)?.fields.length ?? members.length;
    const isSubset = members.length < fullCount;
    const fieldViewsOfGroup = members
      .map((meta) => fieldViews.get(meta.path))
      .filter((field): field is GatewayFieldView => !!field);
    const singleEdit = limitIssue(view?.profileLimits, edits.length);
    const forceExpanded = isFiltering || edits.length > 0 || !!error || blocked;
    const isCollapsed = group.collapsedByDefault === true && !forceExpanded && !expandedGroups[group.id];
    return (
      <section className="card gateway-group-card" key={group.id} data-testid={`gateway-group-${group.id}`}>
        <header className="card-head">
          <div className="gateway-card-heading">
            <h2>{c(group.titleKey)}</h2>
            <p className="detail">{c(group.helpKey)}</p>
          </div>
          <div className="gateway-card-badges">
            {edits.length > 0 ? (
              <span className="gateway-dirty-pill">{c('groupDirtyBadge', edits.length)}</span>
            ) : null}
            <span className="badge">
              {isSubset ? c('groupShownRatio', members.length).replace('{total}', String(fullCount)) : c('fieldCount', members.length)}
            </span>
            {group.collapsedByDefault && !forceExpanded ? (
              <button
                type="button"
                className="btn btn-secondary btn-xs"
                data-testid={`gateway-group-toggle-${group.id}`}
                onClick={() =>
                  setExpandedGroups((current) => ({
                    ...current,
                    [group.id]: !current[group.id],
                  }))
                }
              >
                {isCollapsed ? c('expandGroup') : c('collapseGroup')}
              </button>
            ) : null}
          </div>
        </header>
        {isCollapsed ? null : (
          <>
            {isSubset && currentTab === 'common' ? (
              <div className="gateway-subset-bar">
                <span className="detail">{c('commonSubsetHint')}</span>
                <button
                  type="button"
                  className="btn btn-secondary btn-xs"
                  onClick={() => {
                    if (group.provider) setActiveUpstreamProvider(group.provider);
                    setCurrentTab(group.tab);
                  }}
                >
                  {c('gotoGroup')}
                </button>
              </div>
            ) : null}
            <div className={'gateway-fields-list' + (fieldViewsOfGroup.length === 1 ? ' is-single' : '')}>
              {fieldViewsOfGroup.map((field) => renderField(field))}
            </div>
            <SectionActionFooter
              className="group-footer"
              status={
                <span className="detail">
                  {edits.length ? c('groupDirtyCount', edits.length) : c('noDrafts')}
                </span>
              }
            >
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                disabled={!edits.length}
                onClick={() => {
                  setDiscardGroup(group.id);
                }}
              >
                {c('discard')}
              </button>
              <button
                type="button"
                className="btn btn-primary btn-sm"
                disabled={blocked || !edits.length || !groupView?.writable || saving !== '' || savingAll || singleEdit !== null}
                onClick={() => void saveGroup(group)}
              >
                {saving === group.id ? c('groupSaving') : c('groupSave')}
              </button>
            </SectionActionFooter>
          </>
        )}
        {singleEdit ? (
          <p className="notice warn" data-testid={`gateway-group-single-edit-${group.id}`}>
            {c('profileSingleEdit')}
          </p>
        ) : null}
        {groupView && !groupView.writable ? (
          <p className="notice warn">{c(reasonKey(groupView.reason))}</p>
        ) : null}
        {error ? (
          <p className="field-error" role="alert" data-testid={`gateway-group-error-${group.id}`}>
            {error}
          </p>
        ) : null}
        {blocked && reconcileId ? (
          <p className="notice warn" data-testid={`gateway-group-reconcile-${group.id}`}>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={reconciling !== ''}
              onClick={() => void queryOriginal(group.id)}
            >
              {reconciling === group.id ? c('groupReconciling') : c('groupQueryOriginal')}
            </button>
          </p>
        ) : null}
        {operation ? (
          <p className="detail" data-testid={`gateway-group-status-${group.id}`}>
            {operation.state === 'persisted'
              ? operation.code === 'partial'
                ? c('operationPartial')
                : operation.code === 'superseded'
                  ? c('operationSuperseded')
                  : c('savedPersisted')
              : operation.state === 'failed'
                ? c('groupSaveRejected')
                : c('groupSaveUnknown')}
            {operation.state === 'persisted' ? ' · ' + c('savedEffectiveUnknown') : ''}
          </p>
        ) : null}
      </section>
    );
  }

  function renderFamilyCard(family: GatewayFamilyMeta) {
    const familyView = familyViews.get(family.family);
    const draft = familyDrafts[family.family];
    const dirty = dirtyFamilies.has(family.family);
    const rules = draft ?? (Array.isArray(familyView?.rules) ? (familyView.rules as unknown[]) : []);
    // A family save journals under its own scope, so its error and its
    // reconcile target belong to this card and not to the whole rules group.
    const scope = familyScope(family.family);
    const error = groupErrors[scope];
    const reconcileId = reconcileTarget(scope);
    const blocked = needsReconcile(scope);
    const showCardActions = !familyView?.readOnly || dirty;
    const showCardReadOnlyNotice =
      Boolean(familyView?.readOnly) &&
      !blocked &&
      (!rulesReadOnlyNotice || (Boolean(familyView?.reason) && familyView?.reason !== capability?.reason));
    return (
      <section className="card gateway-family-card" key={family.family} data-testid={`gateway-family-${family.family}`}>
        <header className="card-head">
          <h2 className="gateway-family-title">
            <span className="gateway-step-badge">{family.step}</span>
            <span>{c(family.labelKey)}</span>
          </h2>
          <div className="gateway-family-badges">
            {familyView?.readOnly && !dirty ? <span className="badge">{c('readOnlyBadge')}</span> : null}
            <span className="badge">{c('ruleCount', rules.length)}</span>
          </div>
        </header>
        <div className="gateway-family-meta">
          <p className="detail">{c(family.hintKey)}</p>
          <code className="gateway-field-path">{family.path}</code>
        </div>
        {rules.length ? (
          <ul className="rule-preview">
            {rules.map((rule, index) => (
              <li key={index}>{ruleSummary(rule, family)}</li>
            ))}
          </ul>
        ) : (
          <p className="detail">{c('ruleEmpty')}</p>
        )}
        {showCardActions ? (
          <div className="actions">
            <button
              className="btn btn-secondary btn-sm"
              disabled={!familyView?.writable}
              onClick={() => setRuleFamily(family)}
            >
              {c('ruleEditFamily')}
            </button>
            <button className="btn btn-secondary btn-sm" disabled={!dirty} onClick={() => clearFamilyDraft(family.family)}>
              {c('discard')}
            </button>
            <button
              className="btn btn-primary btn-sm"
              disabled={blocked || !dirty || !familyView?.writable || saving !== '' || savingAll}
              onClick={() => void saveFamily(family)}
            >
              {saving === scope ? c('groupSaving') : c('ruleSaveFamily')}
            </button>
          </div>
        ) : null}
        {error ? (
          <p className="field-error" role="alert" data-testid={`gateway-family-error-${family.family}`}>
            {error}
          </p>
        ) : null}
        {blocked && reconcileId ? (
          <p className="notice warn" data-testid={`gateway-family-reconcile-${family.family}`}>
            <button
              className="btn btn-secondary btn-sm"
              disabled={reconciling !== ''}
              onClick={() => void queryOriginal(scope)}
            >
              {reconciling === scope ? c('groupReconciling') : c('groupQueryOriginal')}
            </button>
          </p>
        ) : null}
        {familyView?.operation ? (
          <p className="detail" data-testid={`gateway-family-status-${family.family}`}>
            {familyView.operation.state === 'persisted'
              ? familyView.operation.code === 'superseded'
                ? c('operationSuperseded')
                : c('savedPersisted')
              : familyView.operation.state === 'failed'
                ? c('groupSaveRejected')
                : c('groupSaveUnknown')}
          </p>
        ) : null}
        {showCardReadOnlyNotice ? (
          <p className="notice warn">{c(reasonKey(familyView?.reason))}</p>
        ) : null}
      </section>
    );
  }
  const tabItems: SegmentedTabItem<GatewayTabKey>[] = GATEWAY_TABS.map((tab) => {
    const metric = tabMetrics[tab] ?? { dirty: 0, errors: 0 };
    return {
      id: tab,
      testId: `gateway-tab-${tab}`,
      title: metric.errors
        ? `${c(tab)} · ${c('validationIssues', metric.errors)}`
        : metric.dirty
          ? `${c(tab)} · ${c('unsaved', metric.dirty)}`
          : c(tab),
      label: (
        <>
          <span className="gateway-tab-label">{c(tab)}</span>
          {metric.errors ? (
            <span className="gateway-tab-count has-errors" data-testid={`gateway-tab-errors-${tab}`}>
              {metric.errors}
            </span>
          ) : metric.dirty ? (
            <span className="gateway-tab-count" data-testid={`gateway-tab-dirty-${tab}`}>
              {metric.dirty}
            </span>
          ) : null}
        </>
      ),
    };
  });

  const modeSwitchControl = (
    <SegmentedTabs
      variant="joined"
      roleMode="group"
      responsiveFullWidth={false}
      idBase="gateway-mode"
      activeTab={editorMode}
      onChange={switchEditorMode}
      ariaLabel={c('modeSwitchAria')}
      items={[
        { id: 'visual', label: c('modeVisual'), testId: 'gateway-mode-visual' },
        { id: 'source', label: c('modeSource'), testId: 'gateway-mode-source' },
      ]}
    />
  );
  const reloadButtonControl = (
    <button
      type="button"
      className="btn btn-secondary btn-sm"
      disabled={loading || sourceLoading}
      onClick={triggerWorkspaceReload}
    >
      <IconRefreshCw size={15} aria-hidden="true" />
      <span>{c('reload')}</span>
    </button>
  );

  return (
    <section className="config-workspace fresh-gateway-workspace" data-testid="gateway-config-workspace">
      <PageControlBar
        leading={
          editorMode === 'visual' ? (
            <SegmentedTabs
              items={tabItems}
              activeTab={currentTab}
              ariaLabel={c('gateway')}
              idBase="gateway-tab"
              className="gateway-segmented-tabs"
              responsiveFullWidth={false}
              onChange={(tab) => {
                setCurrentTab(tab);
                if (query) setSearchText('');
                if (changedOnly) setChangedOnly(false);
              }}
            />
          ) : (
            <span className="gateway-source-file">
              <code className="gateway-source-filename">gateway.yaml</code>
            </span>
          )
        }
        actions={
          <>
            <span
              className={
                'gateway-capability-status ' +
                (capability?.mode === 'managed'
                  ? 'is-success'
                  : capability?.mode === 'external'
                    ? 'is-info'
                    : 'is-warning')
              }
              data-testid="gateway-capability"
            >
              <span className="status-dot" aria-hidden="true" />
              {c(capabilityKey(capability?.mode))}
            </span>
            {modeSwitchControl}
            {reloadButtonControl}
          </>
        }
        toolbar={
          <div className="gateway-toolbar-row">
            {editorMode === 'source' ? (
              <>
                <div className="gateway-toolbar-left">
                  <label className="search" aria-label={c('sourceSearchPlaceholder')}>
                    <span className="gateway-search-input-wrap">
                      <input
                        className="input"
                        type="search"
                        data-testid="gateway-source-search"
                        aria-label={c('sourceSearchPlaceholder')}
                        value={sourceSearchText}
                        placeholder={c('sourceSearchPlaceholder')}
                        onChange={(event) => {
                          setSourceSearchText(event.target.value);
                          setSourceSearchStats({ current: 0, total: 0 });
                        }}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') {
                            event.preventDefault();
                            runSourceSearch(event.shiftKey ? 'prev' : 'next');
                          }
                        }}
                      />
                      <IconSearch
                        size={16}
                        className="gateway-search-icon"
                        aria-hidden="true"
                      />
                    </span>
                  </label>
                  {sourceSearchStats.total > 0 ? (
                    <span className="badge" data-testid="gateway-source-search-count">
                      {sourceSearchStats.current} / {sourceSearchStats.total}
                    </span>
                  ) : null}
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!sourceSearchText}
                    onClick={() => runSourceSearch('prev')}
                  >
                    {c('sourceSearchPrev')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!sourceSearchText}
                    onClick={() => runSourceSearch('next')}
                  >
                    {c('sourceSearchNext')}
                  </button>
                </div>
                <div className="gateway-toolbar-right">
                  <span
                    className={'badge' + (sourceDirty ? ' gateway-dirty-badge' : '')}
                    data-testid="gateway-source-status"
                  >
                    {sourceDirty ? c('sourceDirtyBadge') : c('sourceCleanBadge')}
                  </span>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!sourceDirty}
                    data-testid="gateway-source-diff-btn"
                    onClick={() => setSourceDiffOpen(true)}
                  >
                    {c('sourceCompare')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!sourceDirty}
                    data-testid="gateway-source-discard"
                    onClick={() => {
                      clearScopeDrafts('source');
                      setModeSwitchError('');
                    }}
                  >
                    {c('discard')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    disabled={
                      needsReconcile('source') ||
                      !sourceDirty ||
                      !sourceView?.sourceWritable ||
                      saving !== '' ||
                      savingAll
                    }
                    data-testid="gateway-source-save"
                    onClick={() => void saveSource()}
                  >
                    {saving === 'source' ? c('sourceSaving') : c('sourceSave')}
                  </button>
                </div>
              </>
            ) : (
              <>
                <div className="gateway-toolbar-left">
                  <label className="search" aria-label={c('search')}>
                    <span className="gateway-search-input-wrap">
                      <input
                        className="input"
                        type="search"
                        aria-label={c('search')}
                        value={searchText}
                        placeholder={c('search')}
                        onChange={(event) => setSearchText(event.target.value)}
                      />
                      <IconSearch
                        size={16}
                        className="gateway-search-icon"
                        aria-hidden="true"
                      />
                    </span>
                  </label>
                  {query ? (
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      onClick={() => setSearchText('')}
                    >
                      {c('clearSearch')}
                    </button>
                  ) : null}
                  <label className={'gateway-filter-chip' + (changedOnly ? ' is-active' : '')}>
                    <input
                      type="checkbox"
                      checked={changedOnly}
                      onChange={(event) => setChangedOnly(event.target.checked)}
                    />
                    <span>{c('changedOnly')}</span>
                  </label>
                </div>
                <div className="gateway-toolbar-right">
                  <span
                    className={'badge' + (dirtyCount > 0 ? ' gateway-dirty-badge' : '')}
                    data-testid="gateway-dirty-count"
                  >
                    {c('dirtyCount', dirtyCount)}
                  </span>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!dirtyCount}
                    onClick={() => setDiffOpen(true)}
                  >
                    {c('viewChanges')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    disabled={!dirtyCount}
                    onClick={() => setDiscardAll(true)}
                  >
                    {c('discard')}
                  </button>
                  {dirtyCount > 0 && view?.profileLimits?.singleEdit !== true ? (
                    <button
                      type="button"
                      className="btn btn-primary btn-sm"
                      data-testid="gateway-save-all"
                      disabled={
                        saving !== '' ||
                        savingAll ||
                        groups.some(
                          (group) =>
                            (dirtyByGroup.get(group.id)?.length ?? 0) > 0 &&
                            needsReconcile(group.id)
                        ) ||
                        families.some(
                          (family) =>
                            dirtyFamilies.has(family.family) &&
                            needsReconcile(familyScope(family.family))
                        ) ||
                        !(
                          groups.some(
                            (group) =>
                              (dirtyByGroup.get(group.id)?.length ?? 0) > 0 &&
                              Boolean(groupViews.get(group.id)?.writable) &&
                              !needsReconcile(group.id)
                          ) ||
                          families.some(
                            (family) =>
                              dirtyFamilies.has(family.family) &&
                              Boolean(familyViews.get(family.family)?.writable) &&
                              !needsReconcile(familyScope(family.family))
                          )
                        )
                      }
                      onClick={() => void saveAllDirty()}
                    >
                      {savingAll ? c('savingAllGroups') : c('saveAllGroups', dirtyCount)}
                    </button>
                  ) : null}
                </div>
              </>
            )}
          </div>
        }
      />
      {modeSwitchError ? (
        <p className="alert alert-warn" role="alert" data-testid="gateway-mode-switch-error">
          {modeSwitchError}
        </p>
      ) : null}
      {blocker ? (
        <FreshExternalBlocker
          state={blocker}
          onRebind={onRebind}
          onReload={() => {
            clearConflictState();
            if (editorMode === 'source') {
              void loadSource({ keepDraft: true });
            } else {
              void load({ keepDrafts: true });
            }
          }}
          onDismiss={() => {
            setWriteBlocker(null);
            if (editorMode === 'source') setSourceBlocker(null);
            else setVisualBlocker(null);
          }}
        />
      ) : null}
      {loadFailed && !blocker ? (
        <p className="alert alert-warn" role="alert" data-testid="gateway-load-failed">
          {c('loadFailed')}
        </p>
      ) : null}
      {view?.groupsInvalid ? (
        <p className="alert alert-warn" role="alert" data-testid="gateway-journal-invalid">
          {c('reasonJournalInvalid')}
        </p>
      ) : null}
      {pageReadOnlyNotice ? (
        <p className="alert alert-warn gateway-readonly-banner" data-testid="gateway-readonly">
          {pageReadOnlyNotice}
        </p>
      ) : null}
      {conflictGroup
        ? (() => {
            const rows = conflictRows();
            return (
              <div className="alert alert-warn" role="alert" data-testid="gateway-conflict">
                <strong>{c('conflictTitle')}</strong> {c('conflictHelp')}
                {rows.length > 0 ? (
                  <ul className="diff-list" data-testid="gateway-conflict-diff">
                    {rows.map((row) => (
                      <li className="diff-item" key={row.path}>
                        <code>{row.path}</code>
                        <span className="diff-old">{row.oldValue}</span>
                        <span className="diff-arrow" aria-hidden="true">→</span>
                        <span>{row.newValue}</span>
                        <span className="diff-arrow" aria-hidden="true">·</span>
                        <span>
                          {c('fieldDraftValue')} <strong>{row.draftValue}</strong>
                        </span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </div>
            );
          })()
        : null}
      {editorMode === 'source' ? (
        <div className="gateway-source-workspace" data-testid="gateway-source-workspace">
          {sourceView && !sourceView.sourceWritable ? (
            <p className="alert alert-warn" data-testid="gateway-source-readonly">
              {sourceView.capability.mode === 'private'
                ? c('sourcePrivateReadOnly')
                : c(reasonKey(sourceView.reason))}
            </p>
          ) : (
            <p className="notice gateway-source-notice" data-testid="gateway-source-notice">
              {c('sourceNoticeScope')}
            </p>
          )}
          {groupErrors.source ? (
            <p className="field-error" role="alert" data-testid="gateway-source-error">
              {groupErrors.source}
            </p>
          ) : null}
          {needsReconcile('source') && reconcileTarget('source') ? (
            <p className="notice warn" data-testid="gateway-source-reconcile">
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                disabled={reconciling !== ''}
                onClick={() => void queryOriginal('source')}
              >
                {reconciling === 'source' ? c('groupReconciling') : c('groupQueryOriginal')}
              </button>
            </p>
          ) : null}
          <div className="gateway-source-editor-shell">
            <SourceEditorErrorBoundary
              resetKey={sourceEditorRetryKey}
              fallbackMessage={c('loadFailed')}
              retryLabel={c('reload')}
              onError={() => {
                sourceEditorFailedRef.current = true;
              }}
              onRetry={() => {
                sourceEditorFailedRef.current = false;
                setSourceEditorRetryKey((n) => n + 1);
                void loadSource({ keepDraft: true });
              }}
            >
              <Suspense fallback={<p className="detail">{c('sourceLoading')}</p>}>
                <LazyGatewaySourceEditor
                  key={sourceEditorRetryKey}
                  editorRef={sourceEditorRef}
                  syncKey={sourceSyncKey}
                  value={sourceValue}
                  editable={Boolean(sourceView?.sourceWritable) && saving === ''}
                  placeholder={c('sourcePlaceholder')}
                  onChange={(next) => {
                    setSourceDraft(next === (sourceView?.yaml ?? '') ? null : next);
                    if (modeSwitchError) setModeSwitchError('');
                    setGroupErrors((current) => {
                      if (!current.source) return current;
                      const copy = { ...current };
                      delete copy.source;
                      return copy;
                    });
                  }}
                />
              </Suspense>
            </SourceEditorErrorBoundary>
          </div>
          <FloatingActionDock
            open={isRouteVisible && editorMode === 'source' && sourceDirty}
            variant="compact"
            ariaLabel={c('sourceDirtyBadge')}
            testId="gateway-source-dock"
            leading={
              <span className="badge gateway-dirty-badge" data-testid="gateway-source-dock-status">
                {c('sourceDirtyBadge')}
              </span>
            }
          >
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={!sourceDirty}
              data-testid="gateway-source-dock-diff-btn"
              onClick={() => setSourceDiffOpen(true)}
            >
              {c('sourceCompare')}
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={!sourceDirty}
              data-testid="gateway-source-dock-discard"
              onClick={() => {
                clearScopeDrafts('source');
                setModeSwitchError('');
              }}
            >
              {c('discard')}
            </button>
            <button
              type="button"
              className="btn btn-primary btn-sm"
              disabled={
                needsReconcile('source') ||
                !sourceDirty ||
                !sourceView?.sourceWritable ||
                saving !== '' ||
                savingAll
              }
              data-testid="gateway-source-dock-save"
              onClick={() => void saveSource()}
            >
              {saving === 'source' ? c('sourceSaving') : c('sourceSave')}
            </button>
          </FloatingActionDock>
        </div>
      ) : (
        <>
      <div
        id="gateway-tabpanel"
        role="tabpanel"
        aria-labelledby={`gateway-tab-${currentTab}`}
        className="workspace-body"
      >
        {isFiltering ? (
          <div className="gateway-filter-banner">
            <span className="detail">
              {c('filterScopeSummary', visibleGroups.length + (visibleFamilies.length > 0 ? 1 : 0))}
            </span>
            <button
              type="button"
              className="btn btn-secondary btn-xs"
              onClick={() => {
                setSearchText('');
                setChangedOnly(false);
              }}
            >
              {c('clearFilter')}
            </button>
          </div>
        ) : null}
        {currentTab === 'upstream' && !isFiltering ? (
          <div className="gateway-upstream-bar" data-testid="gateway-upstream-nav">
            <p className="detail">{c('upstreamNavHint')}</p>
            <div className="gateway-upstream-pills" role="group" aria-label={c('upstreamNavLabel')}>
              {UPSTREAM_PROVIDERS.map((provider) => {
                const selected = activeUpstreamProvider === provider;
                const metric = upstreamMetrics[provider];
                const labelKey = (
                  provider === 'codex'
                    ? 'upstreamProviderCodex'
                    : provider === 'claude'
                      ? 'upstreamProviderClaude'
                      : provider === 'antigravity'
                        ? 'upstreamProviderAntigravity'
                        : provider === 'devin'
                          ? 'upstreamProviderDevin'
                          : provider === 'other'
                            ? 'upstreamProviderOther'
                            : 'upstreamProviderOauth'
                ) satisfies Parameters<typeof c>[0];
                return (
                  <button
                    key={provider}
                    type="button"
                    aria-pressed={selected}
                    data-testid={`gateway-upstream-${provider}`}
                    className={'gateway-upstream-pill' + (selected ? ' is-active' : '')}
                    onClick={() => setActiveUpstreamProvider(provider)}
                  >
                    <span>{c(labelKey)}</span>
                    {metric.errors ? (
                      <span
                        className="gateway-tab-count has-errors"
                        data-testid={`gateway-upstream-errors-${provider}`}
                      >
                        {metric.errors}
                      </span>
                    ) : metric.dirty ? (
                      <span
                        className="gateway-tab-count"
                        data-testid={`gateway-upstream-dirty-${provider}`}
                      >
                        {metric.dirty}
                      </span>
                    ) : null}
                  </button>
                );
              })}
            </div>
          </div>
        ) : null}
        {(currentTab === 'common' && !isFiltering) ||
        (Boolean(query) &&
          !changedOnly &&
          /plugin|插件|外掛|плагин/i.test(query)) ? (
          <PluginRuntimeSwitch
            client={client}
            credential={credential}
            onApplied={() => {
              const cfgStore = useConfigStore.getState();
              if (cfgStore.config) {
                void cfgStore.fetchConfig(undefined, true).catch(() => {});
              } else {
                cfgStore.clearCache();
              }
              notifyPluginResourcesChanged();
              notifyPluginResourcesChanged({ delayMs: PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS });
            }}
          />
        ) : null}
        {visibleGroups.map((group) => renderGroupCard(group))}
        {currentTab === 'upstream' && !isFiltering && activeUpstreamProvider === 'codex' ? (
          <div className="gateway-crosslink-bar" data-testid="gateway-codex-media-link">
            <span className="detail">{c('upstreamMediaLinkHint')}</span>
            <button
              type="button"
              className="btn btn-secondary btn-xs"
              onClick={() => setCurrentTab('media')}
            >
              {c('upstreamMediaLinkAction')}
            </button>
          </div>
        ) : null}
        {visibleFamilies.length > 0 && rulesReadOnlyNotice ? (
          <div className="alert alert-warn gateway-readonly-banner" data-testid="gateway-readonly">
            <p className="gateway-readonly-title">{rulesReadOnlyNotice}</p>
            {rulesReadOnlyDetail ? (
              <p className="gateway-readonly-detail" data-testid="gateway-readonly-detail">
                {rulesReadOnlyDetail}
              </p>
            ) : null}
          </div>
        ) : null}
        {visibleFamilies.length ? (
          <div className="gateway-family-grid" data-testid="gateway-family-grid">
            {visibleFamilies.map((family) => renderFamilyCard(family))}
          </div>
        ) : null}
        {!visibleGroups.length &&
        !visibleFamilies.length &&
        !(
          Boolean(query) &&
          !changedOnly &&
          capability?.mode === 'managed' &&
          capability?.writable &&
          /plugin|插件|外掛|плагин/i.test(query)
        ) ? (
          <p className="detail" data-testid="gateway-no-matches">
            {c('noMatches')}
          </p>
        ) : null}
      </div>
        </>
      )}
      <Modal open={diffOpen} width={720} title={c('diffTitle')} onClose={() => setDiffOpen(false)}>
        <p className="detail">{c('diffHelp')}</p>
        {dirtyEdits.length ? (
          <ul className="diff-list" data-testid="gateway-diff">
            {dirtyEdits.map((edit) => {
              const field = fieldViews.get(edit.path);
              const ownerGroup = groups.find((entry) => entry.id === field?.group);
              return (
                <li className="diff-item" key={edit.path}>
                  <div className="diff-header">
                    <code>{edit.path}</code>
                    {ownerGroup ? (
                      <button
                        type="button"
                        className="btn btn-secondary btn-xs"
                        onClick={() => {
                          setDiffOpen(false);
                          setSearchText('');
                          setChangedOnly(false);
                          if (ownerGroup.provider) setActiveUpstreamProvider(ownerGroup.provider);
                          setCurrentTab(ownerGroup.tab);
                        }}
                      >
                        {c('jumpToGroup')}
                      </button>
                    ) : null}
                  </div>
                  <div className="diff-values">
                    <span className="diff-old">
                      {field ? formatted(field.presence, field.path, field.value) : ''}
                    </span>
                    <span className="diff-arrow" aria-hidden="true">→</span>
                    <span className="diff-new">
                      {edit.action === 'default'
                        ? c('valueMissing')
                        : field
                          ? formatted('present', edit.path, edit.value)
                          : String(edit.value)}
                    </span>
                  </div>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="detail">{c('noDrafts')}</p>
        )}
        {[...dirtyFamilies.entries()].map(([family, rules]) => (
          <p className="detail" key={family}>
            <code>{family}</code> · {c('ruleCount', rules.length)}
          </p>
        ))}
        <p className="detail">{c('diffNotice')}</p>
      </Modal>
      <Modal
        open={discardGroup !== null || discardAll}
        title={c('discardTitle')}
        onClose={() => {
          setDiscardGroup(null);
          setDiscardAll(false);
        }}
        footer={
          <>
            <button
              className="btn btn-secondary"
              onClick={() => {
                setDiscardGroup(null);
                setDiscardAll(false);
              }}
            >
              {c('keep')}
            </button>
            <button
              className="btn btn-primary"
              onClick={() => {
                clearGroupDrafts(discardAll ? null : discardGroup);
                setDiscardGroup(null);
                setDiscardAll(false);
              }}
            >
              {c('confirmDiscard')}
            </button>
          </>
        }
      >
        <p className="detail" data-testid="gateway-discard-help">
          {c('discardHelp', discardCount)}
        </p>
      </Modal>
      <Modal
        open={sourceDiffOpen}
        width={760}
        title={c('sourceDiffTitle')}
        onClose={() => setSourceDiffOpen(false)}
      >
        {(() => {
          const allLines = computeLineDiff(sourceView?.yaml ?? '', sourceValue);
          const hasChanges = allLines.some((line) => line.kind !== 'same');
          if (!hasChanges) {
            return <p className="detail">{c('sourceDiffEmpty')}</p>;
          }
          return (
            <pre className="gateway-source-diff" data-testid="gateway-source-diff">
              {allLines.map((line, idx) => (
                <span
                  key={idx}
                  className={
                    'gateway-diff-line ' +
                    (line.kind === 'add' ? 'is-add' : line.kind === 'del' ? 'is-del' : 'is-same')
                  }
                >
                  {`${line.kind === 'add' ? '+' : line.kind === 'del' ? '-' : ' '} ${line.text}\n`}
                </span>
              ))}
            </pre>
          );
        })()}
      </Modal>
      {ruleFamily ? (
        <GatewayRuleEditor
          family={ruleFamily}
          rules={
            familyDrafts[ruleFamily.family] ??
            (Array.isArray(familyViews.get(ruleFamily.family)?.rules)
              ? (familyViews.get(ruleFamily.family)?.rules as unknown[])
              : [])
          }
          readOnly={!familyViews.get(ruleFamily.family)?.writable}
          onCancel={() => setRuleFamily(null)}
          onApply={(rules) => {
            setFamilyDrafts((current) => ({ ...current, [ruleFamily.family]: rules }));
            setRuleFamily(null);
          }}
        />
      ) : null}
      {loading && !view ? <p className="detail">{c('loading')}</p> : null}
    </section>
  );
}

function capabilityKey(mode: string | undefined): Parameters<typeof c>[0] {
  switch (mode) {
    case 'managed':
      return 'capabilityManaged';
    case 'external':
      return 'capabilityExternal';
    case 'private':
      return 'capabilityPrivate';
    default:
      return 'capabilityUnavailable';
  }
}

function reasonKey(reason: string | undefined): Parameters<typeof c>[0] {
  switch (reason) {
    case 'external_rule_write_unproven':
      return 'reasonExternalRules';
    case 'external_management_key_rejected':
      return 'reasonKeyRejected';
    case 'external_target_moved':
      return 'reasonTargetMoved';
    case 'external_cpa_version_unsupported':
      return 'reasonVersionUnsupported';
    case 'private_profile_scope_frozen':
      return 'reasonPrivateFrozen';
    case 'config_journal_invalid':
      return 'reasonJournalInvalid';
    case 'original_operation_unknown':
      return 'reasonUnknownOriginal';
    case 'legacy_operation_unresolved':
      return 'reasonLegacyBlocked';
    case 'config_read_unavailable':
      return 'reasonReadUnavailable';
    case 'installation_incomplete':
      return 'reasonNotInstalled';
    default:
      return 'reasonAuthorityUnavailable';
  }
}
