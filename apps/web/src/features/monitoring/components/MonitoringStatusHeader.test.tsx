import type { ReactElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter, Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MonitoringStatusHeader } from './MonitoringStatusHeader';

const t = ((key: string) => key) as TFunction;
let renderer: ReactTestRenderer | undefined;

async function mount(action?: ReactElement) {
  await act(async () => {
    renderer = create(
      <MemoryRouter>
        <MonitoringStatusHeader
          showLoadingOverlay={false}
          monitoringUnavailable
          monitoringUnavailableTitle="monitoring.request_monitoring_disabled_title"
          monitoringUnavailableBody="monitoring.request_monitoring_disabled_body"
          monitoringUnavailableAction={action}
          t={t}
        />
      </MemoryRouter>
    );
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
});

describe('MonitoringStatusHeader unavailable callout', () => {
  it('sends the user to the configuration workspace when the page cannot act', async () => {
    await mount();

    const links = renderer!.root.findAllByType(Link);
    expect(links).toHaveLength(1);
    expect(links[0].props.to).toBe('/config');
    expect(links[0].props.children).toBe('monitoring.open_manager_config');
  });

  it('renders the caller action instead of the configuration link', async () => {
    const onEnable = vi.fn();
    await mount(
      <button type="button" data-testid="enable" onClick={onEnable}>
        enable
      </button>
    );

    expect(renderer!.root.findAllByType(Link)).toHaveLength(0);
    const button = renderer!.root.findByProps({ 'data-testid': 'enable' });
    await act(async () => button.props.onClick());
    expect(onEnable).toHaveBeenCalledTimes(1);
  });
});
