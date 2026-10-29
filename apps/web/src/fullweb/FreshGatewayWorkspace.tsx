import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useInRouterContext } from 'react-router-dom';
import { Modal } from '../components/ui/Modal';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r } from '../i18n/configurationReference';
import { prefixRouteBase } from '../features/demo/demoMode';
import { readRoutingContext } from './routingContext';
import type { PublicApiClient } from './publicApi';
import { PublicApiError } from './publicApi';
import {
  COMMON_GATEWAY_PATHS,
  GATEWAY_TABS,
  gatewayFamilies,
  gatewayGroups,
  gatewayOwners,
  type GatewayFamilyMeta,
  type GatewayFieldMeta,
  type GatewayGroupMeta,
  type GatewayTabKey,
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
  readGatewayView,
  saveGatewayFamily,
  saveGatewayGroup,
  type GatewayDraft,
  type GatewayEdit,
  type GatewayFamilyView,
  type GatewayFieldView,
  type GatewayGroupView,
  type GatewayOperation,
  type GatewayPresence,
  type GatewayView,
} from './gatewayConfigClient';
import { GatewayRuleEditor } from './GatewayRuleEditor';
import { ruleSummary } from './gatewayRuleForm';
import { FreshExternalBlocker } from './FreshExternalBlocker';
import { blockerOf, type ExternalBlocker } from './externalState';

// The gateway configuration page. One write is one page group: the page collects
// every edited leaf of a group into a single PATCH, restores a leaf to CPA's own
// default with one DELETE, writes a rule family with one PUT, and never sends a
// second write for an unresolved one.

