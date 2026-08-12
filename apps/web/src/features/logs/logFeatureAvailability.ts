import type { Config } from '@/types';

export function isFileLogsAvailable(config?: Pick<Config, 'loggingToFile'> | null): boolean {
  return config?.loggingToFile === true;
}
