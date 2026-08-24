export type MonitoringAccountIdentityInput = {
  provider?: string | null;
  account?: string | null;
  authLabel?: string | null;
  source?: string | null;
  authIndex?: string | null;
  sourceHash?: string | null;
};

const normalizeIdentityValue = (value: string | null | undefined) => String(value || '').trim();

export const normalizeMonitoringProvider = (value: string | null | undefined) =>
  normalizeIdentityValue(value).toLowerCase();

const encodeIdentityPart = (value: string) =>
  Array.from(new TextEncoder().encode(value), (byte) => byte.toString(16).padStart(2, '0'))
    .join('')
    .toUpperCase();

export const buildMonitoringAccountRowId = (identity: MonitoringAccountIdentityInput) => {
  const candidate = [
    ['account', identity.account],
    ['label', identity.authLabel],
    ['source', identity.source],
    ['auth', identity.authIndex],
    ['source-hash', identity.sourceHash],
  ].find(([, value]) => normalizeIdentityValue(value));
  if (!candidate) return '-';

  const [kind, rawValue] = candidate;
  return [
    'monitoring-account',
    '1',
    kind,
    encodeIdentityPart(normalizeMonitoringProvider(identity.provider)),
    encodeIdentityPart(normalizeIdentityValue(rawValue)),
  ].join(':');
};
