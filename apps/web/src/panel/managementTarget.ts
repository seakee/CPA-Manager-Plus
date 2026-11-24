// The legacy CPA client remains the Panel transport. Its relative endpoint
// paths must stay beneath the configured CPA Management namespace.
const ALLOWED_V8_MANAGEMENT_PATHS: Record<string, true> = {
  '/credentials': true,
  '/oauth/siwc/bind': true,
  '/oauth/callback': true,
  '/oauth/status': true,
  '/oauth/session': true,
};

export function assertPanelManagementTarget(base: string, resource: string): void {
  const path = resource.split('?')[0];
  const normalizedBase = base.trim().replace(/\/+$/, '');
  const isV0Base = /\/v0\/management$/i.test(normalizedBase);
  const isV8Base = /\/v8\/management$/i.test(normalizedBase);
  const fullWebLegacyBridge = /\/api\/v2\/cpamp\/v0\/management$/i.test(normalizedBase);
  const fullWebV8Bridge = /\/api\/v2\/cpamp\/v8\/management$/i.test(normalizedBase);
  if (
    normalizedBase.startsWith('//') ||
    (!isV0Base && !isV8Base) ||
    (/\/api\/v2(?:\/|$)/i.test(normalizedBase) &&
      !(isV0Base ? fullWebLegacyBridge : fullWebV8Bridge)) ||
    !path.startsWith('/') ||
    path.startsWith('//') ||
    /[\\#]/.test(resource) ||
    /%(?:2e|5c)/i.test(path) ||
    path.split('/').some((segment) => segment === '.' || segment === '..') ||
    /^\/api\/v2(?:\/|$)/i.test(path) ||
    (isV8Base && !Object.prototype.hasOwnProperty.call(ALLOWED_V8_MANAGEMENT_PATHS, path))
  ) {
    throw new Error('Panel Lite requests require a CPA Management target');
  }
}
