import { createContext, useContext } from 'react';
import type { PublicApiClient } from '@/fullweb/publicApi';

export const LogsAuthorityClientContext = createContext<PublicApiClient | null>(null);

export function useLogsAuthorityClient(): PublicApiClient | null {
  return useContext(LogsAuthorityClientContext);
}
