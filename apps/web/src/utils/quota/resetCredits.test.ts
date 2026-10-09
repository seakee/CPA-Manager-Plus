import { describe, expect, it } from 'vitest';
import {
  normalizeCodexResetCreditsPayload,
  resolveCodexResetCreditsObservationCount,
  resolveCodexResetCreditsCountEvidenceAtMs,
  resolveCodexResetCreditsDetailEvidenceAtMs,
  mergeCodexResetCreditsEvidence,
  shouldAutoFetchCodexResetCreditDetails,
  buildCodexResetCreditAutoFetchSignature,
} from './resetCredits';

describe('normalizeCodexResetCreditsPayload', () => {
  it('normalizes available Codex rate limit reset credits', () => {
    const result = normalizeCodexResetCreditsPayload({
      available_count: '2',
      credits: [
        {
          id: 123,
          reset_type: 'codex_rate_limits',
          status: 'available',
          granted_at: '2026-06-01T00:00:00Z',
          expires_at: '2026-06-30T00:00:00Z',
        },
        {
          id: 'used-credit',
          reset_type: 'codex_rate_limits',
          status: 'used',
          expires_at: '2026-06-30T00:00:00Z',
        },
        {
          id: 'other-credit',
          reset_type: 'other',
          status: 'available',
          expires_at: '2026-06-30T00:00:00Z',
        },
      ],
    });

    expect(result).toEqual({
      availableCount: 2,
      creditsObserved: true,
      invalidPayload: false,
      credits: [
        {
          id: '123',
          status: 'available',
          grantedAt: '2026-06-01T00:00:00Z',
          expiresAt: '2026-06-30T00:00:00Z',
        },
      ],
    });
  });

  it('parses JSON string payloads and supports camelCase fields', () => {
    const result = normalizeCodexResetCreditsPayload(
      JSON.stringify({
        availableCount: 1,
        credits: [
          {
            id: 'credit-1',
            resetType: 'codex_rate_limits',
            status: 'available',
            grantedAt: '2026-06-01T00:00:00Z',
            expiresAt: '2026-06-30T00:00:00Z',
          },
        ],
      })
    );

    expect(result.availableCount).toBe(1);
    expect(result.credits[0]?.id).toBe('credit-1');
    expect(result.creditsObserved).toBe(true);
    expect(result.invalidPayload).toBe(false);
  });

  it('marks invalid payloads', () => {
    expect(normalizeCodexResetCreditsPayload('not-json')).toEqual({
      availableCount: null,
      credits: [],
      creditsObserved: false,
      invalidPayload: true,
    });

    expect(normalizeCodexResetCreditsPayload({ unknown: true })).toEqual({
      availableCount: null,
      credits: [],
      creditsObserved: false,
      invalidPayload: true,
    });
  });

  it('Test 1: handles explicit empty credits array as observed detail', () => {
    const result = normalizeCodexResetCreditsPayload({ credits: [] });
    expect(result).toEqual({
      availableCount: null,
      credits: [],
      creditsObserved: true,
      invalidPayload: false,
    });
  });

  it('Test 2: handles count-only payload without credits array as unobserved detail', () => {
    const result = normalizeCodexResetCreditsPayload({ available_count: 2 });
    expect(result).toEqual({
      availableCount: 2,
      credits: [],
      creditsObserved: false,
      invalidPayload: false,
    });
  });

  it('Test 3: marks payload with invalid credits type and missing available_count as invalid', () => {
    const result = normalizeCodexResetCreditsPayload({ credits: 'invalid' });
    expect(result).toEqual({
      availableCount: null,
      credits: [],
      creditsObserved: false,
      invalidPayload: true,
    });
  });

  it('Test 4: marks payload with invalid available_count and missing credits as invalid', () => {
    const result = normalizeCodexResetCreditsPayload({ available_count: 'invalid' });
    expect(result).toEqual({
      availableCount: null,
      credits: [],
      creditsObserved: false,
      invalidPayload: true,
    });
  });

  it('Test 5: preserves valid available_count even when credits field is malformed', () => {
    const result = normalizeCodexResetCreditsPayload({
      available_count: 2,
      credits: 'invalid',
    });
    expect(result).toEqual({
      availableCount: 2,
      credits: [],
      creditsObserved: false,
      invalidPayload: false,
    });
  });

  it('Test 6: preserves valid available_count and explicit empty credits array independently', () => {
    const result = normalizeCodexResetCreditsPayload({
      available_count: 2,
      credits: [],
    });
    expect(result).toEqual({
      availableCount: 2,
      credits: [],
      creditsObserved: true,
      invalidPayload: false,
    });
  });
});

