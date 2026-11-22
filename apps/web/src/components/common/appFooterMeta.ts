export const APP_REPOSITORY_URL = 'https://github.com/seakee/CPA-Manager-Plus';
export const APP_RELEASES_URL = `${APP_REPOSITORY_URL}/releases`;
export const APP_AUTHOR_URL = 'https://github.com/seakee';
const FALLBACK_APP_VERSION = 'v2.0.0';

const PRERELEASE_IDENTIFIER = String.raw`(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)`;
const RELEASE_VERSION_PATTERN = new RegExp(
  String.raw`^[vV]?(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-${PRERELEASE_IDENTIFIER}(?:\.${PRERELEASE_IDENTIFIER})*)?$`
);
const GIT_DESCRIBE_VERSION_PATTERN = /-\d+-g[0-9a-f]+(?:-dirty)?$/i;
const DEV_BUILD_VERSION_PATTERN =
  /^[vV]?((?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))-dev(?:\.[0-9a-zA-Z-]+)?$/;

export interface AppFooterVersionMeta {
  label: string;
  href: string;
  title?: string;
}

export function resolveAppFooterVersion(rawVersion?: string | null): AppFooterVersionMeta {
  const trimmed = rawVersion?.trim() || '';
  if (!trimmed || trimmed.toLowerCase() === 'dev' || trimmed.toLowerCase() === 'unknown') {
    return { label: FALLBACK_APP_VERSION, href: APP_RELEASES_URL };
  }

  const devMatch = trimmed.match(DEV_BUILD_VERSION_PATTERN);
  if (devMatch) {
    const fullTag =
      trimmed.startsWith('v') || trimmed.startsWith('V') ? `v${trimmed.slice(1)}` : `v${trimmed}`;
    return {
      label: `v${devMatch[1]}`,
      href: APP_RELEASES_URL,
      title: fullTag,
    };
  }

  if (!GIT_DESCRIBE_VERSION_PATTERN.test(trimmed) && RELEASE_VERSION_PATTERN.test(trimmed)) {
    const tag = `v${trimmed.replace(/^[vV]/, '')}`;
    return {
      label: tag,
      href: `${APP_RELEASES_URL}/tag/${encodeURIComponent(tag)}`,
    };
  }

  return {
    label: FALLBACK_APP_VERSION,
    href: APP_RELEASES_URL,
    title: trimmed,
  };
}
