import { describe, expect, it } from 'vitest';
import {
  applyOAuthAliasWritePlans,
  createSerialAsyncQueue,
  findChannelMappings,
  formatOAuthAliasPreview,
  isMissingOrMethodNotAllowedStatus,
  mergeOAuthAliasLink,
  normalizeOAuthAliasEntries,
  planOAuthAliasRename,
} from './oauthAliasValidation';

describe('applyOAuthAliasWritePlans', () => {
  it('rolls back already-applied channels when a later write fails', async () => {
    const stored = new Map([
      ['claude', [{ name: 'claude-upstream', alias: 'shared' }]],
      ['codex', [{ name: 'codex-upstream', alias: 'shared' }]],
    ]);
    const writes: string[] = [];

    let codexWrites = 0;
    const persist = async (channel: string, mappings: Array<{ name: string; alias: string }>) => {
      writes.push(channel);
      stored.set(channel, mappings);
      if (channel === 'codex' && codexWrites++ === 0) {
        throw new Error('response lost after write');
      }
    };

    await expect(
      applyOAuthAliasWritePlans(
        [
          {
            channel: 'claude',
            previousMappings: [{ name: 'claude-upstream', alias: 'shared' }],
            nextMappings: [],
          },
          {
            channel: 'codex',
            previousMappings: [{ name: 'codex-upstream', alias: 'shared' }],
            nextMappings: [],
          },
        ],
        persist
      )
    ).rejects.toThrow('response lost after write');

    expect(writes).toEqual(['claude', 'codex', 'codex', 'claude']);
    expect(stored.get('claude')).toEqual([{ name: 'claude-upstream', alias: 'shared' }]);
    expect(stored.get('codex')).toEqual([{ name: 'codex-upstream', alias: 'shared' }]);
  });
});

describe('normalizeOAuthAliasEntries', () => {
  it('accepts multiple aliases for the same upstream name', () => {
    const result = normalizeOAuthAliasEntries([
      { name: 'claude-sonnet-4-5-20250929', alias: 'cs4.5', fork: true },
      { name: 'claude-sonnet-4-5-20250929', alias: 'sonnet', fork: true },
    ]);

    expect(result.accepted).toHaveLength(2);
    expect(result.rejectedCount).toBe(0);
    expect(result.issues).toEqual([]);
  });

  it('rejects name === alias and duplicate aliases', () => {
    const result = normalizeOAuthAliasEntries([
      { name: 'claude-sonnet-4-5', alias: 'claude-sonnet-4-5' },
      { name: 'upstream-a', alias: 'shared' },
      { name: 'upstream-b', alias: 'SHARED' },
      { name: '', alias: 'only-alias' },
    ]);

    expect(result.accepted).toEqual([{ name: 'upstream-a', alias: 'shared' }]);
    expect(result.rejectedCount).toBe(2);
    expect(result.incompleteCount).toBe(1);
    expect(result.issues.map((issue) => issue.code)).toEqual([
      'same_as_name',
      'duplicate_alias',
      'empty_fields',
    ]);
  });

  it('preserves fork and forceMapping flags', () => {
    const result = normalizeOAuthAliasEntries([
      {
        name: 'gpt-5',
        alias: 'g5',
        fork: true,
        forceMapping: true,
      },
    ]);

    expect(result.accepted).toEqual([
      {
        name: 'gpt-5',
        alias: 'g5',
        fork: true,
        forceMapping: true,
      },
    ]);
  });
});

describe('findChannelMappings', () => {
  it('matches channels case-insensitively', () => {
    const result = findChannelMappings(
      {
        Claude: [{ name: 'a', alias: 'b' }],
      },
      'claude'
    );

    expect(result.channelKey).toBe('Claude');
    expect(result.mappings).toEqual([{ name: 'a', alias: 'b' }]);
  });
});

describe('formatOAuthAliasPreview', () => {
  it('summarizes mappings and overflow count', () => {
    const preview = formatOAuthAliasPreview(
      [
        { name: 'a', alias: '1' },
        { name: 'b', alias: '2' },
        { name: 'c', alias: '3' },
        { name: 'd', alias: '4' },
      ],
      2
    );

    expect(preview).toBe('a → 1 · b → 2 · +2');
  });
});