describe('resolveCodexResetCreditsObservationCount', () => {
  const creditA = { id: 'A', status: 'available', grantedAt: '', expiresAt: '2026-10-04' };

  it('verifies count resolver matrix with creditsObserved presence', () => {
    // availableCount=2, creditsObserved=false -> count=2
    expect(resolveCodexResetCreditsObservationCount(2, [], false)).toBe(2);

    // availableCount=null, creditsObserved=true, credits=[] -> count=0
    expect(resolveCodexResetCreditsObservationCount(null, [], true)).toBe(0);

    // availableCount=null, creditsObserved=true, credits=[A] -> count=1
    expect(resolveCodexResetCreditsObservationCount(null, [creditA], true)).toBe(1);

    // availableCount=null, creditsObserved=false, normalized credits=[] -> count=null
    expect(resolveCodexResetCreditsObservationCount(null, [], false)).toBeNull();
  });
});

describe('resolveCodexResetCreditsCountEvidenceAtMs and resolveCodexResetCreditsDetailEvidenceAtMs', () => {
  it('prefers resetCreditsCountEvidenceAtMs and falls back to resetCreditsEvidenceAtMs or fetchedAtMs only when count is a finite number', () => {
    // count=2 + resetCreditsCountEvidenceAtMs=100 -> 100
    expect(
      resolveCodexResetCreditsCountEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: 2,
        resetCreditsCountEvidenceAtMs: 100,
        resetCreditsEvidenceAtMs: 50,
      })
    ).toBe(100);

    // count=2 + legacy resetCreditsEvidenceAtMs=90 -> 90
    expect(
      resolveCodexResetCreditsCountEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: 2,
        resetCreditsEvidenceAtMs: 90,
      })
    ).toBe(90);

    // count=2 + fetchedAtMs=80 -> 80
    expect(
      resolveCodexResetCreditsCountEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: 2,
        fetchedAtMs: 80,
      })
    ).toBe(80);

    // count=null + fetchedAtMs=80 -> null
    expect(
      resolveCodexResetCreditsCountEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: null,
        fetchedAtMs: 80,
      })
    ).toBeNull();

    // count=undefined + observedAtMs=80 -> null
    expect(
      resolveCodexResetCreditsCountEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: undefined,
        observedAtMs: 80,
      })
    ).toBeNull();
  });

  it('resolves detail evidence when resetCreditsDetailEvidenceAtMs is present', () => {
    expect(
      resolveCodexResetCreditsDetailEvidenceAtMs({
        resetCreditsDetailEvidenceAtMs: 200,
        rateLimitResetCredits: [{ id: '1', status: 'available', grantedAt: '', expiresAt: '2026-10-01' }],
      })
    ).toBe(200);
  });

  it('falls back to legacy resetCreditsEvidenceAtMs only if credits array has non-empty records (Test 10)', () => {
    // Non-empty credits: fallback allowed
    expect(
      resolveCodexResetCreditsDetailEvidenceAtMs({
        resetCreditsEvidenceAtMs: 150,
        rateLimitResetCredits: [{ id: '1', status: 'available', grantedAt: '', expiresAt: '2026-10-01' }],
      })
    ).toBe(150);

    // Empty credits with count > 0: do NOT treat as fresh detail evidence
    expect(
      resolveCodexResetCreditsDetailEvidenceAtMs({
        rateLimitResetCreditsAvailableCount: 2,
        resetCreditsEvidenceAtMs: 150,
        rateLimitResetCredits: [],
      })
    ).toBeNull();
  });
});

