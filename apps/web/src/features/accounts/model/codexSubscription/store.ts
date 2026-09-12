import { create } from 'zustand';
import { classifyCodexSubscriptionError, fetchCodexSubscription } from './fetchCodexSubscription';
import { CODEX_SUBSCRIPTION_TTL_MS, type CodexSubscriptionEntry } from './types';

type EnsureCodexSubscriptionInput = {
  accountId: string;
  authIndex: string;
  nowMs?: number;
  force?: boolean;
};

interface CodexSubscriptionState {
  entries: Record<string, CodexSubscriptionEntry>;
  ensureFresh: (input: EnsureCodexSubscriptionInput) => Promise<CodexSubscriptionEntry>;
  getEntry: (accountId: string) => CodexSubscriptionEntry;
  clearForTests: () => void;
}

const inflight = new Map<string, Promise<CodexSubscriptionEntry>>();

const idleEntry = (): CodexSubscriptionEntry => ({ status: 'idle' });

export const useCodexSubscriptionStore = create<CodexSubscriptionState>((set, get) => ({
  entries: {},
  getEntry: (accountId) => get().entries[accountId.trim()] ?? idleEntry(),
  ensureFresh: async ({ accountId, authIndex, nowMs = Date.now(), force = false }) => {
    const id = accountId.trim();
    if (!id) return idleEntry();

    const current = get().entries[id] ?? idleEntry();
    if (
      !force &&
      current.status === 'ready' &&
      nowMs - current.record.fetchedAtMs < CODEX_SUBSCRIPTION_TTL_MS
    ) {
      return current;
    }

    const existing = inflight.get(id);
    if (existing) return existing;

    const request = (async (): Promise<CodexSubscriptionEntry> => {
      if (current.status !== 'ready') {
        set((state) => ({
          entries: {
            ...state.entries,
            [id]: { status: 'loading', accountId: id, inflightSinceMs: nowMs },
          },
        }));
      }

      try {
        const record = await fetchCodexSubscription({ accountId: id, authIndex, nowMs });
        const ready: CodexSubscriptionEntry = { status: 'ready', record };
        set((state) => ({
          entries: {
            ...state.entries,
            [id]: ready,
          },
        }));
        return ready;
      } catch (error) {
        const previous = get().entries[id];
        if (previous?.status === 'ready') {
          return previous;
        }
        const failed: CodexSubscriptionEntry = {
          status: 'soft_failed',
          accountId: id,
          failedAtMs: Date.now(),
          errorKind: classifyCodexSubscriptionError(error),
        };
        set((state) => ({
          entries: {
            ...state.entries,
            [id]: failed,
          },
        }));
        return failed;
      } finally {
        inflight.delete(id);
      }
    })();

    inflight.set(id, request);
    return request;
  },
  clearForTests: () => {
    inflight.clear();
    set({ entries: {} });
  },
}));

export const ensureCodexSubscriptionFresh = (
  input: EnsureCodexSubscriptionInput
): Promise<CodexSubscriptionEntry> => useCodexSubscriptionStore.getState().ensureFresh(input);

export const getCodexSubscriptionEntry = (accountId: string): CodexSubscriptionEntry =>
  useCodexSubscriptionStore.getState().getEntry(accountId);

export const getReadyCodexSubscriptionRecord = (accountId: string | null | undefined) => {
  if (!accountId) return null;
  const entry = useCodexSubscriptionStore.getState().getEntry(accountId);
  return entry.status === 'ready' ? entry.record : null;
};
