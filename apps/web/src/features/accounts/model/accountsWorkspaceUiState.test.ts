import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE,
  normalizeAccountsWorkspaceUiState,
  readAccountsWorkspaceUiState,
  writeAccountsWorkspaceUiState,
} from './accountsWorkspaceUiState';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
};

const createMemoryStorage = (): StorageLike => {
  const values = new Map<string, string>();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
  };
};

const originalWindow = (globalThis as { window?: unknown }).window;

describe('accountsWorkspaceUiState', () => {
  beforeEach(() => {
    (globalThis as { window?: unknown }).window = { localStorage: createMemoryStorage() };
  });

  afterEach(() => {
    if (originalWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = originalWindow;
  });

  it('persists safe workspace preferences', () => {
    const state = {
      ...DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE,
      search: 'team-*',
      statusFilter: 'weekly_limited' as const,
      operationalFilter: 'cooldown' as const,
      accountSort: { key: 'name' as const, direction: 'asc' as const },
      pageSize: 20,
      accountDisplayMode: 'full' as const,
      layoutMode: 'grid' as const,
    };
    writeAccountsWorkspaceUiState(state);
    expect(readAccountsWorkspaceUiState()).toEqual(state);
  });

  it('drops unsupported persisted values', () => {
    expect(
      normalizeAccountsWorkspaceUiState({
        statusFilter: 'secret',
        operationalFilter: 'unknown',
        accountSort: { key: 'bad', direction: 'sideways' },
        pageSize: 999,
        quotaFocused: true,
        layoutMode: 'invalid-mode',
      })
    ).toEqual(DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE);
  });

  it('keeps an explicitly disabled quota pace preference', () => {
    expect(normalizeAccountsWorkspaceUiState({ quotaPaceEnabled: false }).quotaPaceEnabled).toBe(
      false
    );
  });

  it('defaults the quota pace to enabled for older persisted state', () => {
    expect(normalizeAccountsWorkspaceUiState({ search: 'x' }).quotaPaceEnabled).toBe(true);
    expect(normalizeAccountsWorkspaceUiState({ quotaPaceEnabled: 'no' }).quotaPaceEnabled).toBe(
      true
    );
  });
});
