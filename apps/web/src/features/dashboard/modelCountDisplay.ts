export const getDashboardModelCountDisplay = (
  count: number,
  loading: boolean,
  error: string | null
) => (count > 0 ? count : loading || error ? '-' : count);
