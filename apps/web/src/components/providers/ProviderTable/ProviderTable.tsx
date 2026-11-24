import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ChangeEvent,
  type CSSProperties,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconCheck,
  IconCopy,
  IconEye,
  IconModelCluster,
  IconMoreVertical,
  IconSatellite,
  IconSettings,
  IconShield,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { copyToClipboard } from '@/utils/clipboard';
import { ProviderStatusBar } from '../ProviderStatusBar';
import {
  formatCompactHistoryUsd,
  formatCompactTimestamp,
  formatCompactTokens,
} from './formatters';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from './kindMeta';
import type { ProviderModelEntry, ProviderRow } from './rowData';
import styles from './ProviderTable.module.scss';

export interface ProviderTableProps {
  rows: ProviderRow[];
  loading?: boolean;
  actionsDisabled?: boolean;
  toggleDisabled?: boolean;
  resolvedTheme: string;
  emptyState?: ReactNode;
  selectionMode?: boolean;
  selectedKeys?: ReadonlySet<string>;
  onToggleSelectRow?: (key: string) => void;
  onToggleSelectAllPage?: (checked: boolean) => void;
  selectedDrawerKey?: string | null;
  onInspect?: (row: ProviderRow) => void;
  onOpenModels?: (row: ProviderRow) => void;
  onOpenConnectivity?: (row: ProviderRow) => void;
  onHealthCheckRow?: (row: ProviderRow) => void;
  onEdit: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
  onToggleEnabled?: (row: ProviderRow, enabled: boolean) => void;
  onChangePriority?: (row: ProviderRow, nextPriority?: number) => Promise<void> | void;
  routingStrategy?: string;
  globalDisableCooling?: boolean;
}

const TOOLTIP_MARGIN = 12;
const TOOLTIP_OFFSET = 8;
const MODEL_PREVIEW_LIMIT = 10;
const formatModelEntryLabel = (entry: ProviderModelEntry): string =>
  entry.alias ? `${entry.alias} → ${entry.name}` : entry.name;

const stopPropagation = (event: MouseEvent) => {
  event.stopPropagation();
};

const priorityToDraft = (priority: number | undefined) =>
  Number.isFinite(priority) ? String(Math.trunc(priority ?? 0)) : '';

function useRowCopyFeedback() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const timerRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    return () => {
      clearTimeout(timerRef.current);
    };
  }, []);

  const handleCopyText = useCallback(
    async (event: MouseEvent, text: string, key: string) => {
      event.stopPropagation();
      if (!text) return;
      const copied = await copyToClipboard(text);
      if (!copied) return;
      setCopiedKey(key);
      clearTimeout(timerRef.current);
      if (typeof window !== 'undefined') {
        timerRef.current = window.setTimeout(() => {
          setCopiedKey((current) => (current === key ? null : current));
        }, 1600);
      }
    },
    []
  );

  return { copiedKey, handleCopyText };
}

interface PriorityControlProps {
  row: ProviderRow;
  disabled: boolean;
  editLabel: string;
  onChangePriority?: (row: ProviderRow, nextPriority?: number) => Promise<void> | void;
}

export function PriorityControl({
  row,
  disabled,
  editLabel,
  onChangePriority,
}: PriorityControlProps) {
  const [draft, setDraft] = useState(() => priorityToDraft(row.priority));
  const [isEditing, setIsEditing] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const skipNextBlurCommitRef = useRef(false);
  const currentPriority = Number.isFinite(row.priority) ? Math.trunc(row.priority ?? 0) : 0;
  const displayPriority = priorityToDraft(row.priority) || '—';

  useEffect(() => {
    if (!isEditing) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [isEditing]);

  const commitDraft = () => {
    if (skipNextBlurCommitRef.current) {
      skipNextBlurCommitRef.current = false;
      return;
    }

    const trimmed = draft.trim();
    if (!trimmed) {
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      return;
    }

    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) {
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      return;
    }

    const nextPriority = Math.trunc(parsed);
    setDraft(String(nextPriority));
    setIsEditing(false);
    if (nextPriority !== currentPriority || row.priority === undefined) {
      void Promise.resolve(onChangePriority?.(row, nextPriority));
    }
  };

  const startEditing = () => {
    if (disabled) return;
    skipNextBlurCommitRef.current = false;
    setDraft(priorityToDraft(row.priority));
    setIsEditing(true);
  };

  const handleInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    setDraft(event.target.value);
  };

  const handleInputKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      commitDraft();
      skipNextBlurCommitRef.current = true;
      event.currentTarget.blur();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      skipNextBlurCommitRef.current = true;
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      event.currentTarget.blur();
    }
  };

  return (
    <div className={styles.priorityControl} onClick={stopPropagation}>
      {isEditing ? (
        <input
          ref={inputRef}
          className={styles.priorityInput}
          type="number"
          step={1}
          inputMode="numeric"
          value={draft}
          placeholder="—"
          disabled={disabled}
          aria-label={editLabel}
          title={editLabel}
          onChange={handleInputChange}
          onBlur={commitDraft}
          onKeyDown={handleInputKeyDown}
        />
      ) : (
        <button
          type="button"
          className={styles.priorityValueButton}
          disabled={disabled}
          aria-label={editLabel}
          title={editLabel}
          onClick={startEditing}
        >
          {displayPriority}
        </button>
      )}
    </div>
  );
}

