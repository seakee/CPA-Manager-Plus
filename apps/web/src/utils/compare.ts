import {
  hasModelThinkingLevelsClearMarker,
  hasModelThinkingLevelsEditMarker,
  removeThinkingFlagAliases,
  THINKING_DYNAMIC_ALLOWED_FIELDS,
  THINKING_ZERO_ALLOWED_FIELDS,
} from '@/types';

export function areStringArraysEqual(a: readonly string[], b: readonly string[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

export function areKeyValueEntriesEqual(
  a: readonly { key: string; value: string }[],
  b: readonly { key: string; value: string }[]
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const left = a[i];
    const right = b[i];
    if (!left || !right) return false;
    if (left.key !== right.key || left.value !== right.value) return false;
  }
  return true;
}

export function areJsonLikeValuesEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((value, index) => areJsonLikeValuesEqual(value, b[index]));
  }

  const isRecord = (value: unknown): value is Record<string, unknown> =>
    value !== null && typeof value === 'object';
  if (!isRecord(a) || !isRecord(b)) return false;

  const aKeys = Object.keys(a);
  const bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  return aKeys.every(
    (key) => Object.prototype.hasOwnProperty.call(b, key) && areJsonLikeValuesEqual(a[key], b[key])
  );
}

const comparableThinking = (entry: { thinking?: unknown }) => {
  if (!entry.thinking || typeof entry.thinking !== 'object' || Array.isArray(entry.thinking)) {
    return entry.thinking;
  }

  const thinking = entry.thinking as Record<string, unknown>;
  if (hasModelThinkingLevelsEditMarker(entry)) {
    return removeThinkingFlagAliases(thinking, [
      ...THINKING_ZERO_ALLOWED_FIELDS,
      ...THINKING_DYNAMIC_ALLOWED_FIELDS,
    ]);
  }
  if (hasModelThinkingLevelsClearMarker(entry)) {
    const next = { ...thinking };
    delete next.levels;
    return next;
  }
  return thinking;
};

export function areModelEntriesEqual(
  a: readonly {
    name: string;
    alias: string;
    forceMapping?: boolean;
    inputModalities?: string[];
    outputModalities?: string[];
    thinking?: unknown;
  }[],
  b: readonly {
    name: string;
    alias: string;
    forceMapping?: boolean;
    inputModalities?: string[];
    outputModalities?: string[];
    thinking?: unknown;
  }[]
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const left = a[i];
    const right = b[i];
    if (!left || !right) return false;
    if (
      left.name !== right.name ||
      left.alias !== right.alias ||
      Boolean(left.forceMapping) !== Boolean(right.forceMapping) ||
      !areStringArraysEqual(left.inputModalities ?? [], right.inputModalities ?? []) ||
      !areStringArraysEqual(left.outputModalities ?? [], right.outputModalities ?? []) ||
      !areJsonLikeValuesEqual(comparableThinking(left), comparableThinking(right))
    )
      return false;
  }
  return true;
}
