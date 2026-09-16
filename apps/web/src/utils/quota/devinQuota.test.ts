import { describe, expect, it } from 'vitest';
import {
  normalizeIsoTimestampMs,
  normalizePlanName,
  normalizeQuotaPercent,
  normalizeUnixSecondsToMs,
  parseDevinQuotaPayload,
} from './devinQuota';

describe('devinQuota normalizers', () => {
  it('normalizes quota percent correctly', () => {
    expect(normalizeQuotaPercent(54)).toBe(54);
    expect(normalizeQuotaPercent('54')).toBe(54);
    expect(normalizeQuotaPercent(0)).toBe(0);
    expect(normalizeQuotaPercent('0')).toBe(0);
    expect(normalizeQuotaPercent(100)).toBe(100);
    expect(normalizeQuotaPercent('100')).toBe(100);
    expect(normalizeQuotaPercent(0.5)).toBe(0.5);
    expect(normalizeQuotaPercent('0.5')).toBe(0.5);

    // Invalid percent inputs return null
    expect(normalizeQuotaPercent('54%')).toBeNull();
    expect(normalizeQuotaPercent(-1)).toBeNull();
    expect(normalizeQuotaPercent('-1')).toBeNull();
    expect(normalizeQuotaPercent(101)).toBeNull();
    expect(normalizeQuotaPercent('101')).toBeNull();
    expect(normalizeQuotaPercent(NaN)).toBeNull();
    expect(normalizeQuotaPercent(Infinity)).toBeNull();
    expect(normalizeQuotaPercent(-Infinity)).toBeNull();
    expect(normalizeQuotaPercent(true)).toBeNull();
    expect(normalizeQuotaPercent(false)).toBeNull();
    expect(normalizeQuotaPercent({})).toBeNull();
    expect(normalizeQuotaPercent([])).toBeNull();
    expect(normalizeQuotaPercent(null)).toBeNull();
    expect(normalizeQuotaPercent(undefined)).toBeNull();
    expect(normalizeQuotaPercent('')).toBeNull();
    expect(normalizeQuotaPercent('   ')).toBeNull();
  });

  it('normalizes unix seconds to epoch ms', () => {
    expect(normalizeUnixSecondsToMs(1726000000)).toBe(1726000000000);
    expect(normalizeUnixSecondsToMs('1726000000')).toBe(1726000000000);
    expect(normalizeUnixSecondsToMs(0)).toBeNull();
    expect(normalizeUnixSecondsToMs(-1)).toBeNull();
    expect(normalizeUnixSecondsToMs(1.5)).toBeNull();
    expect(normalizeUnixSecondsToMs('1.5')).toBeNull();
    expect(normalizeUnixSecondsToMs(Infinity)).toBeNull();
    expect(normalizeUnixSecondsToMs(NaN)).toBeNull();
    expect(normalizeUnixSecondsToMs(1e20)).toBeNull();
    expect(normalizeUnixSecondsToMs('abc')).toBeNull();
    expect(normalizeUnixSecondsToMs('')).toBeNull();
    expect(normalizeUnixSecondsToMs(null)).toBeNull();
    expect(normalizeUnixSecondsToMs(undefined)).toBeNull();
  });

  it('normalizes ISO timestamp to ms and rejects Go/protobuf zero-time', () => {
    expect(normalizeIsoTimestampMs('2026-09-15T10:00:00Z')).toBe(
      Date.parse('2026-09-15T10:00:00Z')
    );
    expect(normalizeIsoTimestampMs('0001-01-01T00:00:00Z')).toBeNull();
    expect(normalizeIsoTimestampMs('invalid-date')).toBeNull();
    expect(normalizeIsoTimestampMs('')).toBeNull();
    expect(normalizeIsoTimestampMs(null)).toBeNull();
    expect(normalizeIsoTimestampMs(undefined)).toBeNull();
  });

  it('normalizes plan name preserving case and trimming', () => {
    expect(normalizePlanName('  Pro  ')).toBe('Pro');
    expect(normalizePlanName('Team')).toBe('Team');
    expect(normalizePlanName('pro')).toBe('pro');
    expect(normalizePlanName('')).toBeNull();
    expect(normalizePlanName('   ')).toBeNull();
    expect(normalizePlanName(null)).toBeNull();
  });
});

