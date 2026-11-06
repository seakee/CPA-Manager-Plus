import { describe, expect, it } from 'vitest';
import type { PluginListEntry } from '@/types';
import { buildPluginDisplay, getPluginStatusDisplay } from './pluginDisplay';

const basePlugin: PluginListEntry = {
  id: 'codex-invite',
  path: 'plugins/linux/amd64/codex-invite.so',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: false,
  supportsQuota: false,
  logo: '',
  configFields: [],
  menus: [
    {
      path: '/v0/resource/plugins/codex-invite/invite',
      menu: 'Codex Invite',
      description: 'Send Codex invite emails with a selected Codex credential.',
    },
  ],
  metadata: {
    name: 'Codex Invite',
    version: '0.1.4',
    author: 'router-for-me',
    githubRepository: 'https://github.com/router-for-me/cpa-plugin-codex-invite',
    logo: '',
    configFields: [],
  },
};

describe('plugin display helpers', () => {
  it('builds installed-list display data from plugin metadata and menus', () => {
    const display = buildPluginDisplay(basePlugin);

    expect(display.title).toBe('Codex Invite');
    expect(display.subtitleParts).toEqual(['codex-invite', 'v0.1.4', 'router-for-me']);
    expect(display.status).toMatchObject({
      key: 'effective',
      labelKey: 'plugin_management.status_effective',
      tone: 'success',
    });
    expect(display.menuCount).toBe(1);
    expect(display.primaryMenuLabel).toBe('Codex Invite');
    expect(display.primaryMenuDescription).toBe(
      'Send Codex invite emails with a selected Codex credential.'
    );
  });

  it('uses actionable inactive status priority', () => {
    expect(
      getPluginStatusDisplay({
        ...basePlugin,
        effectiveEnabled: false,
        enabled: false,
      })
    ).toMatchObject({ key: 'disabled', tone: 'muted' });

    expect(
      getPluginStatusDisplay({
        ...basePlugin,
        effectiveEnabled: false,
        registered: false,
      })
    ).toMatchObject({ key: 'not_registered', tone: 'warning' });

    expect(
      getPluginStatusDisplay(
        {
          ...basePlugin,
          effectiveEnabled: false,
        },
        { pluginsEnabled: false }
      )
    ).toMatchObject({ key: 'global_disabled', tone: 'warning' });
  });
});
