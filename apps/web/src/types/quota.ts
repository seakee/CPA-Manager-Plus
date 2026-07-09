/**
 * Quota management types.
 */

// Theme types
export type ThemeColors = { bg: string; text: string; border?: string };
export type TypeColorSet = { light: ThemeColors; dark?: ThemeColors };
export type ResolvedTheme = 'light' | 'dark';

export interface AntigravityQuotaInfo {
  displayName?: string;
  display_name?: string;
  model?: string;
  apiProvider?: string;
  api_provider?: string;
  modelProvider?: string;
  model_provider?: string;
  quotaInfo?: {
    remainingFraction?: number | string;
    remaining_fraction?: number | string;
    remaining?: number | string;
    resetTime?: string;
    reset_time?: string;
  };
  quota_info?: {
    remainingFraction?: number | string;
    remaining_fraction?: number | string;
    remaining?: number | string;
    resetTime?: string;
    reset_time?: string;
  };
}

export type AntigravityModelsPayload = Record<string, AntigravityQuotaInfo>;

export interface AntigravityQuotaSummaryBucketPayload {
  bucketId?: string;
  bucket_id?: string;
  displayName?: string;
  display_name?: string;
  window?: string;
  resetTime?: string;
  reset_time?: string;
  remainingFraction?: number | string;
  remaining_fraction?: number | string;
  description?: string;
}

export interface AntigravityQuotaSummaryGroupPayload {
  displayName?: string;
  display_name?: string;
  description?: string;
  buckets?: AntigravityQuotaSummaryBucketPayload[];
}

export interface AntigravityQuotaSummaryPayload {
  groups?: AntigravityQuotaSummaryGroupPayload[];
  models?: AntigravityModelsPayload;
  defaultAgentModelId?: string;
  default_agent_model_id?: string;
  agentModelSorts?: Array<{
    displayName?: string;
    display_name?: string;
    groups?: Array<{
      modelIds?: string[];
      model_ids?: string[];
    }>;
  }>;
  agent_model_sorts?: Array<{
    displayName?: string;
    display_name?: string;
    groups?: Array<{
      modelIds?: string[];
      model_ids?: string[];
    }>;
  }>;
  commandModelIds?: string[];
  command_model_ids?: string[];
  tabModelIds?: string[];
  tab_model_ids?: string[];
  imageGenerationModelIds?: string[];
  image_generation_model_ids?: string[];
  mqueryModelIds?: string[];
  mquery_model_ids?: string[];
  webSearchModelIds?: string[];
  web_search_model_ids?: string[];
  commitMessageModelIds?: string[];
  commit_message_model_ids?: string[];
  deprecatedModelIds?: Record<
    string,
    {
      newModelId?: string;
      new_model_id?: string;
      oldModelEnum?: string;
      old_model_enum?: string;
      newModelEnum?: string;
      new_model_enum?: string;
    }
  >;
  deprecated_model_ids?: Record<
    string,
    {
      newModelId?: string;
      new_model_id?: string;
      oldModelEnum?: string;
      old_model_enum?: string;
      newModelEnum?: string;
      new_model_enum?: string;
    }
  >;
  tieredModelIds?: Record<string, string[]>;
  tiered_model_ids?: Record<string, string[]>;
}

export interface AntigravityQuotaGroupDefinition {
  id: string;
  label: string;
  identifiers: string[];
  labelFromModel?: boolean;
}

export interface CodexUsageWindow {
  used_percent?: number | string;
  usedPercent?: number | string;
  limit_window_seconds?: number | string;
  limitWindowSeconds?: number | string;
  reset_after_seconds?: number | string;
  resetAfterSeconds?: number | string;
  reset_at?: number | string;
  resetAt?: number | string;
}

export interface CodexRateLimitInfo {
  allowed?: boolean;
  limit_reached?: boolean;
  limitReached?: boolean;
  primary_window?: CodexUsageWindow | null;
  primaryWindow?: CodexUsageWindow | null;
  secondary_window?: CodexUsageWindow | null;
  secondaryWindow?: CodexUsageWindow | null;
}

export interface CodexAdditionalRateLimit {
  limit_name?: string;
  limitName?: string;
  metered_feature?: string;
  meteredFeature?: string;
  rate_limit?: CodexRateLimitInfo | null;
  rateLimit?: CodexRateLimitInfo | null;
}

