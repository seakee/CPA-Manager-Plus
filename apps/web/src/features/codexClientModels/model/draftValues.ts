/**
 * 编辑器草稿的取值转换：输入框文本与 JSON 取值之间的格式化与解析。
 *
 * 解析结果用 ``DraftParseResult`` 表达，失败时保留原因而不是抛异常，
 * 这样输入过程中的中间态可以留在输入框里、不写入补丁。
 */

import type { useTranslation } from 'react-i18next';
import { isStringListArray, parseStringList } from './codexClientModelsTree';

export type DraftParseError = { reason: 'json' | 'array' | 'number' | 'empty'; detail?: string };
export type DraftParseResult = { ok: true; value: unknown } | { ok: false; error: DraftParseError };

export const formatJsonText = (value: unknown): string =>
  value === undefined ? '' : (JSON.stringify(value, null, 2) ?? '');

export const formatPlainText = (value: unknown): string =>
  value === undefined || value === null ? '' : String(value);

/** 字符串数组按「一行一项」呈现，其余数组按 JSON 呈现。 */
export const formatArrayText = (value: unknown): string =>
  isStringListArray(value) ? value.join('\n') : formatJsonText(value);

export const parsePlainText = (text: string): DraftParseResult => ({ ok: true, value: text });

export const parseStringListText = (text: string): DraftParseResult => ({
  ok: true,
  value: parseStringList(text),
});

export const parseJsonArrayText = (text: string): DraftParseResult => {
  try {
    const parsed: unknown = JSON.parse(text);
    if (!Array.isArray(parsed)) return { ok: false, error: { reason: 'array' } };
    return { ok: true, value: parsed };
  } catch (error) {
    return {
      ok: false,
      error: { reason: 'json', detail: error instanceof Error ? error.message : '' },
    };
  }
};

export const parseNumberText = (text: string): DraftParseResult => {
  const trimmed = text.trim();
  if (!trimmed) return { ok: false, error: { reason: 'empty' } };
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) return { ok: false, error: { reason: 'number' } };
  return { ok: true, value: parsed };
};

type Translate = ReturnType<typeof useTranslation>['t'];

export const describeDraftError = (error: DraftParseError, t: Translate): string => {
  switch (error.reason) {
    case 'json':
      return t('codex_client_models.patch_error_invalid_json', { message: error.detail ?? '' });
    case 'array':
      return t('codex_client_models.field_error_array');
    case 'number':
      return t('codex_client_models.field_error_number');
    default:
      return t('codex_client_models.field_error_empty');
  }
};
