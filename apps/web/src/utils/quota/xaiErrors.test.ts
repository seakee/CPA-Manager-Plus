import { describe, expect, it, vi } from 'vitest';
import {
  classifyXaiProbe,
  parseXaiErrorEnvelope,
  type XaiProbeSurface,
} from './xaiErrors';
import sharedXaiInspectionCasesJSON from '../../../../../tests/fixtures/xai-inspection-cases.json?raw';

type SharedXaiInspectionCase = {
  name: string;
  surface: XaiProbeSurface;
  statusCode: number;
  body: unknown;
  headers?: Record<string, string>;
  expected: {
    classification: string;
    action: string;
    reasonCode: string;
    retryAfterSeconds: number | null;
  };
};

const sharedXaiInspectionCases = JSON.parse(
  sharedXaiInspectionCasesJSON
) as SharedXaiInspectionCase[];

describe('parseXaiErrorEnvelope', () => {
  it('parses nested websocket errors and Retry-After headers', () => {
    const envelope = parseXaiErrorEnvelope({
      body: {
        type: 'error',
        status: 429,
        error: {
          code: 'subscription:free-usage-exhausted',
          message: "You've used all the included free usage for now.",
        },
      },
      headers: { 'Retry-After': ['3600'] },
    });

    expect(envelope).toMatchObject({
      statusCode: 429,
      code: 'subscription:free-usage-exhausted',
      message: "You've used all the included free usage for now.",
      retryAfterSeconds: 3600,
    });
  });

  it('parses JSON strings and HTTP-date Retry-After values', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-15T00:00:00Z'));
    const envelope = parseXaiErrorEnvelope({
      statusCode: 403,
      bodyText: JSON.stringify({
        code: 'The caller does not have permission to execute the specified operation',
        error: 'You need a Grok subscription.',
      }),
      headers: { 'retry-after': 'Wed, 15 Jul 2026 00:01:00 GMT' },
    });
    vi.useRealTimers();

    expect(envelope.statusCode).toBe(403);
    expect(envelope.message).toBe('You need a Grok subscription.');
    expect(envelope.retryAfterSeconds).toBe(60);
  });
});

describe('classifyXaiProbe', () => {
  const classify = (statusCode: number, body: unknown) =>
    classifyXaiProbe({
      surface: 'inference',
      envelope: parseXaiErrorEnvelope({ statusCode, body }),
    });

  it.each(sharedXaiInspectionCases)('matches shared fixture: $name', (fixture) => {
    const envelope = parseXaiErrorEnvelope({
      statusCode: fixture.statusCode,
      body: fixture.body,
      headers: fixture.headers,
    });
    const decision = classifyXaiProbe({ surface: fixture.surface, envelope });

    expect(decision).toMatchObject({
      classification: fixture.expected.classification,
      suggestedAction: fixture.expected.action,
      reasonCode: fixture.expected.reasonCode,
      retryAfterSeconds: fixture.expected.retryAfterSeconds,
    });
  });

  it.each([402, 429])('classifies free usage exhaustion under HTTP %i', (statusCode) => {
    expect(
      classify(statusCode, {
        code: 'subscription:free-usage-exhausted',
        error: "You've used all the included free usage for now.",
      })
    ).toMatchObject({
      classification: 'free_quota_exhausted',
      suggestedAction: 'disable',
      confidence: 'verified',
    });
  });

  it('classifies spending limits before generic permission handling', () => {
    expect(
      classify(403, {
        code: 'The caller does not have permission to execute the specified operation',
        error: 'Your team has used all available credits or reached its monthly spending limit.',
      })
    ).toMatchObject({
      classification: 'spending_limit',
      suggestedAction: 'disable',
    });
  });

  it('keeps generic 429 as a retryable rate limit', () => {
    expect(classify(429, { code: 'rate_limit', error: 'too many requests' })).toMatchObject({
      classification: 'rate_limited',
      suggestedAction: 'keep',
    });
  });

  it('does not turn a generic 403 into delete or reauth', () => {
    expect(
      classify(403, {
        code: 'The caller does not have permission to execute the specified operation',
        error: 'Forbidden',
      })
    ).toMatchObject({
      classification: 'permission_unknown',
      suggestedAction: 'keep',
      needsReview: true,
    });
  });

  it('separates policy failures from account failures', () => {
    expect(
      classify(403, {
        code: 'The caller does not have permission to execute the specified operation',
        error: 'Content violates usage guidelines. Failed check: SAFETY_CHECK_TYPE_DATA_LEAKAGE',
      })
    ).toMatchObject({
      classification: 'policy_denied',
      suggestedAction: 'keep',
    });
  });

  it('classifies invalid refresh credentials as reauth', () => {
    expect(
      classify(400, { error: 'invalid_grant', error_description: 'Refresh token reused' })
    ).toMatchObject({
      classification: 'auth_invalid',
      suggestedAction: 'reauth',
      confidence: 'verified',
    });
  });

  it('classifies client version errors without mutating the account', () => {
    expect(classify(426, { error: 'client version is too old' })).toMatchObject({
      classification: 'client_outdated',
      suggestedAction: 'keep',
    });
  });

  it('does not claim inference health from billing success', () => {
    const envelope = parseXaiErrorEnvelope({ statusCode: 200, body: { config: {} } });
    expect(classifyXaiProbe({ surface: 'billing', envelope, hasPayload: true })).toMatchObject({
      classification: 'billing_healthy',
      suggestedAction: 'keep',
    });
  });

  it('only enables healthy accounts owned by automatic recovery', () => {
    const envelope = parseXaiErrorEnvelope({ statusCode: 200, body: { config: {} } });
    expect(
      classifyXaiProbe({
        surface: 'billing',
        envelope,
        hasPayload: true,
        disabled: true,
        autoRecoverOwned: false,
      }).suggestedAction
    ).toBe('keep');
    expect(
      classifyXaiProbe({
        surface: 'billing',
        envelope,
        hasPayload: true,
        disabled: true,
        autoRecoverOwned: true,
      }).suggestedAction
    ).toBe('enable');
  });
});
