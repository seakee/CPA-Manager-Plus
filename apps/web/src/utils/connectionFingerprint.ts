const readTrimmedString = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

export const createConnectionFingerprint = (
  apiBase: string,
  managementKey: string
): string | null => {
  const normalizedApiBase = readTrimmedString(apiBase).replace(/\/+$/, '');
  const normalizedManagementKey = readTrimmedString(managementKey);
  if (!normalizedApiBase || !normalizedManagementKey) return null;

  const input = `${normalizedApiBase}\u0000${normalizedManagementKey}`;
  let hashA = 0x811c9dc5;
  let hashB = 0x9e3779b9;

  for (let index = 0; index < input.length; index += 1) {
    const code = input.charCodeAt(index);
    hashA = Math.imul(hashA ^ code, 0x01000193);
    hashB = Math.imul(hashB ^ code, 0x85ebca6b);
  }

  return `v1:${(hashA >>> 0).toString(36)}${(hashB >>> 0).toString(36)}`;
};
