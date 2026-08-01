import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    request: vi.fn(),
  },
}));

vi.mock('@/services/api', () => ({
  apiCallApi: {
    request: mocks.request,
  },
  getApiCallErrorMessage: (result: { statusCode: number; bodyText?: string }) =>
    `${result.statusCode} ${result.bodyText || 'Request failed'}`.trim(),
}));

import {
  buildGitHubReleasesPageURL,
  fetchPluginReleaseVersions,
  getGitHubRepositorySlug,
  isValidManualReleaseTag,
  normalizePluginReleaseVersions,
  supportsPluginVersionSelection,
} from './pluginReleaseVersions';

beforeEach(() => {
  mocks.request.mockReset();
});

describe('plugin release version helpers', () => {
  it('allows custom versions only for GitHub release installs', () => {
    expect(supportsPluginVersionSelection('github-release')).toBe(true);
    expect(supportsPluginVersionSelection(' GitHub-Release ')).toBe(true);
    expect(supportsPluginVersionSelection('direct')).toBe(false);
    expect(supportsPluginVersionSelection('')).toBe(false);
  });

  it('normalizes GitHub repository slugs from supported repository formats', () => {
    expect(getGitHubRepositorySlug('router-for-me/demo.git')).toBe('router-for-me/demo');
    expect(getGitHubRepositorySlug('github.com/router-for-me/demo')).toBe('router-for-me/demo');
    expect(getGitHubRepositorySlug('https://github.com/router-for-me/demo.git')).toBe(
      'router-for-me/demo'
    );
    expect(getGitHubRepositorySlug('https://www.github.com/router-for-me/demo/releases')).toBe(
      'router-for-me/demo'
    );
    expect(getGitHubRepositorySlug('https://github.com.evil.test/router-for-me/demo')).toBe('');
    expect(getGitHubRepositorySlug('router-for-me')).toBe('');
  });

  it('builds GitHub releases URLs only for GitHub repositories', () => {
    expect(buildGitHubReleasesPageURL('router-for-me/demo')).toBe(
      'https://github.com/router-for-me/demo/releases'
    );
    expect(buildGitHubReleasesPageURL('https://gitlab.com/router-for-me/demo')).toBe('');
  });

  it('validates manual release tags accepted by the install dialog', () => {
    expect(isValidManualReleaseTag('v1.2.3')).toBe(true);
    expect(isValidManualReleaseTag('release_2026.07+hotfix-1')).toBe(true);
    expect(isValidManualReleaseTag('')).toBe(false);
    expect(isValidManualReleaseTag('/v1.2.3')).toBe(false);
    expect(isValidManualReleaseTag('v1.2.3/beta')).toBe(false);
  });

  it('normalizes GitHub release API responses and filters invalid entries', () => {
    expect(
      normalizePluginReleaseVersions([
        {
          tag_name: ' v1.2.0 ',
          name: 'Stable release',
          published_at: '2026-07-09T00:00:00Z',
          prerelease: false,
          html_url: 'https://github.com/router-for-me/demo/releases/tag/v1.2.0',
          assets: [{ name: 'demo-linux-amd64.so' }, { name: '' }, null],
        },
        {
          tag_name: '',
        },
      ])
    ).toEqual([
      {
        tagName: 'v1.2.0',
        name: 'Stable release',
        publishedAt: '2026-07-09T00:00:00Z',
        prerelease: false,
        htmlUrl: 'https://github.com/router-for-me/demo/releases/tag/v1.2.0',
        assetNames: ['demo-linux-amd64.so'],
      },
    ]);
  });

  it('fetches release versions through the proxied API call helper', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 200,
      body: [
        {
          tag_name: 'v1.2.0',
          name: 'Stable',
          published_at: '2026-07-09T00:00:00Z',
          prerelease: false,
          html_url: 'https://github.com/router-for-me/demo/releases/tag/v1.2.0',
          assets: [{ name: 'demo.so' }],
        },
      ],
    });

    await expect(fetchPluginReleaseVersions('router-for-me/demo')).resolves.toEqual([
      {
        tagName: 'v1.2.0',
        name: 'Stable',
        publishedAt: '2026-07-09T00:00:00Z',
        prerelease: false,
        htmlUrl: 'https://github.com/router-for-me/demo/releases/tag/v1.2.0',
        assetNames: ['demo.so'],
      },
    ]);
    expect(mocks.request).toHaveBeenCalledWith({
      method: 'GET',
      url: 'https://api.github.com/repos/router-for-me/demo/releases?per_page=50',
      header: {
        Accept: 'application/vnd.github+json',
        'X-GitHub-Api-Version': '2022-11-28',
      },
    });
  });

  it('throws when GitHub returns a non-success response', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 404,
      bodyText: 'not found',
      body: { message: 'not found' },
    });

    await expect(fetchPluginReleaseVersions('router-for-me/missing')).rejects.toThrow(
      '404 not found'
    );
  });
});
