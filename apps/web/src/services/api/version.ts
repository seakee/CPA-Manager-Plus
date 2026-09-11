/**
 * 版本相关 API
 */

import axios from 'axios';
import { REQUEST_TIMEOUT_MS } from '@/utils/constants';
import { apiClient } from './client';

export const CPA_MANAGER_UPDATE_INDEX_URL =
  'https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/update-index.json';

export interface ManagerLatestRelease {
  tag_name?: string;
  name?: string;
  html_url?: string;
  published_at?: string;
  [key: string]: unknown;
}

export const versionApi = {
  checkLatest: () => apiClient.get<Record<string, unknown>>('/latest-version'),
  checkManagerUpdateIndex: async () => {
    const response = await axios.get(CPA_MANAGER_UPDATE_INDEX_URL, {
      timeout: REQUEST_TIMEOUT_MS,
    });
    return response.data;
  },
};