describe('parseDevinQuotaPayload', () => {
  it('parses valid Connect JSON response into daily and weekly windows', () => {
    const payload = {
      userStatus: {
        planStatus: {
          planInfo: { planName: '  Devin Pro  ' },
          planStart: '2026-09-01T00:00:00Z',
          planEnd: '2026-10-01T00:00:00Z',
          dailyQuotaRemainingPercent: '54',
          weeklyQuotaRemainingPercent: 80,
          dailyQuotaResetAtUnix: 1726400000,
          weeklyQuotaResetAtUnix: '1726900000',
        },
        // Sensitive fields that must NOT enter quota data
        email: 'user@example.com',
        userName: 'devin-user',
        userId: 'u_12345',
        teamId: 't_67890',
        orgId: 'org_abc',
        orgName: 'Acme Corp',
        sessionToken: 'secret-session-token',
        signals: { abuse: false },
        metadata: { apiKey: 'sk-devin-secret' },
      },
    };

    const parsed = parseDevinQuotaPayload(payload, { observedAtMs: 123456789 });
    expect(parsed).not.toBeNull();
    expect(parsed?.observedAtMs).toBe(123456789);
    expect(parsed?.plan).toBe('Devin Pro');
    expect(parsed?.planStartMs).toBe(Date.parse('2026-09-01T00:00:00Z'));
    expect(parsed?.planEndMs).toBe(Date.parse('2026-10-01T00:00:00Z'));

    expect(parsed?.windows).toEqual([
      {
        id: 'daily',
        remainingPercent: 54,
        resetAtMs: 1726400000000,
        periodHours: 24,
      },
      {
        id: 'weekly',
        remainingPercent: 80,
        resetAtMs: 1726900000000,
        periodHours: 168,
      },
    ]);

    // Verify sensitive data leak prevention
    const rawResult = parsed as unknown as Record<string, unknown>;
    expect(rawResult['email']).toBeUndefined();
    expect(rawResult['userName']).toBeUndefined();
    expect(rawResult['userId']).toBeUndefined();
    expect(rawResult['teamId']).toBeUndefined();
    expect(rawResult['orgId']).toBeUndefined();
    expect(rawResult['orgName']).toBeUndefined();
    expect(rawResult['sessionToken']).toBeUndefined();
    expect(rawResult['signals']).toBeUndefined();
    expect(rawResult['metadata']).toBeUndefined();
  });

  it('correctly handles 0 remaining percent as genuine quota exhaustion', () => {
    const payload = {
      userStatus: {
        planStatus: {
          dailyQuotaRemainingPercent: 0,
          weeklyQuotaRemainingPercent: '0',
          dailyQuotaResetAtUnix: 1726400000,
          weeklyQuotaResetAtUnix: 1726900000,
        },
      },
    };

    const parsed = parseDevinQuotaPayload(payload);
    expect(parsed?.windows[0].remainingPercent).toBe(0);
    expect(parsed?.windows[1].remainingPercent).toBe(0);
  });

  it('parses JSON string payload', () => {
    const jsonStr = JSON.stringify({
      userStatus: {
        planStatus: {
          dailyQuotaRemainingPercent: 100,
          dailyQuotaResetAtUnix: 1726400000,
        },
      },
    });

    const parsed = parseDevinQuotaPayload(jsonStr);
    expect(parsed).not.toBeNull();
    expect(parsed?.windows[0].remainingPercent).toBe(100);
    expect(parsed?.windows[1].remainingPercent).toBeNull();
  });

  it('returns null (empty_data) when plan-only or no quota observations exist', () => {
    // Only plan name, no remaining percent or reset unix
    const planOnly = {
      userStatus: {
        planStatus: {
          planInfo: { planName: 'Pro' },
          planStart: '2026-09-01T00:00:00Z',
          planEnd: '2026-10-01T00:00:00Z',
        },
      },
    };
    expect(parseDevinQuotaPayload(planOnly)).toBeNull();

    // Invalid values across all quota fields
    const invalidValues = {
      userStatus: {
        planStatus: {
          dailyQuotaRemainingPercent: 'invalid%',
          weeklyQuotaRemainingPercent: -5,
          dailyQuotaResetAtUnix: 'bad',
          weeklyQuotaResetAtUnix: 0,
        },
      },
    };
    expect(parseDevinQuotaPayload(invalidValues)).toBeNull();

    // Malformed JSON string
    expect(parseDevinQuotaPayload('not a json')).toBeNull();

    // Empty object or non-object
    expect(parseDevinQuotaPayload({})).toBeNull();
    expect(parseDevinQuotaPayload(null)).toBeNull();
    expect(parseDevinQuotaPayload(undefined)).toBeNull();
  });

  it('rejects payloads where only invalid reset timestamp exists without valid observation', () => {
    const invalidResetOnly = {
      userStatus: {
        planStatus: {
          dailyQuotaResetAtUnix: 1.5,
          weeklyQuotaResetAtUnix: 'bad',
        },
      },
    };
    expect(parseDevinQuotaPayload(invalidResetOnly)).toBeNull();
  });
});
