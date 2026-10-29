import type { ApiCallRequest, ApiCallResult } from '@/services/api/apiCall';
import type { AxiosRequestConfig } from 'axios';

type TokenParameter = 'max_tokens' | 'max_completion_tokens';
type SendRequest = (payload: ApiCallRequest, config?: AxiosRequestConfig) => Promise<ApiCallResult>;

const usesCompletionTokens = (model: string): boolean => {
  const name = model.trim().split('/').pop() ?? '';
  const gptGeneration = /^gpt-(\d+)(?:[.-]|$)/i.exec(name);
  return (gptGeneration !== null && Number(gptGeneration[1]) >= 5) || /^o\d+(?:[.-]|$)/i.test(name);
};

const rejectsTokenParameter = (result: ApiCallResult, parameter: TokenParameter): boolean => {
  if (result.statusCode !== 400 && result.statusCode !== 422) return false;
  const body = result.body as {
    error?: { param?: unknown; code?: unknown; message?: unknown };
  } | null;
  const error = body?.error;
  const message = String(error?.message ?? result.bodyText ?? '').toLowerCase();
  const namesParameter =
    error?.param === parameter || new RegExp(`\\b${parameter}\\b`, 'i').test(message);
  return (
    namesParameter &&
    (error?.code === 'unsupported_parameter' ||
      /unsupported|not supported|unrecognized|unknown|not permitted/.test(message))
  );
};

/** Probe the chosen model without depending on a separate legacy model being configured. */
export async function requestOpenAIConnectivityTest(
  send: SendRequest,
  payload: Omit<ApiCallRequest, 'data'>,
  model: string,
  config?: AxiosRequestConfig
): Promise<ApiCallResult> {
  const modern = usesCompletionTokens(model);
  const tokenLimit = modern ? 128 : 5;
  const parameter: TokenParameter = modern ? 'max_completion_tokens' : 'max_tokens';
  const request = (tokenParameter: TokenParameter) =>
    send(
      {
        ...payload,
        data: JSON.stringify({
          model,
          messages: [{ role: 'user', content: 'Reply with OK.' }],
          stream: false,
          [tokenParameter]: tokenLimit,
        }),
      },
      config
    );

  const result = await request(parameter);
  // Some compatible providers use aliases or support only the older token field.
  // Retry once only for an explicit parameter rejection, never for quota/auth/rate errors.
  if (!rejectsTokenParameter(result, parameter)) return result;
  return request(parameter === 'max_tokens' ? 'max_completion_tokens' : 'max_tokens');
}
