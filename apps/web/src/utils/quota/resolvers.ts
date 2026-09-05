/**
 * Resolver functions for extracting data from auth files.
 */

import type { AuthFileItem } from '@/types';
import { normalizePlanType, parseIdTokenPayload } from './parsers';

const CODEX_ACCOUNT_ID_FIELDS = [
  'chatgpt_account_id',
  'chatgptAccountId',
  'account_id',
  'accountId',
] as const;

const CODEX_NESTED_EVIDENCE_FIELDS = ['id_token', 'idToken', 'metadata', 'attributes'] as const;

export type CodexChatgptAccountIdEvidence = {
  accountId: string | null;
  invalid: boolean;
};

const readRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

// Workspace IDs are opaque. Match the backend/SQLite rule: trim only ASCII
// spaces and preserve every other byte of a non-empty value.
const normalizeCodexWorkspaceId = (value: unknown): string | null => {
  let normalized: string;
  if (typeof value === 'string') {
    normalized = value.replace(/^ +| +$/g, '');
  } else if (typeof value === 'number' && Number.isFinite(value)) {
    normalized = String(value);
  } else {
    return null;
  }
  return normalized || null;
};

const readCodexAccountIdCandidate = (
  value: unknown
): { value: string | null; present: boolean; valid: boolean } => {
  if (value === undefined || value === null) {
    return { value: null, present: false, valid: true };
  }
  if (typeof value === 'string' && value.trim() === '') {
    return { value: null, present: false, valid: true };
  }
  const normalized = normalizeCodexWorkspaceId(value);
  return normalized
    ? { value: normalized, present: true, valid: true }
    : { value: null, present: true, valid: false };
};

const resolveCodexChatgptAccountIdEvidenceFromValue = (
  value: unknown
): CodexChatgptAccountIdEvidence => {
  const candidates = new Set<string>();
  let invalid = false;
  const visited = new Set<object>();

  const visit = (input: unknown) => {
    const record = readRecord(input);
    if (!record) {
      const payload = parseIdTokenPayload(input);
      if (payload) visit(payload);
      return;
    }
    if (visited.has(record)) return;
    visited.add(record);

    CODEX_ACCOUNT_ID_FIELDS.forEach((field) => {
      if (!(field in record)) return;
      const candidate = readCodexAccountIdCandidate(record[field]);
      if (!candidate.present) return;
      if (!candidate.valid || !candidate.value) {
        invalid = true;
        return;
      }
      candidates.add(candidate.value);
    });

    CODEX_NESTED_EVIDENCE_FIELDS.forEach((field) => {
      const nested = record[field];
      if (nested === undefined || nested === null) return;
      visit(nested);
    });
  };

  visit(value);
  if (invalid || candidates.size > 1) return { accountId: null, invalid: true };
  return { accountId: candidates.values().next().value ?? null, invalid: false };
};

export function extractCodexChatgptAccountId(value: unknown): string | null {
  return resolveCodexChatgptAccountIdEvidenceFromValue(value).accountId;
}

export function resolveCodexChatgptAccountIdEvidence(
  file: AuthFileItem
): CodexChatgptAccountIdEvidence {
  return resolveCodexChatgptAccountIdEvidenceFromValue(file);
}

export function resolveCodexChatgptAccountId(file: AuthFileItem): string | null {
  return resolveCodexChatgptAccountIdEvidenceFromValue(file).accountId;
}

export function isCodexChatgptAccountIdInvalid(file: AuthFileItem): boolean {
  return resolveCodexChatgptAccountIdEvidenceFromValue(file).invalid;
}

export function resolveCodexPlanType(file: AuthFileItem): string | null {
  const metadata =
    file && typeof file.metadata === 'object' && file.metadata !== null
      ? (file.metadata as Record<string, unknown>)
      : null;
  const attributes =
    file && typeof file.attributes === 'object' && file.attributes !== null
      ? (file.attributes as Record<string, unknown>)
      : null;
  const idToken =
    file && typeof file.id_token === 'object' && file.id_token !== null
      ? (file.id_token as Record<string, unknown>)
      : null;
  const metadataIdToken =
    metadata && typeof metadata.id_token === 'object' && metadata.id_token !== null
      ? (metadata.id_token as Record<string, unknown>)
      : null;
  const resolveIdTokenPlanCandidate = (value: unknown): string | null => {
    const payload = parseIdTokenPayload(value);
    if (!payload) return null;
    return normalizePlanType(payload.plan_type ?? payload.planType);
  };
  const candidates = [
    file.plan_type,
    file.planType,
    file['plan_type'],
    file['planType'],
    resolveIdTokenPlanCandidate(file.id_token),
    idToken?.plan_type,
    idToken?.planType,
    metadata?.plan_type,
    metadata?.planType,
    resolveIdTokenPlanCandidate(metadata?.id_token),
    metadataIdToken?.plan_type,
    metadataIdToken?.planType,
    attributes?.plan_type,
    attributes?.planType,
    resolveIdTokenPlanCandidate(attributes?.id_token),
  ];

  for (const candidate of candidates) {
    const planType = normalizePlanType(candidate);
    if (planType) return planType;
  }

  return null;
}