interface ProviderIdentityBlockProps {
  row: ProviderRow;
  resolvedTheme: string;
  copiedKey: string | null;
  onCopyText: (event: MouseEvent, text: string, key: string) => Promise<void>;
}

export function ProviderIdentityBlock({
  row,
  resolvedTheme,
  copiedKey,
  onCopyText,
}: ProviderIdentityBlockProps) {
  const { t } = useTranslation();
  const kindLabel = PROVIDER_KIND_LABELS[row.kind];
  const copyIdentityValue = row.alias || row.maskedKey;
  const identityCopyId = `${row.key}:identity`;
  return (
    <div className={styles.identityBlock}>
      <span className={styles.kindIconFrame} aria-hidden="true">
        <img
          src={getProviderKindIcon(row.kind, resolvedTheme)}
          alt=""
          className={[
            styles.kindIcon,
            row.kind === 'codex' ? styles.kindIconCodex : '',
            row.kind === 'openai' ? styles.kindIconOpenai : '',
          ]
            .filter(Boolean)
            .join(' ')}
        />
      </span>

      <div className={styles.identityText}>
        <div className={styles.identityLine1}>
          <strong className={styles.identityLabel} title={row.label}>
            {row.label}
          </strong>

          {copyIdentityValue ? (
            <button
              type="button"
              className={styles.inlineCopyButton}
              onClick={(event) => void onCopyText(event, copyIdentityValue, identityCopyId)}
              title={
                copiedKey === identityCopyId
                  ? t('common.copied', { defaultValue: '已复制' })
                  : `${t('common.copy')} ${copyIdentityValue}`
              }
              aria-label={`${t('common.copy')} ${copyIdentityValue}`}
            >
              {copiedKey === identityCopyId ? <IconCheck size={11} /> : <IconCopy size={11} />}
            </button>
          ) : null}
        </div>

        <div className={styles.identityLine2}>
          <span className={styles.kindLabel}>{kindLabel}</span>

          {row.kind === 'openai' ? (
            row.keyCount === 0 ? (
              <>
                <span className={styles.metaSep} aria-hidden="true">·</span>
                <span
                  className={styles.identityKeyWarn}
                  title={`${t('ai_providers.openai_keys_count')}: 0`}
                  aria-label={`${t('ai_providers.openai_keys_count')}: 0`}
                >
                  {t('ai_providers.keys_count_empty', { defaultValue: '无密钥' })}
                </span>
              </>
            ) : row.keyCount > 1 ? (
              <>
                <span className={styles.metaSep} aria-hidden="true">·</span>
                <span
                  className={styles.identityKeyMeta}
                  title={`${t('ai_providers.openai_keys_count')}: ${row.keyCount}`}
                  aria-label={`${t('ai_providers.openai_keys_count')}: ${row.keyCount}`}
                >
                  {t('ai_providers.keys_count_pill', {
                    count: row.keyCount,
                    defaultValue: `${row.keyCount} 把密钥`,
                  })}
                </span>
              </>
            ) : row.maskedKey ? (
              <>
                <span className={styles.metaSep} aria-hidden="true">·</span>
                <code className={styles.maskedKey} title={row.maskedKey}>
                  {row.maskedKey}
                </code>
              </>
            ) : null
          ) : (
            <>
              {row.alias && row.maskedKey ? (
                <>
                  <span className={styles.metaSep} aria-hidden="true">·</span>
                  <code className={styles.maskedKey} title={row.maskedKey}>
                    {row.maskedKey}
                  </code>
                </>
              ) : null}
              {row.v8GroupKeyMeta ? (
                <>
                  <span className={styles.metaSep} aria-hidden="true">·</span>
                  <span
                    className={styles.identityKeyMeta}
                    title={t('ai_providers.v8_group_key_title', {
                      index: row.v8GroupKeyMeta.index,
                      total: row.v8GroupKeyMeta.total,
                      defaultValue: `同组第 ${row.v8GroupKeyMeta.index} / ${row.v8GroupKeyMeta.total} 把轮询密钥`,
                    })}
                    aria-label={t('ai_providers.v8_group_key_title', {
                      index: row.v8GroupKeyMeta.index,
                      total: row.v8GroupKeyMeta.total,
                      defaultValue: `同组第 ${row.v8GroupKeyMeta.index} / ${row.v8GroupKeyMeta.total} 把轮询密钥`,
                    })}
                  >
                    {t('ai_providers.v8_group_key_pill', {
                      index: row.v8GroupKeyMeta.index,
                      total: row.v8GroupKeyMeta.total,
                      defaultValue: `密钥 ${row.v8GroupKeyMeta.index}/${row.v8GroupKeyMeta.total}`,
                    })}
                  </span>
                </>
              ) : null}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

interface ProviderBaseUrlBlockProps {
  row: ProviderRow;
  copiedKey: string | null;
  onCopyText: (event: MouseEvent, text: string, key: string) => Promise<void>;
}

export function ProviderBaseUrlBlock({
  row,
  copiedKey,
  onCopyText,
}: ProviderBaseUrlBlockProps) {
  const { t } = useTranslation();
  const urlCopyId = `${row.key}:url`;
  const prefixCopyId = `${row.key}:prefix`;
  const displayEndpoint = row.displayEndpoint || row.baseUrl;
  const showOfficialTag = Boolean(row.baseUrl) && row.isOfficialEndpoint;
  const normalizedCloakMode = row.cloakMode?.toLowerCase();
  const trailingMetaItems = [
    row.hasProxy
      ? {
          key: 'proxy',
          label: t('ai_providers.endpoint_proxy_badge', { defaultValue: '代理' }),
          title: t('ai_providers.endpoint_proxy_hint', { defaultValue: '已为该渠道配置上游代理地址' }),
        }
      : null,
    row.cloakMode
      ? {
          key: 'cloak',
          label:
            normalizedCloakMode === 'never'
              ? t('ai_providers.endpoint_cloak_off_badge', { defaultValue: '不伪装' })
              : t('ai_providers.endpoint_cloak_badge', { defaultValue: '伪装' }),
          title: t('ai_providers.endpoint_cloak_hint', {
            mode: row.cloakMode,
            defaultValue: `请求伪装 (${row.cloakMode})：按指定策略模拟官方客户端请求特征`,
          }),
        }
      : null,
    row.websockets
      ? {
          key: 'ws',
          label: t('ai_providers.endpoint_ws_badge', { defaultValue: 'WebSocket' }),
          title: t('ai_providers.endpoint_ws_hint', { defaultValue: '已开启 WebSocket 传输' }),
        }
      : null,
  ].filter((item): item is { key: string; label: string; title: string } => Boolean(item));
  const hasSubRow = showOfficialTag || Boolean(row.prefix) || trailingMetaItems.length > 0;

  return (
    <div className={styles.endpointBlock}>
      {row.baseUrl ? (
        <div className={styles.urlCellWrap}>
          <span className={styles.urlText} title={row.baseUrl}>
            {displayEndpoint}
          </span>
          <button
            type="button"
            className={styles.urlCopyButton}
            onClick={(event) => void onCopyText(event, row.baseUrl, urlCopyId)}
            title={
              copiedKey === urlCopyId
                ? t('common.copied', { defaultValue: '已复制' })
                : `${t('common.copy')} ${row.baseUrl}`
            }
            aria-label={`${t('common.copy')} ${row.baseUrl}`}
          >
            {copiedKey === urlCopyId ? <IconCheck size={12} /> : <IconCopy size={12} />}
          </button>
        </div>
      ) : (
        <span className={styles.urlDefault}>
          {t('ai_providers.official_default_endpoint', { defaultValue: '官方默认端点' })}
        </span>
      )}

      {hasSubRow ? (
        <div className={styles.endpointChips}>
          {showOfficialTag ? (
            <span
              className={`${styles.endpointMetaItem} ${styles.endpointOfficialTag}`}
              title={t('ai_providers.official_default_endpoint', { defaultValue: '官方默认端点' })}
            >
              {t('ai_providers.official_endpoint_badge', { defaultValue: '官方' })}
            </span>
          ) : null}
          {row.prefix ? (
            <>
              {showOfficialTag ? (
                <span className={styles.metaSep} aria-hidden="true">·</span>
              ) : null}
              <button
                type="button"
                className={styles.prefixBadge}
                onClick={(event) => void onCopyText(event, row.prefix, prefixCopyId)}
                title={
                  copiedKey === prefixCopyId
                    ? t('common.copied', { defaultValue: '已复制' })
                    : t('ai_providers.endpoint_prefix_hint', {
                        prefix: row.prefix,
                        defaultValue: `路由前缀：调用 ${row.prefix}/<模型名> 时命中此渠道（点击复制）`,
                      })
                }
                aria-label={`${t('common.copy')} ${row.prefix}`}
              >
                <span className={styles.prefixBadgeLabel}>
                  {t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' })}
                </span>
                <span className={styles.prefixBadgeValue}>{row.prefix}/</span>
                {copiedKey === prefixCopyId ? <IconCheck size={10} /> : null}
              </button>
            </>
          ) : null}
          {trailingMetaItems.map((item, idx) => (
            <span key={item.key} className={styles.endpointMetaItem} title={item.title}>
              {idx > 0 || showOfficialTag || row.prefix ? (
                <span className={styles.metaSep} aria-hidden="true">·</span>
              ) : null}
              <span>{item.label}</span>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}
interface ProviderModelsTriggerProps {
  row: ProviderRow;
  disabled: boolean;
  onOpenModels: (row: ProviderRow) => void;
  maxVisible?: number;
  hideSummaryLine?: boolean;
}

export function ProviderModelsTrigger({
  row,
  disabled,
  onOpenModels,
  maxVisible = 2,
  hideSummaryLine = false,
}: ProviderModelsTriggerProps) {
  const { t } = useTranslation();
  const [tooltipOpen, setTooltipOpen] = useState(false);
  const [tooltipStyle, setTooltipStyle] = useState<CSSProperties | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const tooltipRef = useRef<HTMLDivElement>(null);

  const resolvedEntries: ProviderModelEntry[] =
    row.modelEntries && row.modelEntries.length > 0
      ? row.modelEntries
      : row.modelNames.map((name) => ({ name }));
  const excludedModelNames = row.excludedModelNames ?? [];
  const previewEntries = resolvedEntries.slice(0, MODEL_PREVIEW_LIMIT);
  const remainingCount = Math.max(0, resolvedEntries.length - previewEntries.length);
  const aliasCount = resolvedEntries.filter((entry) => Boolean(entry.alias)).length;
  const hasPreviewTooltip = previewEntries.length > 0 || excludedModelNames.length > 0;
  const updateTooltipPosition = useCallback(() => {
    if (typeof window === 'undefined') return;
    const anchor = triggerRef.current;
    if (!anchor) return;
    const rect = anchor.getBoundingClientRect();
    const tooltipRect = tooltipRef.current?.getBoundingClientRect();
    const estimatedWidth = tooltipRect?.width ?? 220;
    const estimatedHeight = tooltipRect?.height ?? 120;

    const placeBelow =
      rect.top < estimatedHeight + TOOLTIP_MARGIN + TOOLTIP_OFFSET &&
      window.innerHeight - rect.bottom > rect.top;

    const rawLeft = rect.left + rect.width / 2 - estimatedWidth / 2;
    const clampedLeft = Math.min(
      Math.max(rawLeft, TOOLTIP_MARGIN),
      Math.max(TOOLTIP_MARGIN, window.innerWidth - estimatedWidth - TOOLTIP_MARGIN)
    );

    const top = placeBelow
      ? Math.min(
          rect.bottom + TOOLTIP_OFFSET,
          window.innerHeight - estimatedHeight - TOOLTIP_MARGIN
        )
      : Math.max(TOOLTIP_MARGIN, rect.top - estimatedHeight - TOOLTIP_OFFSET);

    setTooltipStyle({
      left: Math.round(clampedLeft),
      top: Math.round(top),
    });
  }, []);

  useLayoutEffect(() => {
    if (tooltipOpen && hasPreviewTooltip) {
      updateTooltipPosition();
    }
  }, [hasPreviewTooltip, tooltipOpen, updateTooltipPosition]);

  const primaryText =
    row.modelCount > 0
      ? t('ai_providers.models_count_pill', {
          count: row.modelCount,
          defaultValue: `${row.modelCount} 模型`,
        })
      : row.enabled
        ? t('ai_providers.models_wildcard_all', { defaultValue: '通配全开' })
        : '0';

  const excludedText =
    row.excludedModelCount > 0
      ? t('ai_providers.excluded_models_pill', {
          count: row.excludedModelCount,
          defaultValue: `排除 ${row.excludedModelCount}`,
        })
      : '';

  const buttonAriaLabel = excludedText
    ? `${t('ai_providers.table_col_models')}: ${primaryText} (${excludedText})`
    : `${t('ai_providers.table_col_models')}: ${primaryText}`;

  const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    onOpenModels(row);
  };

  const visibleEntries = resolvedEntries.slice(0, maxVisible);
  const hiddenModelCount = Math.max(0, resolvedEntries.length - visibleEntries.length);
  const summaryItems: Array<{ key: string; text: string; warn?: boolean }> = [];
  if (hiddenModelCount > 0) {
    summaryItems.push({
      key: 'total',
      text: t('ai_providers.models_total_summary', {
        count: resolvedEntries.length,
        defaultValue: `共 ${resolvedEntries.length} 个模型`,
      }),
    });
  }
  if (aliasCount > 0) {
    summaryItems.push({
      key: 'aliases',
      text: t('ai_providers.models_alias_count', {
        count: aliasCount,
        defaultValue: `${aliasCount} 条别名`,
      }),
    });
  }
  if (excludedText) {
    summaryItems.push({
      key: 'excluded',
      text: excludedText,
      warn: true,
    });
  }

  return (
    <div className={styles.modelsTriggerWrap} onClick={stopPropagation}>
      <button
        ref={triggerRef}
        type="button"
        className={[
          styles.modelsTrigger,
          row.modelCount === 0 && row.enabled ? styles.modelsTriggerWildcard : '',
        ]
          .filter(Boolean)
          .join(' ')}
        disabled={disabled}
        onClick={handleClick}
        onMouseEnter={() => {
          if (!hasPreviewTooltip) return;
          updateTooltipPosition();
          setTooltipOpen(true);
        }}
        onMouseLeave={() => setTooltipOpen(false)}
        onFocus={() => {
          if (!hasPreviewTooltip) return;
          updateTooltipPosition();
          setTooltipOpen(true);
        }}
        onBlur={() => setTooltipOpen(false)}
        aria-label={buttonAriaLabel}
      >
        {visibleEntries.length > 0 ? (
          <>
            <div className={styles.modelsTopRow}>
              {visibleEntries.map((entry) => {
                const publicName = entry.alias ?? entry.name;
                const chipTitle = formatModelEntryLabel(entry);
                return (
                  <span
                    key={`${entry.name}:${entry.alias ?? ''}`}
                    className={`${styles.modelPreviewChip} ${
                      visibleEntries.length > 1 ? styles.modelPreviewChipPair : ''
                    }`}
                    title={chipTitle}
                  >
                    {publicName}
                  </span>
                );
              })}
              {hiddenModelCount > 0 ? (
                <span className={styles.modelsMoreBadge}>{`+${hiddenModelCount}`}</span>
              ) : null}
            </div>
            {!hideSummaryLine && summaryItems.length > 0 ? (
              <div className={styles.modelsSubRow}>
                {summaryItems.map((item, idx) => (
                  <span
                    key={item.key}
                    className={item.warn ? styles.modelsExcludedInline : styles.modelsSummaryItem}
                  >
                    {idx > 0 ? (
                      <span className={styles.metaSep} aria-hidden="true">·</span>
                    ) : null}
                    <span>{item.text}</span>
                  </span>
                ))}
              </div>
            ) : null}
          </>
        ) : (
          <div className={styles.modelsTopRow}>
            <span className={styles.modelsPrimaryText}>{primaryText}</span>
            {!hideSummaryLine && excludedText ? (
              <span className={styles.excludedBadge}>{excludedText}</span>
            ) : null}
          </div>
        )}
      </button>
      {tooltipOpen && hasPreviewTooltip && typeof document !== 'undefined' && document.body
        ? createPortal(
            <div
              ref={tooltipRef}
              role="tooltip"
              className={styles.modelsTooltip}
              style={tooltipStyle ?? { visibility: 'hidden', top: 0, left: 0 }}
            >
              {previewEntries.length > 0 ? (
                <>
                  <div className={styles.modelsTooltipHeader}>
                    <span className={styles.modelsTooltipTitle}>
                      {t('ai_providers.models_preview_title', {
                        count: resolvedEntries.length,
                        defaultValue: `模型列表 (${resolvedEntries.length})`,
                      })}
                    </span>
                    {aliasCount > 0 ? (
                      <span className={styles.modelsTooltipSubtitle}>
                        {t('ai_providers.models_mapping_tooltip_hint', {
                          defaultValue: '对外调用名 → 上游模型',
                        })}
                      </span>
                    ) : null}
                  </div>
                  <ul className={styles.modelsTooltipList}>
                    {previewEntries.map((entry) =>
                      entry.alias ? (
                        <li
                          key={`${entry.name}:${entry.alias}`}
                          className={styles.modelsTooltipMappingRow}
                        >
                          <span className={styles.modelsTooltipAlias}>{entry.alias}</span>
                          <span className={styles.modelsTooltipArrow} aria-hidden="true">
                            →
                          </span>
                          <span className={styles.modelsTooltipUpstream}>{entry.name}</span>
                        </li>
                      ) : (
                        <li key={entry.name}>{entry.name}</li>
                      )
                    )}
                  </ul>
                  {remainingCount > 0 ? (
                    <div className={styles.modelsTooltipMore}>
                      {t('ai_providers.models_preview_more', {
                        count: remainingCount,
                        defaultValue: `另有 ${remainingCount} 个模型`,
                      })}
                    </div>
                  ) : null}
                </>
              ) : null}
              {excludedModelNames.length > 0 ? (
                <div
                  className={
                    previewEntries.length > 0
                      ? styles.modelsTooltipExcluded
                      : styles.modelsTooltipExcludedOnly
                  }
                >
                  <div className={styles.modelsTooltipExcludedTitle}>
                    {t('ai_providers.models_excluded_tooltip_title', {
                      count: excludedModelNames.length,
                      defaultValue: `已排除 (${excludedModelNames.length})`,
                    })}
                  </div>
                  <ul className={styles.modelsTooltipList}>
                    {excludedModelNames.slice(0, 6).map((name) => (
                      <li key={name}>{name}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>,
            document.body
          )
        : null}
    </div>
  );
}

export function ProviderSchedulingBlock({
  row,
  disabled,
  routingStrategy,
  globalDisableCooling,
  onChangePriority,
}: {
  row: ProviderRow;
  disabled: boolean;
  routingStrategy?: string;
  globalDisableCooling?: boolean;
  onChangePriority?: (row: ProviderRow, nextPriority?: number) => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const hasWeight =
    typeof row.weight === 'number' && Number.isFinite(row.weight) && row.weight !== 1;
  const normalizedStrategy =
    String(routingStrategy ?? '')
      .trim()
      .toLowerCase() || 'round-robin';
  const showEffectiveWeight = hasWeight && normalizedStrategy === 'weighted-round-robin';
  const effectiveGlobalDisableCooling = Boolean(globalDisableCooling);
  const showNoCooling = row.disableCooling === true;
  const showExplicitCoolingEnabled =
    row.disableCooling === false && effectiveGlobalDisableCooling;
  const hasCoolingIndicator = showNoCooling || showExplicitCoolingEnabled;

  return (
    <div className={styles.schedulingCell}>
      <div className={styles.priorityPillWrap} onClick={stopPropagation}>
        <span className={styles.priorityPrefix}>
          {t('ai_providers.priority_short', { defaultValue: '优先级' })}
        </span>
        <PriorityControl
          key={`${row.key}:${row.priority ?? 'unset'}`}
          row={row}
          disabled={disabled}
          editLabel={t('ai_providers.priority_edit')}
          onChangePriority={onChangePriority}
        />
      </div>
      {showEffectiveWeight || hasCoolingIndicator ? (
        <div className={styles.schedulingSubRow}>
          {showEffectiveWeight ? (
            <span className={styles.weightMeta}>
              {t('ai_providers.weight_value', {
                count: row.weight,
                defaultValue: `权重 ${row.weight}`,
              })}
            </span>
          ) : null}
          {showEffectiveWeight && hasCoolingIndicator ? (
            <span className={styles.metaSep} aria-hidden="true">·</span>
          ) : null}
          {showNoCooling ? (
            <span className={styles.coolingWarnMeta}>
              {t('ai_providers.chip_no_cooling', { defaultValue: '不冷却' })}
            </span>
          ) : showExplicitCoolingEnabled ? (
            <span className={styles.coolingMeta}>
              {t('ai_providers.chip_cooling_enabled', { defaultValue: '允许冷却' })}
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export function ProviderRecentRequestsBlock({ row }: { row: ProviderRow }) {
  const { t, i18n } = useTranslation();
  const history = row.history;
  const hasHistoryMetrics = Boolean(history?.matched && history.totalRequests > 0);
  const totalCalls = row.stats.success + row.stats.failure;
  const { compact: lastSeenCompact, full: lastSeenFull } = formatCompactTimestamp(
    row.lastSeenMs,
    i18n?.language
  );
  const latestFailure = totalCalls > 0 ? row.latestFailure : undefined;
  const failureBadgeLabel = latestFailure
    ? latestFailure.statusCode
      ? String(latestFailure.statusCode)
      : latestFailure.errorCode ||
        t('ai_providers.latest_failure_fallback', { defaultValue: '异常' })
    : null;
  const failureTimestamp = latestFailure
    ? formatCompactTimestamp(latestFailure.timestampMs, i18n?.language).full
    : null;
  const failureTitle = latestFailure
    ? [failureBadgeLabel, latestFailure.summary, failureTimestamp].filter(Boolean).join(' · ')
    : undefined;
  const showActiveFailureBadge = Boolean(latestFailure?.isMostRecent && failureBadgeLabel);

  const displayRate = totalCalls > 0 ? Math.round((row.stats.success / totalCalls) * 1000) / 10 : 0;
  const rateClass =
    displayRate >= 90
      ? styles.statRateHigh
      : displayRate >= 50
        ? styles.statRateMedium
        : styles.statRateLow;
  const historyTokensCompact =
    hasHistoryMetrics && history ? formatCompactTokens(history.totalTokens) : '';
  const historyCostCompact =
    hasHistoryMetrics && history && history.totalCost !== null
      ? formatCompactHistoryUsd(history.totalCost)
      : '';
  const historyTokenText = historyTokensCompact
    ? historyCostCompact
      ? `${historyTokensCompact} · ${historyCostCompact}`
      : `${historyTokensCompact} tok`
    : '';
  const showUnavailableHint = row.usageStatsCoverage === 'unavailable' && !hasHistoryMetrics;
  const showPartialHint = totalCalls === 0 && row.usageStatsCoverage === 'partial';

  return (
    <div className={styles.recentActiveWrap}>
      <div className={styles.recentTopRow}>
        <div className={styles.recentBarWrap}>
          <ProviderStatusBar statusData={row.statusData} hideRate />
        </div>
        <div className={styles.recentTrailingSlot}>
          {showActiveFailureBadge && failureBadgeLabel ? (
            <span className={styles.latestFailureBadge} title={failureTitle}>
              {failureBadgeLabel}
            </span>
          ) : totalCalls > 0 && lastSeenCompact ? (
            <span className={styles.recentTrailingTime} title={lastSeenFull ?? undefined}>
              {lastSeenCompact}
            </span>
          ) : (
            <span className={styles.recentTrailingEmpty} aria-hidden="true">
              —
            </span>
          )}
        </div>
      </div>

      {totalCalls > 0 ? (
        <div className={styles.recentCountsRow}>
          <div className={styles.recentStatsGroup}>
            <span className={`${styles.statRate} ${rateClass}`}>{`${displayRate}%`}</span>
            <span className={styles.statTotal}>
              {`${totalCalls} ${t('ai_providers.calls_unit', { defaultValue: '次' })}`}
            </span>
            {row.stats.failure > 0 ? (
              <span
                className={styles.statFailure}
                title={failureTitle ?? t('stats.failure')}
              >
                <IconX size={11} /> {row.stats.failure}
              </span>
            ) : null}
          </div>
          {historyTokensCompact ? (
            <span className={styles.recentHistoryMeta} title={historyTokenText}>
              <span className={styles.recentHistoryNum}>
                {historyCostCompact ? historyTokensCompact : `${historyTokensCompact} tok`}
              </span>
              {historyCostCompact ? (
                <>
                  <span className={styles.recentMetaDot} aria-hidden="true">·</span>
                  <span className={styles.recentHistoryNum}>{historyCostCompact}</span>
                </>
              ) : null}
            </span>
          ) : row.usageStatsCoverage === 'partial' ? (
            <span className={styles.noRecent}>{t('ai_providers.usage_stats_partial')}</span>
          ) : null}
        </div>
      ) : showUnavailableHint ? (
        <div className={styles.recentCountsRow}>
          <span className={styles.noRecent}>{t('ai_providers.usage_stats_unavailable')}</span>
        </div>
      ) : showPartialHint ? (
        <div className={styles.recentCountsRow}>
          <span className={styles.noRecent}>{t('ai_providers.usage_stats_partial')}</span>
        </div>
      ) : null}
    </div>
  );
}

interface ProviderRowActionGroupProps {
  row: ProviderRow;
  actionsDisabled: boolean;
  toggleDisabled: boolean;
  onInspect?: (row: ProviderRow) => void;
  onOpenModels: (row: ProviderRow) => void;
  onOpenConnectivity?: (row: ProviderRow) => void;
  onHealthCheckRow?: (row: ProviderRow) => void;
  onEdit: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
  onToggleEnabled?: (row: ProviderRow, enabled: boolean) => void;
}

const buildProviderMoreMenuItems = ({
  row,
  t,
  onInspect,
  onOpenModels,
  onOpenConnectivity,
  onDelete,
}: {
  row: ProviderRow;
  t: (key: string, options?: Record<string, unknown>) => string;
  onInspect?: (row: ProviderRow) => void;
  onOpenModels: (row: ProviderRow) => void;
  onOpenConnectivity?: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
}): DropdownMenuItem[] => {
  const items: DropdownMenuItem[] = [];
  if (onInspect) {
    items.push({
      key: 'inspect',
      label: t('ai_providers.detail_button', { defaultValue: '详情' }),
      icon: <IconEye size={14} />,
      onClick: () => onInspect(row),
    });
  }
  items.push({
    key: 'models',
    label: t('ai_providers.models_action', { defaultValue: '模型配置' }),
    icon: <IconModelCluster size={14} />,
    onClick: () => onOpenModels(row),
  });
  if (onOpenConnectivity) {
    items.push({
      key: 'connectivity',
      label: t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' }),
      icon: <IconSatellite size={14} />,
      onClick: () => onOpenConnectivity(row),
    });
  }
  items.push({
    key: 'divider-danger',
    type: 'divider',
  });
  items.push({
    key: 'delete',
    label: t('common.delete'),
    icon: <IconTrash2 size={14} />,
    tone: 'danger',
    onClick: () => onDelete(row),
  });
  return items;
};

export function ProviderMoreMenu({
  row,
  disabled,
  triggerClassName,
  onInspect,
  onOpenModels,
  onOpenConnectivity,
  onDelete,
}: {
  row: ProviderRow;
  disabled: boolean;
  triggerClassName?: string;
  onInspect?: (row: ProviderRow) => void;
  onOpenModels: (row: ProviderRow) => void;
  onOpenConnectivity?: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
}) {
  const { t } = useTranslation();
  const items = buildProviderMoreMenuItems({
    row,
    t,
    onInspect,
    onOpenModels,
    onOpenConnectivity,
    onDelete,
  });
  return (
    <DropdownMenu
      items={items}
      ariaLabel={t('ai_providers.row_more_actions', {
        name: row.label,
        defaultValue: `更多操作 (${row.label})`,
      })}
      triggerTitle={t('ai_providers.row_actions_aria', { defaultValue: '更多操作' })}
      triggerIcon={<IconMoreVertical size={14} />}
      triggerClassName={triggerClassName}
      disabled={disabled}
    />
  );
}

export function ProviderRowActionGroup({
  row,
  actionsDisabled,
  toggleDisabled,
  onInspect,
  onOpenModels,
  onOpenConnectivity,
  onHealthCheckRow,
  onEdit,
  onDelete,
  onToggleEnabled,
}: ProviderRowActionGroupProps) {
  const { t } = useTranslation();
  return (
    <div
      className={styles.rowActions}
      onClick={stopPropagation}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <div className={styles.quickActions}>
        {onHealthCheckRow ? (
          <Button
            type="button"
            variant="secondary"
            size="xs"
            iconOnly
            className={`${styles.actionIconBtn} ${styles.actionIconBtnProbe}`}
            onClick={() => onHealthCheckRow(row)}
            disabled={actionsDisabled}
            aria-label={t('ai_providers.row_health_check', { defaultValue: '测活' })}
            title={t('ai_providers.row_health_check', { defaultValue: '测活' })}
          >
            <IconShield size={14} />
          </Button>
        ) : null}
        <Button
          type="button"
          variant="secondary"
          size="xs"
          iconOnly
          className={`${styles.actionIconBtn} ${styles.actionIconBtnSettings}`}
          onClick={() => onEdit(row)}
          disabled={actionsDisabled}
          aria-label={t('common.edit')}
          title={t('common.edit')}
        >
          <IconSettings size={14} />
        </Button>
        <ProviderMoreMenu
          row={row}
          disabled={actionsDisabled}
          triggerClassName={styles.moreMenuTrigger}
          onInspect={onInspect}
          onOpenModels={onOpenModels}
          onOpenConnectivity={onOpenConnectivity}
          onDelete={onDelete}
        />
      </div>

      <span className={styles.actionsDivider} aria-hidden="true" />

      <div className={styles.toggleWrap}>
        <ToggleSwitch
          ariaLabel={t('ai_providers.config_toggle_label')}
          checked={row.enabled}
          disabled={toggleDisabled}
          onChange={(value) => onToggleEnabled?.(row, value)}
        />
      </div>
    </div>
  );
}
export function ProviderTable({
  rows,
  loading = false,
  actionsDisabled = false,
  toggleDisabled,
  resolvedTheme,
  emptyState = null,
  selectionMode = false,
  selectedKeys,
  onToggleSelectRow,
  onToggleSelectAllPage,
  selectedDrawerKey = null,
  onInspect,
  onOpenModels,
  onOpenConnectivity,
  onHealthCheckRow,
  onEdit,
  onDelete,
  onToggleEnabled,
  onChangePriority,
  routingStrategy,
  globalDisableCooling,
}: ProviderTableProps) {
  const { t } = useTranslation();
  const { copiedKey, handleCopyText } = useRowCopyFeedback();

  const handleOpenModels = onOpenModels ?? onEdit;
  const resolvedToggleDisabled = toggleDisabled ?? actionsDisabled;
  if (loading && rows.length === 0) {
    return <div className="hint">{t('common.loading')}</div>;
  }

  if (rows.length === 0) {
    return <>{emptyState}</>;
  }

  const allPageSelected =
    selectionMode && rows.length > 0 && rows.every((row) => Boolean(selectedKeys?.has(row.key)));

  const handleRowKeyDown = (event: KeyboardEvent<HTMLDivElement>, row: ProviderRow) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      if (selectionMode) {
        onToggleSelectRow?.(row.key);
      } else {
        onInspect?.(row);
      }
    }
  };

  return (
    <div className={styles.tableScroller}>
      <div
        className={`${styles.table} ${selectionMode ? styles.tableWithSelection : ''}`}
        role="table"
        aria-label={t('ai_providers.table_aria_label')}
      >
        <div className={styles.headerRow} role="row">
          {selectionMode ? (
            <span role="columnheader" className={styles.cellSelectHeader}>
              <SelectionCheckbox
                checked={allPageSelected}
                onChange={(checked) => onToggleSelectAllPage?.(checked)}
                disabled={actionsDisabled || rows.length === 0}
                ariaLabel={t('ai_providers.select_all_page', { defaultValue: '全选当前页' })}
              />
            </span>
          ) : null}
          <span role="columnheader">{t('ai_providers.table_col_identity')}</span>
          <span role="columnheader">
            {t('ai_providers.table_col_endpoint', { defaultValue: '端点与路由' })}
          </span>
          <span role="columnheader" className={styles.cellModelsHeader}>
            {t('ai_providers.table_col_models')}
          </span>
          <span role="columnheader" className={styles.cellPriorityHeader}>
            {t('ai_providers.table_col_scheduling', { defaultValue: '调度' })}
          </span>
          <span role="columnheader" className={styles.cellRecentHeader}>
            {t('ai_providers.table_col_recent')}
          </span>
          <span role="columnheader" className={styles.cellActionsHeader}>
            {t('ai_providers.table_col_actions')}
          </span>
        </div>

        {rows.map((row) => {
          const kindLabel = PROVIDER_KIND_LABELS[row.kind];
          const isBulkSelected = Boolean(selectedKeys?.has(row.key));
          const isDrawerSelected = selectedDrawerKey === row.key;
          const rowClassName = [
            styles.row,
            !row.enabled ? styles.rowDisabled : '',
            isDrawerSelected ? styles.rowSelected : '',
            isBulkSelected ? styles.rowBulkSelected : '',
            selectionMode ? styles.rowSelectionMode : '',
          ]
            .filter(Boolean)
            .join(' ');

          return (
            <div
              key={row.key}
              role="row"
              tabIndex={0}
              aria-selected={selectionMode ? isBulkSelected : isDrawerSelected}
              className={rowClassName}
              onClick={
                selectionMode
                  ? () => onToggleSelectRow?.(row.key)
                  : () => onInspect?.(row)
              }
              onKeyDown={(event) => handleRowKeyDown(event, row)}
              aria-label={`${kindLabel} ${row.label}`}
            >
              {selectionMode ? (
                <div className={styles.cellSelect} role="cell" onClick={stopPropagation}>
                  <SelectionCheckbox
                    checked={isBulkSelected}
                    onChange={() => onToggleSelectRow?.(row.key)}
                    disabled={actionsDisabled}
                    ariaLabel={t('ai_providers.select_provider', {
                      name: row.label,
                      defaultValue: `选择 ${row.label}`,
                    })}
                  />
                </div>
              ) : null}

              <div className={styles.cellIdentity} role="cell">
                <ProviderIdentityBlock
                  row={row}
                  resolvedTheme={resolvedTheme}
                  copiedKey={copiedKey}
                  onCopyText={handleCopyText}
                />
              </div>

              <div className={styles.cellUrl} role="cell">
                <ProviderBaseUrlBlock
                  row={row}
                  copiedKey={copiedKey}
                  onCopyText={handleCopyText}
                />
              </div>

              <div className={styles.cellModels} role="cell">
                <ProviderModelsTrigger
                  row={row}
                  disabled={actionsDisabled}
                  onOpenModels={handleOpenModels}
                />
              </div>

              <div className={styles.cellPriority} role="cell">
                <ProviderSchedulingBlock
                  row={row}
                  disabled={actionsDisabled}
                  routingStrategy={routingStrategy}
                  globalDisableCooling={globalDisableCooling}
                  onChangePriority={onChangePriority}
                />
              </div>

              <div className={styles.cellRecent} role="cell">
                <ProviderRecentRequestsBlock row={row} />
              </div>

              <div className={styles.cellActions} role="cell">
                <ProviderRowActionGroup
                  row={row}
                  actionsDisabled={actionsDisabled}
                  toggleDisabled={resolvedToggleDisabled}
                  onInspect={onInspect}
                  onOpenModels={handleOpenModels}
                  onOpenConnectivity={onOpenConnectivity}
                  onHealthCheckRow={onHealthCheckRow}
                  onEdit={onEdit}
                  onDelete={onDelete}
                  onToggleEnabled={onToggleEnabled}
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