// A named code that only says "the outcome is not known". A network failure, the
// client abort and a code-less 5xx are treated the same way by classification
// below, because none of them can prove the write did not land.
const GATEWAY_SAVE_UNKNOWN_CODES: Record<string, true> = {
  original_operation_unknown: true,
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

export function FreshGatewayWorkspace({
  client,
  credential,
  onUnauthorized,
  onPending,
  onDirtyChange,
  onRebind,
}: {
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  onDirtyChange?: (dirty: boolean) => void;
  onRebind?: () => void;
}) {
  // Subscribe to language changes just like the original Console pages.
  const { i18n } = useTranslation();
  const language = i18n.resolvedLanguage;
  // An ownership link leaves this page for another Full Web route. It must go
  // through the mounted router so the router base path is kept; a raw href would
  // resolve below the document root and leave the Console.
  const inRouter = useInRouterContext();

  const [view, setView] = useState<GatewayView | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  // A typed External blocker from the load, kept separate from the generic
  // load failure: the page names the state and offers its own way out instead
  // of a bare "could not read".
  const [blocker, setBlocker] = useState<ExternalBlocker | null>(null);
  const [currentTab, setCurrentTab] = useState<GatewayTabKey>('overview');
  const [searchText, setSearchText] = useState('');
  const [changedOnly, setChangedOnly] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, GatewayDraft>>({});
  const [issues, setIssues] = useState<Record<string, string>>({});
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
  const issued = useRef<Set<string>>(new Set());
  const pendingWrite = useRef<Record<string, string>>({});

  // eslint-disable-next-line react-hooks/exhaustive-deps
  const groups = useMemo(() => gatewayGroups(), [language]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const families = useMemo(() => gatewayFamilies(), [language]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const owners = useMemo(() => gatewayOwners(), [language]);

  const handleError = useCallback(
    (error: unknown) => {
      if (error instanceof PublicApiError && error.code === 'unauthorized') onUnauthorized();
    },
    [onUnauthorized]
  );

  const load = useCallback(
    async (options: { keepDrafts?: boolean } = {}) => {
      setLoading(true);
      try {
        const next = await readGatewayView(client, { kind: 'admin', value: credential });
        setView(next);
        setBlocker(null);
        setLoadFailed(false);
        if (!options.keepDrafts) {
          setDrafts({});
          setIssues({});
          setGroupErrors({});
          setFamilyDrafts({});
        }
      } catch (error) {
        handleError(error);
        // A typed External code is the page's own contract: it names the state
        // and its way out, so the generic failure notice stays hidden.
        setBlocker(
          error instanceof PublicApiError ? blockerOf(error.code) : null
        );
        setLoadFailed(true);
      } finally {
        setLoading(false);
      }
    },
    [client, credential, handleError]
  );

  useEffect(() => {
    void load();
  }, [load]);

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

  const dirtyEdits = useMemo(() => {
    const edits: GatewayEdit[] = [];
    for (const field of view?.fields ?? []) {
      const draft = drafts[field.path];
      if (!draft) continue;
      if (draftEquals(field, draft)) continue;
      edits.push({ ...editFor(field, draft), path: field.path });
    }
    return edits;
  }, [drafts, view]);

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
        return !!field && !!draft && draftIssue(field, draft) !== null;
      }).length;
      if (!dirty && !errors) continue;
      bump('overview', dirty, errors);
      bump(group.tab, dirty, errors);
    }
    for (const field of view?.fields ?? []) {
      const draft = drafts[field.path];
      if (!draft || draftEquals(field, draft)) continue;
      if (COMMON_GATEWAY_PATHS[field.path] === true) bump('common', 1, 0);
    }
    for (const family of families) {
      if (!dirtyFamilies.has(family.family)) continue;
      bump('requests', 1, 0);
    }
    // A conflict is an error: the group cannot be saved again as it stands.
    for (const scope of Object.keys(groupErrors)) {
      if (scope.startsWith('rules:') || scope === 'rules') {
        bump('requests', 0, 1);
        continue;
      }
      const owning = groups.find((group) => group.id === scope);
      bump(owning?.tab ?? 'overview', 0, 1);
    }
    return metrics;
  }, [drafts, dirtyByGroup, dirtyFamilies, families, fieldViews, groups, groupErrors, view]);

  const tabRefs = useRef<Partial<Record<GatewayTabKey, HTMLButtonElement | null>>>({});

  useEffect(() => {
    onDirtyChange?.(dirtyCount > 0);
  }, [dirtyCount, onDirtyChange]);

  // A journaled unknown is still an unanswered write, so it keeps the exit lock
  // exactly as the per-field editor's own unknown result did: leaving to a
  // signed-out page would abandon an operation nobody can reconcile any more.
  const unresolvedOperation = [
    ...groups.map((group) => group.id),
    ...families.map((family) => familyScope(family.family)),
  ].some((scope) => needsReconcile(scope) && !!reconcileTarget(scope));

  useEffect(() => {
    onPending(!!saving || !!reconciling || unresolvedOperation);
  }, [onPending, reconciling, saving, unresolvedOperation]);

  useEffect(() => {
    if (!dirtyCount || typeof window === 'undefined' || typeof window.addEventListener !== 'function')
      return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirtyCount]);

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
    setIssues({});
    if (groupId === null) {
      setFamilyDrafts({});
      setGroupErrors({});
    } else {
      setGroupErrors((current) => {
        const next = { ...current };
        delete next[groupId];
        return next;
      });
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
  }

  function clearScopeDrafts(scope: string | null) {
    if (scope === null) {
      clearGroupDrafts(null);
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
    if (scope.startsWith('rules:')) return familyViews.get(scope.slice('rules:'.length))?.operation;
    return groupViews.get(scope)?.operation;
  }

  /** The ID to reconcile with: the journaled one first, then this tab's own. */
  function reconcileTarget(scope: string): string {
    return scopeOperation(scope)?.requestId ?? pendingWrite.current[scope] ?? '';
  }

  /**
   * Capture the observations this page was editing against, before a conflict
   * re-read overwrites them, so the conflict view can show old and new.
   */
  function rememberConflictBaseline(scope: string) {
    const fields: Record<string, { presence: GatewayPresence; value: unknown }> = {};
    for (const field of view?.fields ?? []) {
      fields[field.path] = { presence: field.presence, value: field.value };
    }
    setConflictSnapshot({ scope, fields });
  }

  function needsReconcile(scope: string): boolean {
    // A journaled operation outranks this tab's message: once the re-read shows
    // it settled, the button goes away even if the old warning is still shown.
    const operation = scopeOperation(scope);
    if (operation) return operation.state === 'unknown';
    return groupErrors[scope] === c('groupSaveUnknown');
  }

  function reportWriteError(scope: string, error: unknown) {
    handleError(error);
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
      rememberConflictBaseline(scope);
      setConflictGroup(scope);
      reject('groupSaveConflict');
      void load({ keepDrafts: true });
      return;
    }
    if (code === 'generation_mismatch') {
      // A typed conflict: this page read a different CPA target than it writes.
      rememberConflictBaseline(scope);
      setConflictGroup(scope);
      reject('groupSaveGenerationMismatch');
      void load({ keepDrafts: true });
      return;
    }
    if (code === 'legacy_operation_unresolved' || code === 'original_operation_unknown') {
      // Blocked until the earlier operation is settled; the group stays locked.
      reject(code === 'legacy_operation_unresolved' ? 'groupSaveLegacyBlocked' : 'groupSaveUnknown');
      void load({ keepDrafts: true });
      return;
    }
    if (isUnknownResult(error)) {
      // A network failure, the client abort or a code-less 5xx: nothing proves
      // the document is unchanged, so the result is unknown, never "failed".
      reject('groupSaveUnknown');
      void load({ keepDrafts: true });
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

  async function saveGroup(group: GatewayGroupMeta) {
    const groupView = groupViews.get(group.id);
    const edits = dirtyByGroup.get(group.id) ?? [];
    if (!groupView || !edits.length || !groupView.writable) return;
    // The private H1 profile writes one field per operation and has no delete,
    // so the Manager refuses both shapes; the page must never build them.
    if (limitIssue(view?.profileLimits, edits.length)) return;
    if (!allowsDefaultEdit(view?.profileLimits) && edits.some((edit) => edit.action === 'default')) {
      setGroupErrors((current) => ({ ...current, [group.id]: c('groupSaveNoDefault') }));
      return;
    }
    const problems: Record<string, string> = {};
    for (const edit of edits) {
      const field = fieldViews.get(edit.path);
      const draft = drafts[edit.path];
      if (!field || !draft) continue;
      const issue = draftIssue(field, draft);
      if (issue) problems[edit.path] = c(issue === 'required' ? 'valueRequired' : 'valueInteger');
    }
    if (Object.keys(problems).length) {
      setIssues((current) => ({ ...current, ...problems }));
      return;
    }
    const requestId = newGatewayRequestId(issued.current);
    pendingWrite.current[group.id] = requestId;
    setSaving(group.id);
    setGroupErrors((current) => {
      const next = { ...current };
      delete next[group.id];
      return next;
    });
    setConflictGroup('');
    try {
      const result = await saveGatewayGroup(client, { kind: 'admin', value: credential }, group.id, {
        requestId,
        expected: { generation: view?.generation ?? '', baseline: groupView.baseline },
        edits,
      });
      delete pendingWrite.current[group.id];
      if (result.operation.state === 'failed') {
        reportWriteError(group.id, new PublicApiError('http', 422, undefined, 'cpa_rejected_config'));
      } else {
        clearGroupDrafts(group.id);
      }
      await load({ keepDrafts: true });
    } catch (error) {
      reportWriteError(group.id, error);
    } finally {
      setSaving('');
    }
  }

  async function saveFamily(family: GatewayFamilyMeta) {
    const rules = dirtyFamilies.get(family.family);
    const familyView = familyViews.get(family.family);
    if (!rules || !familyView || !familyView.writable) return;
    const scope = familyScope(family.family);
    const requestId = newGatewayRequestId(issued.current);
    pendingWrite.current[scope] = requestId;
    setSaving(scope);
    setGroupErrors((current) => {
      const next = { ...current };
      delete next[scope];
      return next;
    });
    try {
      const result = await saveGatewayFamily(client, { kind: 'admin', value: credential }, family.family, {
        requestId,
        expected: { generation: view?.generation ?? '', baseline: familyView.baseline },
        rules,
      });
      delete pendingWrite.current[scope];
      if (result.operation.state === 'failed') {
        reportWriteError(scope, new PublicApiError('http', 422, undefined, 'cpa_rejected_config'));
      } else {
        clearFamilyDraft(family.family);
      }
      await load({ keepDrafts: true });
    } catch (error) {
      reportWriteError(scope, error);
    } finally {
      setSaving('');
    }
  }

  async function queryOriginal(scope: string) {
    const requestId = reconcileTarget(scope);
    if (!requestId) {
      await load({ keepDrafts: true });
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
      delete pendingWrite.current[scope];
      // The journal is now authoritative, so the earlier "known unknown" warning
      // must not outlive the evidence that answered it.
      setGroupErrors((current) => {
        if (!current[scope]) return current;
        const next = { ...current };
        delete next[scope];
        return next;
      });
      if (result.operation.state === 'persisted') {
        clearScopeDrafts(scope);
      }
      await load({ keepDrafts: true });
    } catch (error) {
      handleError(error);
      if (!(error instanceof PublicApiError) || error.kind !== 'http') {
        setGroupErrors((current) => ({ ...current, [scope]: c('groupSaveUnknown') }));
      }
    } finally {
      setReconciling('');
    }
  }

  const query = searchText.trim().toLowerCase();
  const visibleGroups = useMemo(() => {
    const matches = (group: GatewayGroupMeta, field: GatewayFieldMeta) => {
      // A search is global: it is not confined to the tab it started from.
      if (query) {
        const haystack = [group.titleKey ? c(group.titleKey) : '', r(field.labelKey), field.path]
          .join(' ')
          .toLowerCase();
        return haystack.includes(query);
      }
      if (changedOnly) return (dirtyByGroup.get(group.id) ?? []).some((edit) => edit.path === field.path);
      if (currentTab === 'common') return COMMON_GATEWAY_PATHS[field.path] === true;
      if (currentTab === 'overview' || currentTab === 'owners') return false;
      return group.tab === currentTab;
    };
    return groups
      .map((group) => ({ ...group, fields: group.fields.filter((field) => matches(group, field)) }))
      .filter((group) => group.fields.length > 0);
  }, [changedOnly, currentTab, dirtyByGroup, groups, query]);

  // The payload rule lists live on the request tab, exactly where the accepted
  // design placed them; a search still reaches them from anywhere.
  const visibleFamilies = useMemo(
    () =>
      families.filter((family) => {
        if (query) return [c(family.labelKey), family.path].join(' ').toLowerCase().includes(query);
        if (changedOnly) return dirtyFamilies.has(family.family);
        if (currentTab === 'overview' || currentTab === 'owners') return false;
        return currentTab === 'requests';
      }),
    [changedOnly, currentTab, dirtyFamilies, families, query]
  );

  const capability = view?.capability;
  const pageReason = capability?.reason;
  const readOnlyNotice = capability
    ? capability.writable
      ? capability.rulesWritable
        ? ''
        : c('reasonExternalRules')
      : c(reasonKey(pageReason))
    : '';

  /** Old (this tab's baseline) and new (the re-read value) for a conflict. */
  function conflictRows() {
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
    const dirty = !draftEquals(field, draft);
    const disabled = !field.editable || saving !== '';
    const dependsOn = meta?.depends ? fieldViews.get(meta.depends) : undefined;
    const dependencyOff = !!dependsOn && dependsOn.presence === 'present' && dependsOn.value === false;
    const locked = !field.editable || dependencyOff;
    const label = meta ? r(meta.labelKey) : field.path;
    const help = meta ? r(meta.helpKey) : '';
    const issue = issues[field.path];
    // The private profile writes one field per operation and cannot delete a
    // leaf, so restoring a default is not offered on it at all.
    const canUseDefault = allowsDefaultEdit(view?.profileLimits);
    // A `*bool` leaf whose unset state resolves to a CPA default is edited as
    // inherit / on / off: an explicit false is a real change, not "no change".
    const inheritable = isInheritable(field);
    const state = boolState(draft);
    const testId = `gateway-field-${field.path}`;
    return (
      <div
        className={'config-field' + (dirty ? ' field-dirty' : '') + (locked ? ' field-locked' : '')}
        key={field.path}
        data-testid={testId}
        data-dirty={dirty ? 'true' : 'false'}
      >
        <div className="field-head">
          <span>{label}</span>
          {meta?.risk ? <span className="badge warn">{c('risk')}</span> : null}
          <code>{field.path}</code>
        </div>
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
              <span>
                {draft.action === 'set' && draft.value === true ? c('valueOn') : c('valueOff')}
              </span>
            </label>
          ) : field.kind === 'select' ? (
            <select
              aria-label={label}
              disabled={disabled}
              value={draft.action === 'default' ? '' : String(draft.value ?? '')}
              onChange={(event) => {
                if (event.target.value === '') {
                  setDraft(field.path, { action: 'default', value: '' });
                  return;
                }
                setDraft(field.path, { action: 'set', value: event.target.value });
              }}
            >
              {/* An image-mode value CPA already persists is shown, never offered
                  as a new draft: "passthrough" postdates the accepted inventory. */}
              {meta?.options?.map((option) =>
                option.readOnly && option.value !== String(field.value ?? '') ? null : (
                  <option key={option.value} value={option.value} disabled={option.readOnly}>
                    {r(option.labelKey)}
                  </option>
                )
              )}
              {meta?.options?.some((option) => option.value === String(field.value ?? '')) ? null : (
                <option value={String(field.value ?? '')}>
                  {formatted(field.presence, field.path, field.value)}
                </option>
              )}
            </select>
          ) : (
            <input
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
              className="btn btn-secondary btn-sm"
              disabled={disabled}
              onClick={() => setDraft(field.path, { action: 'default', value: '' })}
            >
              {c('fieldUseDefault')}
            </button>
          ) : null}
        </div>
        <p className="detail">{help}</p>
        <p className="detail">
          {c('fieldCurrentValue')} <strong>{formatted(field.presence, field.path, field.value)}</strong>
          {dirty
            ? ' · ' +
              c('fieldDraftValue') +
              ' ' +
              (draft.action === 'default'
                ? inheritable
                  ? c(field.inherit === true ? 'valueInheritOn' : 'valueInheritOff')
                  : c('valueMissing')
                : formatted('present', field.path, draft.value))
            : ''}
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
    const fieldViewsOfGroup = members
      .map((meta) => fieldViews.get(meta.path))
      .filter((field): field is GatewayFieldView => !!field);
    const singleEdit = limitIssue(view?.profileLimits, edits.length);
    return (
      <section className="card" key={group.id} data-testid={`gateway-group-${group.id}`}>
        <header className="card-head">
          <h2>{c(group.titleKey)}</h2>
          <span className="badge">{c('fieldCount', members.length)}</span>
        </header>
        <p className="detail">{c(group.helpKey)}</p>
        {fieldViewsOfGroup.map((field) => renderField(field))}
        <div className="group-footer">
          <span className="detail">
            {edits.length ? c('groupDirtyCount', edits.length) : c('noDrafts')}
          </span>
          <div className="actions">
            <button
              className="btn btn-secondary btn-sm"
              disabled={!edits.length}
              onClick={() => {
                setDiscardGroup(group.id);
              }}
            >
              {c('discard')}
            </button>
            <button
              className="btn btn-primary btn-sm"
              disabled={!edits.length || !groupView?.writable || saving !== '' || singleEdit !== null}
              onClick={() => void saveGroup(group)}
            >
              {saving === group.id ? c('groupSaving') : c('groupSave')}
            </button>
          </div>
        </div>
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
    return (
      <section className="card" key={family.family} data-testid={`gateway-family-${family.family}`}>
        <header className="card-head">
          <h2>{c(family.labelKey)}</h2>
          <span className="badge">{c('ruleCount', rules.length)}</span>
        </header>
        <p className="detail">{c(family.hintKey)}</p>
        <code>{family.path}</code>
        {rules.length ? (
          <ul className="rule-preview">
            {rules.map((rule, index) => (
              <li key={index}>{ruleSummary(rule, family)}</li>
            ))}
          </ul>
        ) : (
          <p className="detail">{c('ruleEmpty')}</p>
        )}
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
            disabled={!dirty || !familyView?.writable || saving !== ''}
            onClick={() => void saveFamily(family)}
          >
            {saving === scope ? c('groupSaving') : c('groupSave')}
          </button>
        </div>
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
        {familyView?.readOnly && !blocked ? (
          <p className="notice warn">{c(reasonKey(familyView.reason))}</p>
        ) : null}
      </section>
    );
  }

  return (
    <section className="config-workspace" data-testid="gateway-config-workspace">
      <header className="workspace-head">
        <div>
          <h1>{c('gateway')}</h1>
          <p className="detail">{c('gatewayHelp')}</p>
        </div>
        <div className="actions">
          <span className="badge" data-testid="gateway-capability">
            {c(capabilityKey(capability?.mode))}
          </span>
          <button className="btn btn-secondary btn-sm" disabled={loading} onClick={() => void load()}>
            {c('reload')}
          </button>
        </div>
      </header>
      <p className="notice">{c('catalogNotice')}</p>
      {blocker ? (
        <FreshExternalBlocker
          state={blocker}
          onRebind={onRebind}
          onReload={() => void load()}
          onDismiss={() => setBlocker(null)}
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
      {readOnlyNotice ? (
        <p className="alert alert-warn" data-testid="gateway-readonly">
          {readOnlyNotice}
        </p>
      ) : null}
      {conflictGroup ? (
        <div className="alert alert-warn" role="alert" data-testid="gateway-conflict">
          <strong>{c('conflictTitle')}</strong> {c('conflictHelp')}
          <ul className="diff-list" data-testid="gateway-conflict-diff">
            {conflictRows().map((row) => (
              <li key={row.path}>
                <code>{row.path}</code>
                <span>{row.oldValue}</span>
                <span aria-hidden="true">→</span>
                <span>{row.newValue}</span>
                <span aria-hidden="true">·</span>
                <span>
                  {c('fieldDraftValue')} {row.draftValue}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <div className="workspace-toolbar">
        <label className="search">
          <span>{c('search')}</span>
          <input
            type="search"
            value={searchText}
            placeholder={c('searchPlaceholder')}
            onChange={(event) => setSearchText(event.target.value)}
          />
        </label>
        <label className="toggle">
          <input
            type="checkbox"
            checked={changedOnly}
            onChange={(event) => setChangedOnly(event.target.checked)}
          />
          <span>{c('changedOnly')}</span>
        </label>
        <span className="badge" data-testid="gateway-dirty-count">
          {c('dirtyCount', dirtyCount)}
        </span>
        <button
          className="btn btn-secondary btn-sm"
          disabled={!dirtyCount}
          onClick={() => setDiffOpen(true)}
        >
          {c('viewChanges')}
        </button>
        <button
          className="btn btn-secondary btn-sm"
          disabled={!dirtyCount}
          onClick={() => setDiscardAll(true)}
        >
          {c('discard')}
        </button>
      </div>
      <nav
        className="gateway-tabs"
        role="tablist"
        aria-label={c('gateway')}
        onKeyDown={(event) => {
          // Arrow/Home/End move between tabs, as the accepted prototype does.
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
          event.preventDefault();
          const index = GATEWAY_TABS.indexOf(currentTab);
          const next =
            event.key === 'Home'
              ? 0
              : event.key === 'End'
                ? GATEWAY_TABS.length - 1
                : (index + (event.key === 'ArrowRight' ? 1 : -1) + GATEWAY_TABS.length) %
                  GATEWAY_TABS.length;
          const target = GATEWAY_TABS[next];
          setCurrentTab(target);
          tabRefs.current[target]?.focus();
        }}
      >
        {GATEWAY_TABS.map((tab) => {
          const metric = tabMetrics[tab] ?? { dirty: 0, errors: 0 };
          return (
            <button
              key={tab}
              ref={(node) => {
                tabRefs.current[tab] = node;
              }}
              role="tab"
              id={`gateway-tab-${tab}`}
              data-testid={`gateway-tab-${tab}`}
              aria-selected={currentTab === tab}
              aria-controls="gateway-tabpanel"
              tabIndex={currentTab === tab ? 0 : -1}
              aria-label={
                metric.errors
                  ? `${c(tab)} · ${c('validationIssues', metric.errors)}`
                  : metric.dirty
                    ? `${c(tab)} · ${c('unsaved', metric.dirty)}`
                    : c(tab)
              }
              className={'btn btn-secondary btn-sm' + (currentTab === tab ? ' active' : '')}
              onClick={() => setCurrentTab(tab)}
            >
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
            </button>
          );
        })}
      </nav>
      <div
        id="gateway-tabpanel"
        role="tabpanel"
        aria-labelledby={`gateway-tab-${currentTab}`}
        className="workspace-body"
      >
        {currentTab === 'overview' ? (
          <div className="overview-grid" data-testid="gateway-overview">
            <section className="card">
              <h2>{c('overview')}</h2>
              <p className="detail">{c('overviewHelp')}</p>
              <dl className="stat-grid">
                <div>
                  <dt>{c('fields')}</dt>
                  <dd>{view?.fields.length ?? 0}</dd>
                </div>
                <div>
                  <dt>{c('ruleFamilies')}</dt>
                  <dd>{view?.families.length ?? 0}</dd>
                </div>
                <div>
                  <dt>{c('ownerGroups')}</dt>
                  <dd>{owners.length}</dd>
                </div>
                <div>
                  <dt>{c('dirty')}</dt>
                  <dd>{dirtyCount}</dd>
                </div>
              </dl>
            </section>
            <section className="card" key="rules">
              <header className="card-head">
                <h2>{c('ruleFamilies')}</h2>
                <span className="badge">{c('ruleCount', families.length)}</span>
              </header>
              <p className="detail">{c('familyDefaultHelp')}</p>
              <div className="actions">
                <button className="btn btn-secondary btn-sm" onClick={() => setCurrentTab('requests')}>
                  {c('gotoGroup')}
                </button>
              </div>
            </section>
            <section className="card" key="runtime">
              <header className="card-head">
                <h2>{c('ownerDetailRuntime')}</h2>
                <span className="badge">{c('independent')}</span>
              </header>
              <code>credentials.concurrency / credentials.in-flight</code>
              <p className="detail">{c('runtimeLinkHelp')}</p>
              <OwnerTargetLink inRouter={inRouter} target="/system/runtime" />
            </section>
            {groups.map((group) => (
              <section className="card" key={group.id}>
                <header className="card-head">
                  <h2>{c(group.titleKey)}</h2>
                  <span className="badge">{c('fieldCount', group.fields.length)}</span>
                </header>
                <p className="detail">{c(group.helpKey)}</p>
                <div className="actions">
                  <button className="btn btn-secondary btn-sm" onClick={() => setCurrentTab(group.tab)}>
                    {c('gotoGroup')}
                  </button>
                </div>
              </section>
            ))}
          </div>
        ) : null}
        {currentTab === 'owners' ? (
          <div className="owner-grid" data-testid="gateway-owners">
            {owners.map((owner) => (
              <section className="card" key={owner.path} data-testid={`gateway-owner-${owner.path}`}>
                <header className="card-head">
                  <h2>{r(owner.ownerKey)}</h2>
                </header>
                <code>{owner.path}</code>
                <p className="detail">{c(owner.detailKey)}</p>
                <details>
                  <summary>{c('ownerDetail')}</summary>
                  <ul>
                    {owner.fields.map((field) => (
                      <li key={field}>
                        <code>{field}</code>
                      </li>
                    ))}
                  </ul>
                </details>
                {owner.target ? (
                  <OwnerTargetLink inRouter={inRouter} target={owner.target} />
                ) : (
                  <p className="detail">{c('ownerUnavailable')}</p>
                )}
              </section>
            ))}
          </div>
        ) : null}
        {currentTab !== 'overview' && currentTab !== 'owners'
          ? visibleGroups.map((group) => renderGroupCard(group))
          : null}
        {visibleFamilies.length
          ? visibleFamilies.map((family) => renderFamilyCard(family))
          : null}
        {currentTab !== 'overview' && currentTab !== 'owners' && !visibleGroups.length && !visibleFamilies.length ? (
          <p className="detail" data-testid="gateway-no-matches">
            {c('noMatches')}
          </p>
        ) : null}
      </div>
      <Modal open={diffOpen} width={720} title={c('diffTitle')} onClose={() => setDiffOpen(false)}>
        <p className="detail">{c('diffHelp')}</p>
        {dirtyEdits.length ? (
          <ul className="diff-list" data-testid="gateway-diff">
            {dirtyEdits.map((edit) => {
              const field = fieldViews.get(edit.path);
              return (
                <li key={edit.path}>
                  <code>{edit.path}</code>
                  <span>{field ? formatted(field.presence, field.path, field.value) : ''}</span>
                  <span aria-hidden="true">→</span>
                  <span>
                    {edit.action === 'default'
                      ? c('valueMissing')
                      : field
                        ? formatted('present', edit.path, edit.value)
                        : String(edit.value)}
                  </span>
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
        <p className="detail">{c('discardHelp')}</p>
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


/**
 * An ownership link to another Full Web route. Inside the Console router a Link
 * resolves against the verified basename, so the page never escapes `/console`.
 * The membership panel can also be mounted without a router (unit mounts), where
 * a plain anchor against the same basename is the only safe form.
 */
function OwnerTargetLink({ inRouter, target }: { inRouter: boolean; target: string }) {
  if (inRouter) {
    return (
      <Link className="btn btn-secondary btn-sm" to={target} data-testid={`gateway-owner-link-${target}`}>
        {c('openOwner')}
      </Link>
    );
  }
  // No router above this mount: apply the verified base path by hand, because a
  // bare href would resolve under the document root and leave the Console. A
  // non-browser mount has no verified base path to prefix with.
  const basePath =
    typeof document === 'undefined' ? '' : readRoutingContext(document)?.basePath ?? '';
  return (
    <a
      className="btn btn-secondary btn-sm"
      href={basePath ? prefixRouteBase(target, basePath) : target}
      data-testid={`gateway-owner-link-${target}`}
    >
      {c('openOwner')}
    </a>
  );
}
