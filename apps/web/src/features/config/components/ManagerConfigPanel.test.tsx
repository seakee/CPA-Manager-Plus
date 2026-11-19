import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import en from '@/i18n/locales/en.json';
import { ManagerConfigPanel } from './ManagerConfigPanel';

vi.mock('./AccountProcessingPolicySection', () => ({
  AccountProcessingPolicySection: () => null,
}));

describe('ManagerConfigPanel collector fields', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
  });

  async function renderPanel(collectorMode: string) {
    await act(async () => {
      renderer = create(
        <ManagerConfigPanel
          managerLoading={false}
          managerSaving={false}
          panelHostedByUsageService={true}
          detectedPanelBase="https://manager.test"
          managerRuntimeModeLabel="embedded"
          managerHasBoundCPAManagementKey={true}
          managerCPABaseInput="https://cpa.example"
          managerCPAManagementKeyInput=""
          managerCPAManagementKeyVisible={false}
          managerBoundCPABase="https://cpa.example"
          disableControls={false}
          canConfigureRequestMonitoring={true}
          managerRequestMonitoringEnabled={true}
          managerCollectorMode={collectorMode}
          managerCollectorModeOptions={[
            { value: 'auto', label: 'auto' },
            { value: 'resp', label: 'resp' },
            { value: 'subscribe', label: 'subscribe' },
            { value: 'http', label: 'http' },
          ]}
          managerPollIntervalMs="500"
          managerBatchSize="100"
          managerQueryLimit="50000"
          managerQueue="usage"
          managerPopSide="right"
          managerTlsSkipVerify={false}
          managerRetentionSeconds={0}
          managerConfigSourceLabel="db"
          managerUsageStatisticsEnabled={true}
          onRefresh={vi.fn()}
          onRequestMonitoringChange={vi.fn()}
          onCPABaseInputChange={vi.fn()}
          onCPAManagementKeyInputChange={vi.fn()}
          onCPAManagementKeyClear={vi.fn()}
          onCPAManagementKeyVisibilityToggle={vi.fn()}
          onCollectorModeChange={vi.fn()}
          onPollIntervalMsChange={vi.fn()}
          onBatchSizeChange={vi.fn()}
          onQueryLimitChange={vi.fn()}
          onQueueChange={vi.fn()}
          onPopSideChange={vi.fn()}
          onTlsSkipVerifyChange={vi.fn()}
        />
      );
    });
  }

  it('renders queue and tlsSkipVerify without pop_side in subscribe mode', async () => {
    await renderPanel('subscribe');
    const root = renderer!.root;
    expect(
      root.findAll((node) => node.type === 'input' && node.props.placeholder === 'usage')
    ).toHaveLength(1);
    expect(
      root.findAll(
        (node) =>
          node.type === 'span' &&
          node.props.children === en.config_management.manager.tls_skip_verify
      )
    ).toHaveLength(1);
    expect(
      root.findAll(
        (node) =>
          node.type === 'button' &&
          node.props['aria-label'] === en.config_management.manager.pop_side
      )
    ).toHaveLength(0);
  });

  it('renders queue, pop_side, and tlsSkipVerify in auto/resp modes and hides all in http mode', async () => {
    await renderPanel('resp');
    let root = renderer!.root;
    expect(
      root.findAll((node) => node.type === 'input' && node.props.placeholder === 'usage')
    ).toHaveLength(1);
    expect(
      root.findAll(
        (node) =>
          node.type === 'button' &&
          node.props['aria-label'] === en.config_management.manager.pop_side
      )
    ).toHaveLength(1);
    expect(
      root.findAll(
        (node) =>
          node.type === 'span' &&
          node.props.children === en.config_management.manager.tls_skip_verify
      )
    ).toHaveLength(1);

    await renderPanel('http');
    root = renderer!.root;
    expect(
      root.findAll((node) => node.type === 'input' && node.props.placeholder === 'usage')
    ).toHaveLength(0);
    expect(
      root.findAll(
        (node) =>
          node.type === 'button' &&
          node.props['aria-label'] === en.config_management.manager.pop_side
      )
    ).toHaveLength(0);
    expect(
      root.findAll(
        (node) =>
          node.type === 'span' &&
          node.props.children === en.config_management.manager.tls_skip_verify
      )
    ).toHaveLength(0);
  });
});
