import { createRef } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import type { InspectionLogViewEntry } from '@/features/monitoring/model/codexInspectionPresentation';
import { CodexInspectionLogsPanel } from './CodexInspectionLogsPanel';

const logs: InspectionLogViewEntry[] = [
  {
    id: 'info-log',
    level: 'info',
    message: '正在加载认证文件，目标类型：Codex + xAI',
    timestamp: Date.UTC(2026, 6, 23, 12, 0, 0),
  },
  {
    id: 'error-log',
    level: 'error',
    message: 'xai@example.com -> 禁用（真实推理检查：消费额度或积分已用尽）',
    detail: '{"provider":"xai","inspectionMode":"真实推理检查"}',
    timestamp: Date.UTC(2026, 6, 23, 12, 0, 1),
  },
];

const renderPanel = ({
  logsCollapsed = false,
  levelFilter = 'all',
}: {
  logsCollapsed?: boolean;
  levelFilter?: 'all' | 'info' | 'success' | 'warning' | 'error';
} = {}) =>
  renderToStaticMarkup(
    <CodexInspectionLogsPanel
      logs={logs}
      logsCollapsed={logsCollapsed}
      levelFilter={levelFilter}
      logListRef={createRef<HTMLDivElement>()}
      locale="zh-CN"
      t={i18n.getFixedT('zh-CN')}
      onLevelFilterChange={vi.fn()}
      onToggleCollapsed={vi.fn()}
    />
  );

describe('CodexInspectionLogsPanel', () => {
  it('renders concise summaries and keeps structured data behind details', () => {
    const markup = renderPanel();

    expect(markup).toContain('正在加载认证文件，目标类型：Codex + xAI');
    expect(markup).toContain('xai@example.com -&gt; 禁用（真实推理检查：消费额度或积分已用尽）');
    expect(markup).toContain('<details');
    expect(markup).toContain('详情');
    expect(markup).toContain('inspectionMode');
  });

  it('applies the same level filter to local and server log entries', () => {
    const markup = renderPanel({ levelFilter: 'error' });

    expect(markup).not.toContain('正在加载认证文件，目标类型：Codex + xAI');
    expect(markup).toContain('xai@example.com -&gt; 禁用（真实推理检查：消费额度或积分已用尽）');
  });

  it('distinguishes an empty level filter from an inspection that has not started', () => {
    const markup = renderPanel({ levelFilter: 'warning' });

    expect(markup).toContain('当前级别筛选下没有日志');
    expect(markup).not.toContain('尚未开始巡检');
  });

  it('uses the shared collapsed presentation without rendering log details', () => {
    const markup = renderPanel({ logsCollapsed: true });

    expect(markup).toContain('巡检日志已折叠，共 2 条。');
    expect(markup).not.toContain('<details');
    expect(markup).not.toContain('xai@example.com');
  });
});