export interface CodexCreditsInfo {
  has_credits?: boolean;
  hasCredits?: boolean;
  unlimited?: boolean;
  overage_limit_reached?: boolean;
  overageLimitReached?: boolean;
  balance?: number | string | null;
  approx_local_messages?: number | string | null;
  approxLocalMessages?: number | string | null;
  approx_cloud_messages?: number | string | null;
  approxCloudMessages?: number | string | null;
}

export interface CodexSpendControlInfo {
  reached?: boolean;
  individual_limit?: number | string | null;
  individualLimit?: number | string | null;
}

export interface CodexRateLimitResetCreditsInfo {
  available_count?: number | string;
  availableCount?: number | string;
}

export interface CodexRateLimitResetCredit {
  id: string;
  status: string;
  grantedAt: string;
  expiresAt: string;
}

export interface CodexResetCreditsSummary {
  availableCount: number | null;
  credits: CodexRateLimitResetCredit[];
  invalidPayload: boolean;
}

export interface CodexUsagePayload {
  user_id?: string;
  userId?: string;
  account_id?: string;
  accountId?: string;
  email?: string;
  plan_type?: string;
  planType?: string;
  rate_limit?: CodexRateLimitInfo | null;
  rateLimit?: CodexRateLimitInfo | null;
  code_review_rate_limit?: CodexRateLimitInfo | null;
  codeReviewRateLimit?: CodexRateLimitInfo | null;
  additional_rate_limits?: CodexAdditionalRateLimit[] | null;
  additionalRateLimits?: CodexAdditionalRateLimit[] | null;
  credits?: CodexCreditsInfo | null;
  spend_control?: CodexSpendControlInfo | null;
  spendControl?: CodexSpendControlInfo | null;
  rate_limit_reached_type?: string | null;
  rateLimitReachedType?: string | null;
  promo?: unknown;
  referral_beacon?: unknown;
  referralBeacon?: unknown;
  rate_limit_reset_credits?: CodexRateLimitResetCreditsInfo | null;
  rateLimitResetCredits?: CodexRateLimitResetCreditsInfo | null;
  subscription_active_until?: string | number | null;
  subscriptionActiveUntil?: string | number | null;
}

// Claude API payload types
export interface ClaudeUsageWindow {
  utilization: number;
  resets_at: string;
}

export interface ClaudeExtraUsage {
  is_enabled: boolean;
  monthly_limit: number;
  used_credits: number;
  utilization: number | null;
}

export interface ClaudeUsagePayload {
  five_hour?: ClaudeUsageWindow | null;
  seven_day?: ClaudeUsageWindow | null;
  seven_day_oauth_apps?: ClaudeUsageWindow | null;
  seven_day_opus?: ClaudeUsageWindow | null;
  seven_day_sonnet?: ClaudeUsageWindow | null;
  seven_day_cowork?: ClaudeUsageWindow | null;
  iguana_necktie?: ClaudeUsageWindow | null;
  extra_usage?: ClaudeExtraUsage | null;
}

export interface ClaudeProfileResponse {
  account?: {
    uuid?: string;
    full_name?: string;
    display_name?: string;
    email?: string;
    has_claude_max?: boolean;
    has_claude_pro?: boolean;
    created_at?: string;
  };
  organization?: {
    uuid?: string;
    name?: string;
    organization_type?: string;
    billing_type?: string;
    rate_limit_tier?: string;
    has_extra_usage_enabled?: boolean;
    subscription_status?: string;
    subscription_created_at?: string;
  };
}

export interface ClaudeQuotaWindow {
  id: string;
  label: string;
  labelKey?: string;
  usedPercent: number | null;
  resetLabel: string;
}

export interface ClaudeQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  windows: ClaudeQuotaWindow[];
  extraUsage?: ClaudeExtraUsage | null;
  planType?: string | null;
  error?: string;
  errorStatus?: number;
}

// Quota state types
export interface AntigravityQuotaGroup {
  id: string;
  label: string;
  description?: string;
  models?: string[];
  buckets: AntigravityQuotaBucket[];
}

export interface AntigravityQuotaSubscription {
  plan: string | null;
  tierName: string | null;
  tierId: string | null;
}

export interface AntigravityQuotaBucket {
  id: string;
  label: string;
  window?: string;
  remainingFraction: number;
  resetTime?: string;
  description?: string;
}

export interface AntigravityQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  groups: AntigravityQuotaGroup[];
  subscription?: AntigravityQuotaSubscription | null;
  serverTimeOffsetMs?: number | null;
  error?: string;
  errorStatus?: number;
}

