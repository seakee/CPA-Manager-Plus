/**
 * 认证文件相关类型
 * 基于原项目 src/modules/auth-files.js
 */

import type { RecentRequestBucket } from '@/utils/recentRequests';

export type AuthFileType =
  | 'qwen'
  | 'kimi'
  | 'gemini'
  | 'aistudio'
  | 'claude'
  | 'codex'
  | 'antigravity'
  | 'xai'
  | 'iflow'
  | 'vertex'
  | 'empty'
  | 'unknown';

export interface AuthFileItem {
  id?: string;
  name: string;
  type?: AuthFileType | string;
  provider?: string;
  size?: number;
  authIndex?: string | number | null;
  runtimeOnly?: boolean | string;
  disabled?: boolean;
  weight?: number;
  unavailable?: boolean;
  status?: string;
  statusMessage?: string;
  lastRefresh?: string | number;
  modified?: number;
  success?: unknown;
  failed?: unknown;
  project_id?: string;
  projectId?: string;
  gemini_virtual_project?: string;
  geminiVirtualProject?: string;
  recent_requests?: RecentRequestBucket[];
  recentRequests?: RecentRequestBucket[];
  siwc_bind_supported?: boolean;
  siwcBindSupported?: boolean;
  use_siwc?: boolean;
  useSiwc?: boolean;
  siwc_bound?: boolean;
  siwcBound?: boolean;
  siwc_expires_at?: string;
  siwcExpiresAt?: string;
  siwc_status?: string;
  siwcStatus?: string;
  [key: string]: unknown;
}

export interface AuthFilesResponse {
  files: AuthFileItem[];
  total?: number;
}
