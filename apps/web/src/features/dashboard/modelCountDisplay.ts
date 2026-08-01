export const getDashboardModelCountDisplay = (
  count: number,
  loading: boolean,
  error: string | null
) => (loading || error ? '-' : count);