export interface CodexQuotaWindow {
  id: string;
  label: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  usedPercent: number | null;
  resetLabel: string;
  limitWindowSeconds?: number | null;
}

export interface CodexQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  windows: CodexQuotaWindow[];
  planType?: string | null;
  activeLimit?: string | null;
  creditsHasCredits?: boolean | null;
  creditsUnlimited?: boolean | null;
  creditsBalance?: string | null;
  rateLimitReachedType?: string | null;
  primaryOverSecondaryLimitPercent?: number | null;
  subscriptionActiveUntil?: string | null;
  rateLimitResetCreditsAvailableCount?: number | null;
  rateLimitResetCredits?: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError?: string | null;
  authFileKey?: string;
  authFileName?: string;
  authIndex?: string | null;
  fetchedAtMs?: number;
  failedAtMs?: number;
  error?: string;
  errorStatus?: number;
  observedFromUsageHeaders?: boolean;
  observedResetCreditsUnknown?: boolean;
  observedAtMs?: number;
  observedTraceId?: string;
  observedErrorKind?: string;
  observedErrorCode?: string;
}

// Kimi API payload types
export interface KimiUsageDetail {
  used?: number;
  limit?: number;
  remaining?: number;
  name?: string;
  title?: string;
  resetAt?: string;
  reset_at?: string;
  resetTime?: string;
  reset_time?: string;
  resetIn?: number;
  reset_in?: number;
  ttl?: number;
}

export interface KimiLimitWindow {
  duration?: number;
  timeUnit?: string;
}

export interface KimiLimitItem {
  name?: string;
  title?: string;
  scope?: string;
  detail?: KimiUsageDetail;
  window?: KimiLimitWindow;
  used?: number;
  limit?: number;
  remaining?: number;
  duration?: number;
  timeUnit?: string;
  resetAt?: string;
  reset_at?: string;
  resetIn?: number;
  reset_in?: number;
  ttl?: number;
}

export interface KimiUsagePayload {
  usage?: KimiUsageDetail;
  limits?: KimiLimitItem[];
}

export interface KimiQuotaRow {
  id: string;
  label?: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  used: number;
  limit: number;
  resetHint?: string;
}

export interface KimiQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  rows: KimiQuotaRow[];
  error?: string;
  errorStatus?: number;
}

// xAI/Grok API payload types
export interface XaiBillingCent {
  val?: number | string;
}

export interface XaiBillingPeriod {
  type?: string;
  start?: string;
  end?: string;
}

export interface XaiBillingProductUsage {
  product?: string;
  usagePercent?: number | string | null;
  usage_percent?: number | string | null;
}

export interface XaiBillingConfig {
  currentPeriod?: XaiBillingPeriod | null;
  current_period?: XaiBillingPeriod | null;
  creditUsagePercent?: number | string | null;
  credit_usage_percent?: number | string | null;
  productUsage?: XaiBillingProductUsage[] | null;
  product_usage?: XaiBillingProductUsage[] | null;
  monthlyLimit?: XaiBillingCent | number | string | null;
  monthly_limit?: XaiBillingCent | number | string | null;
  used?: XaiBillingCent | number | string | null;
  onDemandCap?: XaiBillingCent | number | string | null;
  on_demand_cap?: XaiBillingCent | number | string | null;
  onDemandUsed?: XaiBillingCent | number | string | null;
  on_demand_used?: XaiBillingCent | number | string | null;
  billingPeriodStart?: string;
  billing_period_start?: string;
  billingPeriodEnd?: string;
  billing_period_end?: string;
}

export interface XaiBillingPayload {
  config?: XaiBillingConfig | null;
}

export type XaiBillingPeriodType = 'weekly' | 'monthly' | 'unknown';

export interface XaiProductUsageSummary {
  product: string;
  usagePercent: number | null;
}

export interface XaiBillingSummary {
  periodType: XaiBillingPeriodType;
  usagePercent: number | null;
  periodStart?: string;
  periodEnd?: string;
  productUsage: XaiProductUsageSummary[];
  monthlyLimitCents: number | null;
  usedCents: number | null;
  includedUsedCents: number | null;
  onDemandCapCents: number | null;
  onDemandUsedCents: number | null;
  onDemandUsedPercent: number | null;
  billingPeriodStart?: string;
  billingPeriodEnd?: string;
  usedPercent: number | null;
}

export interface XaiQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  billing: XaiBillingSummary | null;
  error?: string;
  errorStatus?: number;
}
