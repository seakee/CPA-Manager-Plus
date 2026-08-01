import { IconCopy, IconPencil, IconSettings } from '@/components/ui/icons';
import type { ConfigOverviewItem } from '@/features/monitoring/model/codexInspectionPresentation';
import { copyToClipboard } from '@/utils/clipboard';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionConfigOverviewProps = {
  title: string;
  editLabel: string;
  copyLabel: string;
  copiedLabel: string;
  items: ConfigOverviewItem[];
  onEdit: (field?: string) => void;
  ariaLabel?: string;
  compact?: boolean;
  embedded?: boolean;
};

// 已存在配置的「读」面板:可点击的 label/value 概览卡,点任意卡片直达对应字段编辑。
export function CodexInspectionConfigOverview({
  title,
  editLabel,
  copyLabel,
  copiedLabel,
  items,
  onEdit,
  ariaLabel,
  compact = false,
  embedded = false,
}: CodexInspectionConfigOverviewProps) {
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

  const copyValue = async (item: ConfigOverviewItem) => {
    const copied = await copyToClipboard(item.value);
    if (!copied) return;
    setCopiedKey(item.key);
    window.setTimeout(
      () => setCopiedKey((current) => (current === item.key ? null : current)),
      1500
    );
  };

  return (
    <section
      className={[
        styles.configOverview,
        compact ? styles.configOverviewCompact : '',
        embedded ? styles.configOverviewEmbedded : '',
      ]
        .filter(Boolean)
        .join(' ')}
      aria-label={ariaLabel ?? title}
    >
      <header className={styles.configOverviewHeader}>
        <div>
          <span className={styles.configOverviewTitle}>{title}</span>
        </div>
        <button type="button" className={styles.configOverviewEdit} onClick={() => onEdit()}>
          <IconSettings size={14} />
          <span>{editLabel}</span>
        </button>
      </header>
      <div className={styles.configOverviewGrid}>
        {items.map((item) => (
          <div
            key={item.key}
            className={[
              styles.configOverviewItemShell,
              item.display ? styles[`display-${item.display}`] : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <button
              type="button"
              className={[styles.configOverviewItem, item.tone ? styles[`tone-${item.tone}`] : '']
                .filter(Boolean)
                .join(' ')}
              onClick={() => onEdit(item.field)}
              title={item.value}
              aria-label={`${item.label}: ${item.value}`}
            >
              <span className={styles.configOverviewLabel}>{item.label}</span>
              <strong className={styles.configOverviewValue}>{item.value}</strong>
              {item.hint ? <span className={styles.configOverviewHint}>{item.hint}</span> : null}
              <IconPencil
                className={styles.configOverviewItemEditIcon}
                size={13}
                aria-hidden="true"
              />
            </button>
            {item.display === 'long-text' ? (
              <button
                type="button"
                className={styles.configOverviewCopy}
                aria-label={`${copiedKey === item.key ? copiedLabel : copyLabel}: ${item.label}`}
                title={copiedKey === item.key ? copiedLabel : copyLabel}
                onClick={() => void copyValue(item)}
              >
                <IconCopy size={14} aria-hidden="true" />
              </button>
            ) : null}
          </div>
        ))}
      </div>
    </section>
  );
}
import { useState } from 'react';
