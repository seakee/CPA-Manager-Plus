import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { CodexInspectionQuotaWindows } from './CodexInspectionQuotaWindows';
import styles from '../CodexInspectionPage.module.scss';

const t = ((key: string, options?: Record<string, unknown>) =>
  options?.percent ? `${key}:${options.percent}` : key) as never;

const collectText = (renderer: ReactTestRenderer) =>
  renderer.root
    .findAll((node) => typeof node.children[0] === 'string')
    .flatMap((node) => node.children.filter((child): child is string => typeof child === 'string'));

describe('CodexInspectionQuotaWindows', () => {
  it('shows the remaining percentage using the remaining-width progress bar', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <CodexInspectionQuotaWindows
          windows={[{ id: 'monthly', labelKey: 'monthly', usedPercent: 3 }]}
          t={t}
        />
      );
    });

    expect(collectText(renderer!)).toContain('monitoring.codex_inspection_quota_remaining:97%');
    expect(renderer!.root.find((node) => node.props.style?.width).props.style.width).toBe('97%');
  });

  it('collapses quota windows without usage percentages into one unavailable state', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <CodexInspectionQuotaWindows
          windows={[
            { id: 'weekly', labelKey: 'weekly', usedPercent: null },
            { id: 'monthly', labelKey: 'monthly', usedPercent: null },
          ]}
          t={t}
        />
      );
    });

    expect(collectText(renderer!)).toEqual(['monitoring.codex_inspection_quota_unavailable']);
    expect(
      renderer!.root.findAll((node) => node.props.className === styles.quotaWindowPlaceholderBar)
    ).toHaveLength(1);
  });
});