describe('mergeCodexResetCreditsEvidence', () => {
  const creditA = { id: 'A', status: 'available', grantedAt: '', expiresAt: '2026-10-04' };
  const creditB = { id: 'B', status: 'available', grantedAt: '', expiresAt: '2026-10-05' };

  it('Test 2: preserves previous detail when summary count is unchanged', () => {
    const previous = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsCountEvidenceAtMs: 1000,
      resetCreditsDetailStale: false,
    };

    const summaryIncoming = {
      rateLimitResetCreditsAvailableCount: 2,
      resetCreditsCountEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(previous, summaryIncoming, {
      isFullDetailObservation: false,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.rateLimitResetCredits).toEqual([creditA, creditB]);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(1000);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test 3: marks detail stale and clears display credits when summary count changes', () => {
    const previous = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsCountEvidenceAtMs: 1000,
    };

    const summaryIncoming = {
      rateLimitResetCreditsAvailableCount: 1,
      resetCreditsCountEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(previous, summaryIncoming, {
      isFullDetailObservation: false,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(1);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.rateLimitResetCredits).toEqual([]);
    expect(merged.resetCreditsDetailStale).toBe(true);
    // Detail evidence timestamp is retained as historical metadata without being advanced
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(1000);
  });

  it('Test 4: sets count=0, clears credits, marks detailStale=false on summary count -> 0', () => {
    const previous = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsDetailEvidenceAtMs: 1000,
    };

    const summaryIncoming = {
      rateLimitResetCreditsAvailableCount: 0,
      resetCreditsCountEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(previous, summaryIncoming, {
      isFullDetailObservation: false,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(0);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.rateLimitResetCredits).toEqual([]);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBeNull();
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test 5: updates both count and detail timestamps on successful full detail observation', () => {
    const incoming = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsCountEvidenceAtMs: 3000,
      resetCreditsDetailEvidenceAtMs: 3000,
      observedAtMs: 3000,
    };

    const merged = mergeCodexResetCreditsEvidence(undefined, incoming, {
      isFullDetailObservation: true,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(merged.rateLimitResetCredits).toEqual([creditA, creditB]);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(3000);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(3000);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('resolves count from credits length when availableCount is missing in full detail observation', () => {
    const previous = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsCountEvidenceAtMs: 1000,
      resetCreditsDetailStale: false,
    };

    const detailIncoming = {
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [creditA],
      resetCreditsDetailEvidenceAtMs: 2500,
      observedAtMs: 2500,
    };

    const merged = mergeCodexResetCreditsEvidence(previous, detailIncoming, {
      isFullDetailObservation: true,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(1);
    expect(merged.rateLimitResetCredits).toEqual([creditA]);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2500);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(2500);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test A: full detail explicit zero dominates conflicting records and clears credits to empty', () => {
    const incoming = {
      rateLimitResetCreditsAvailableCount: 0,
      rateLimitResetCredits: [creditA],
      resetCreditsCountEvidenceAtMs: 2000,
      resetCreditsDetailEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(undefined, incoming, {
      isFullDetailObservation: true,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(0);
    expect(merged.rateLimitResetCredits).toEqual([]);
    expect(merged.rateLimitResetCreditsError).toBeNull();
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test B: positive mismatch between available count and credits length remains allowed', () => {
    const incoming = {
      rateLimitResetCreditsAvailableCount: 5,
      rateLimitResetCredits: [creditA],
      resetCreditsCountEvidenceAtMs: 2000,
      resetCreditsDetailEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(undefined, incoming, {
      isFullDetailObservation: true,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(5);
    expect(merged.rateLimitResetCredits).toEqual([creditA]);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test C: inferred zero from explicit empty credits preserves fresh detail evidence and count 0', () => {
    const incoming = {
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [],
      resetCreditsCountEvidenceAtMs: 2000,
      resetCreditsDetailEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(undefined, incoming, {
      isFullDetailObservation: true,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(0);
    expect(merged.rateLimitResetCredits).toEqual([]);
    expect(merged.resetCreditsCountEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(2000);
    expect(merged.resetCreditsDetailStale).toBe(false);
  });

  it('Test 6: preserves trusted detail and records error when detail fetch fails and count is unchanged', () => {
    const previous = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [creditA, creditB],
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsCountEvidenceAtMs: 1000,
      resetCreditsDetailStale: false,
    };

    const incoming = {
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [],
      rateLimitResetCreditsError: 'Rate limit endpoint timeout',
      resetCreditsCountEvidenceAtMs: 2000,
      observedAtMs: 2000,
    };

    const merged = mergeCodexResetCreditsEvidence(previous, incoming, {
      isFullDetailObservation: false,
    });

    expect(merged.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(merged.rateLimitResetCredits).toEqual([creditA, creditB]);
    expect(merged.resetCreditsDetailEvidenceAtMs).toBe(1000);
    expect(merged.rateLimitResetCreditsError).toBe('Rate limit endpoint timeout');
    expect(merged.resetCreditsDetailStale).toBe(false);
  });
});

describe('shouldAutoFetchCodexResetCreditDetails (Test 12)', () => {
  const now = 10_000_000;
  const fourMinutesAgo = now - 4 * 60 * 1000;
  const sixMinutesAgo = now - 6 * 60 * 1000;

  it('returns false when count is unknown or null', () => {
    expect(shouldAutoFetchCodexResetCreditDetails(undefined, now)).toBe(false);
    expect(shouldAutoFetchCodexResetCreditDetails({ rateLimitResetCreditsAvailableCount: null }, now)).toBe(false);
  });

  it('returns false when count is 0', () => {
    expect(shouldAutoFetchCodexResetCreditDetails({ rateLimitResetCreditsAvailableCount: 0 }, now)).toBe(false);
  });

  it('returns true when count > 0 and no detail evidence exists', () => {
    expect(
      shouldAutoFetchCodexResetCreditDetails(
        {
          rateLimitResetCreditsAvailableCount: 2,
          rateLimitResetCredits: [],
          resetCreditsDetailEvidenceAtMs: null,
        },
        now
      )
    ).toBe(true);
  });

  it('returns true when count > 0 and detail is marked stale', () => {
    expect(
      shouldAutoFetchCodexResetCreditDetails(
        {
          rateLimitResetCreditsAvailableCount: 2,
          resetCreditsDetailStale: true,
          resetCreditsDetailEvidenceAtMs: fourMinutesAgo,
        },
        now
      )
    ).toBe(true);
  });

  it('returns false when count > 0 and detail evidence is within 5 minutes TTL', () => {
    expect(
      shouldAutoFetchCodexResetCreditDetails(
        {
          rateLimitResetCreditsAvailableCount: 2,
          resetCreditsDetailStale: false,
          resetCreditsDetailEvidenceAtMs: fourMinutesAgo,
        },
        now
      )
    ).toBe(false);
  });

  it('returns true when count > 0 and detail evidence exceeds 5 minutes TTL', () => {
    expect(
      shouldAutoFetchCodexResetCreditDetails(
        {
          rateLimitResetCreditsAvailableCount: 2,
          resetCreditsDetailStale: false,
          resetCreditsDetailEvidenceAtMs: sixMinutesAgo,
        },
        now
      )
    ).toBe(true);
  });
});

describe('buildCodexResetCreditAutoFetchSignature', () => {
  it('generates consistent signature based on selectionKey, count, timestamps, and stale flag', () => {
    const sig1 = buildCodexResetCreditAutoFetchSignature('key-1', {
      rateLimitResetCreditsAvailableCount: 2,
      resetCreditsCountEvidenceAtMs: 1000,
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsDetailStale: false,
    });
    const sig2 = buildCodexResetCreditAutoFetchSignature('key-1', {
      rateLimitResetCreditsAvailableCount: 2,
      resetCreditsCountEvidenceAtMs: 1000,
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsDetailStale: false,
    });
    const sig3 = buildCodexResetCreditAutoFetchSignature('key-1', {
      rateLimitResetCreditsAvailableCount: 1,
      resetCreditsCountEvidenceAtMs: 2000,
      resetCreditsDetailEvidenceAtMs: 1000,
      resetCreditsDetailStale: true,
    });

    expect(sig1).toBe(sig2);
    expect(sig1).not.toBe(sig3);
  });
});
