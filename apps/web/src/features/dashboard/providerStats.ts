type ProviderListResult<T> = PromiseSettledResult<T[]>;

const getErrorStatus = (error: unknown): unknown =>
  typeof error === 'object' && error !== null && 'status' in error
    ? (error as { status?: unknown }).status
    : undefined;

export const resolveProviderCount = <T>(
  result: ProviderListResult<T>,
  options: { unsupportedAsZero?: boolean } = {}
): number | null => {
  if (result.status === 'fulfilled') {
    return result.value.length;
  }
  if (options.unsupportedAsZero && getErrorStatus(result.reason) === 404) {
    return 0;
  }
  return null;
};