describe('createSerialAsyncQueue', () => {
  it('runs tasks sequentially even when started together', async () => {
    const enqueue = createSerialAsyncQueue();
    const order: number[] = [];

    const first = enqueue(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
      order.push(1);
      return 'one';
    });
    const second = enqueue(async () => {
      order.push(2);
      return 'two';
    });

    await expect(Promise.all([first, second])).resolves.toEqual(['one', 'two']);
    expect(order).toEqual([1, 2]);
  });

  it('serializes GET-merge-save style concurrent alias updates without lost writes', async () => {
    const enqueue = createSerialAsyncQueue();
    let stored: Array<{ name: string; alias: string; fork?: boolean }> = [];

    const link = async (name: string, alias: string) =>
      enqueue(async () => {
        const latest = [...stored];
        await new Promise((resolve) => setTimeout(resolve, 15));
        const mergeResult = mergeOAuthAliasLink(latest, name, alias);
        if (mergeResult.kind !== 'updated') return;
        stored = mergeResult.mappings;
      });

    await Promise.all([link('upstream-a', 'alias-a'), link('upstream-b', 'alias-b')]);

    expect(stored).toEqual([
      { name: 'upstream-a', alias: 'alias-a', fork: true },
      { name: 'upstream-b', alias: 'alias-b', fork: true },
    ]);
  });
});

describe('mergeOAuthAliasLink', () => {
  it('rejects identity and duplicate aliases', () => {
    expect(mergeOAuthAliasLink([], 'same', 'same')).toEqual({
      kind: 'rejected',
      reason: 'same_as_name',
      alias: 'same',
    });
    expect(mergeOAuthAliasLink([{ name: 'a', alias: 'shared' }], 'b', 'SHARED')).toEqual({
      kind: 'rejected',
      reason: 'duplicate_alias',
      alias: 'SHARED',
    });
  });
});

describe('planOAuthAliasRename', () => {
  it('validates all channels before returning plans', () => {
    const ok = planOAuthAliasRename(
      {
        claude: [{ name: 'upstream-a', alias: 'shared' }],
        codex: [{ name: 'upstream-b', alias: 'shared' }],
      },
      'shared',
      'renamed'
    );
    expect(ok.ok).toBe(true);
    if (ok.ok) {
      expect(ok.plans).toHaveLength(2);
      expect(ok.plans.map((plan) => plan.channel).sort()).toEqual(['claude', 'codex']);
      expect(ok.plans.every((plan) => plan.nextMappings[0]?.alias === 'renamed')).toBe(true);
    }

    const conflict = planOAuthAliasRename(
      {
        claude: [{ name: 'upstream-a', alias: 'shared' }],
        codex: [
          { name: 'upstream-b', alias: 'shared' },
          { name: 'upstream-c', alias: 'taken' },
        ],
      },
      'shared',
      'taken'
    );
    expect(conflict).toEqual({
      ok: false,
      reason: 'duplicate_alias',
      alias: 'taken',
      channel: 'codex',
    });
  });

  it('rejects renames that would make alias equal source name', () => {
    const result = planOAuthAliasRename(
      {
        claude: [{ name: 'renamed', alias: 'shared' }],
      },
      'shared',
      'renamed'
    );
    expect(result).toEqual({
      ok: false,
      reason: 'same_as_name',
      alias: 'renamed',
      channel: 'claude',
    });
  });
});

describe('isMissingOrMethodNotAllowedStatus', () => {
  it('only allows safe DELETE fallbacks', () => {
    expect(isMissingOrMethodNotAllowedStatus(404)).toBe(true);
    expect(isMissingOrMethodNotAllowedStatus(405)).toBe(true);
    expect(isMissingOrMethodNotAllowedStatus(500)).toBe(false);
    expect(isMissingOrMethodNotAllowedStatus(undefined)).toBe(false);
  });
});
