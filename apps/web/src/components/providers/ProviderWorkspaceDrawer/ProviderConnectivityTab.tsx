import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconChartLine, IconCheck, IconSend, IconX } from '@/components/ui/icons';
import { maskApiKey } from '@/utils/format';
import { hasOpenAIKeyEntryConfiguration } from '@/utils/openAIKeyEntries';
import { OpenAIKeyTestStatusIndicator } from '../OpenAIKeyTestStatusIndicator';
import type { ProviderKind } from '../ProviderTable/rowData';
import type {
  ProviderDrawerConnectivityResult,
  ProviderDrawerFormState,
  ProviderOpenAIKeyTestEntry,
} from './useProviderDrawerEditor';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderConnectivityTabProps {
  activeKind: ProviderKind;
  form: ProviderDrawerFormState;
  actionsDisabled: boolean;
  saving: boolean;
  isTesting: boolean;
  connectivityTestModel: string;
  onConnectivityTestModelChange: (value: string) => void;
  modelSelectOptions: Array<{ value: string; label: string }>;
  singleTestResult: ProviderDrawerConnectivityResult;
  openAIAllTestResult: ProviderDrawerConnectivityResult;
  openAIKeyStatuses: ProviderOpenAIKeyTestEntry[];
  onRunSingleTest: () => Promise<void>;
  onTestSingleOpenAIKey: (index: number) => Promise<boolean>;
  onTestAllOpenAIKeys: () => Promise<void>;
}

const renderStatusPill = (status: ProviderDrawerConnectivityResult['status'], t: (k: string, opts?: Record<string, unknown>) => string) => {
  if (status === 'success') {
    return (
      <span className={`${styles.statusBadge} ${styles.statusBadgeSuccess}`}>
        <IconCheck size={12} />
        {t('ai_providers.connectivity_status_success', { defaultValue: '连通正常' })}
      </span>
    );
  }
  if (status === 'error') {
    return (
      <span className={`${styles.statusBadge} ${styles.statusBadgeDisabled}`}>
        <IconX size={12} />
        {t('ai_providers.connectivity_status_error', { defaultValue: '测试失败' })}
      </span>
    );
  }
  return (
    <span className={styles.dirtyBadge}>
      {t('ai_providers.connectivity_status_loading', { defaultValue: '测试中...' })}
    </span>
  );
};

export function ProviderConnectivityTab({
  activeKind,
  form,
  actionsDisabled,
  saving,
  isTesting,
  connectivityTestModel,
  onConnectivityTestModelChange,
  modelSelectOptions,
  singleTestResult,
  openAIAllTestResult,
  openAIKeyStatuses,
  onRunSingleTest,
  onTestSingleOpenAIKey,
  onTestAllOpenAIKeys,
}: ProviderConnectivityTabProps) {
  const { t } = useTranslation();
  const controlsDisabled = actionsDisabled || saving;
  const isOpenAI = activeKind === 'openai';
  const isModelsProbeKind =
    activeKind === 'gemini' || activeKind === 'interactions' || activeKind === 'vertex';

  const probeDescription = isOpenAI
    ? t('ai_providers.connectivity_desc_openai', {
        defaultValue: '使用当前草稿中的 Base URL、请求头与密钥，向 /chat/completions 发送实时探测请求。',
      })
    : activeKind === 'claude'
      ? t('ai_providers.connectivity_desc_claude', {
          defaultValue: '使用当前草稿向 /v1/messages 发送请求，并附带认证头诊断信息。',
        })
      : activeKind === 'codex' || activeKind === 'xai'
        ? t('ai_providers.connectivity_desc_responses', {
            defaultValue: '使用当前草稿向 /v1/responses 发送请求以验证接口与凭据连通性。',
          })
        : t('ai_providers.connectivity_desc_models_probe', {
            defaultValue: '使用当前草稿中的 Base URL、请求头与密钥向下游拉取模型列表以验证连通性。',
          });

  return (
    <div className={styles.drawerTabPanel}>
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconChartLine size={15} />
            <h4>
              {t('ai_providers.connectivity_panel_title', { defaultValue: '实时草稿连通性诊断' })}
            </h4>
          </div>
          {isOpenAI && (
            <Button
              variant="primary"
              size="xs"
              loading={isTesting}
              disabled={controlsDisabled || isTesting}
              onClick={() => void onTestAllOpenAIKeys()}
            >
              <IconSend size={12} />
              {t('ai_providers.openai_test_all_action')}
            </Button>
          )}
        </div>

        <p className={styles.sectionHint}>{probeDescription}</p>

        {!isModelsProbeKind && (
          <div className={styles.connectivityControls}>
            <div className={styles.fieldGroup}>
              {modelSelectOptions.length > 0 ? (
                <div className={styles.formGridTwo}>
                  <Select
                    value={
                      modelSelectOptions.some((opt) => opt.value === connectivityTestModel)
                        ? connectivityTestModel
                        : ''
                    }
                    options={modelSelectOptions}
                    placeholder={t('ai_providers.select_test_model_placeholder', {
                      defaultValue: '选择已配置模型',
                    })}
                    onChange={(nextVal) => {
                      if (nextVal) onConnectivityTestModelChange(nextVal);
                    }}
                    disabled={controlsDisabled || isTesting}
                    ariaLabel={t('ai_providers.test_model_label', { defaultValue: '测试模型' })}
                  />
                  <Input
                    placeholder={t('ai_providers.custom_test_model_placeholder', {
                      defaultValue: '或输入自定义测试模型',
                    })}
                    value={connectivityTestModel}
                    onChange={(e) => onConnectivityTestModelChange(e.target.value)}
                    disabled={controlsDisabled || isTesting}
                  />
                </div>
              ) : (
                <Input
                  label={t('ai_providers.test_model_label', { defaultValue: '测试模型' })}
                  placeholder={t('ai_providers.custom_test_model_placeholder', {
                    defaultValue: '输入测试模型名称',
                  })}
                  value={connectivityTestModel}
                  onChange={(e) => onConnectivityTestModelChange(e.target.value)}
                  disabled={controlsDisabled || isTesting}
                />
              )}
            </div>

            {!isOpenAI && (
              <Button
                variant="primary"
                size="sm"
                loading={isTesting}
                disabled={controlsDisabled || isTesting}
                onClick={() => void onRunSingleTest()}
              >
                <IconSend size={13} />
                {t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })}
              </Button>
            )}
          </div>
        )}

        {isModelsProbeKind && (
          <div className={styles.sectionMetaActions}>
            <Button
              variant="primary"
              size="sm"
              loading={isTesting}
              disabled={controlsDisabled || isTesting}
              onClick={() => void onRunSingleTest()}
            >
              <IconSend size={13} />
              {t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })}
            </Button>
          </div>
        )}

        {!isOpenAI && singleTestResult.status !== 'idle' && (
          <div
            className={`${styles.connectivityResultCard} ${
              singleTestResult.status === 'success'
                ? styles.connectivityResultSuccess
                : singleTestResult.status === 'error'
                  ? styles.connectivityResultError
                  : ''
            }`}
            role="status"
          >
            <div className={styles.connectivityResultHeader}>
              {renderStatusPill(singleTestResult.status, t)}
              <div className={styles.connectivityMetrics}>
                {singleTestResult.statusCode !== undefined && (
                  <span>HTTP {singleTestResult.statusCode}</span>
                )}
                {singleTestResult.elapsedMs !== undefined && (
                  <span>{singleTestResult.elapsedMs} ms</span>
                )}
              </div>
            </div>
            {singleTestResult.endpoint && (
              <div className={styles.connectivityEndpoint}>{singleTestResult.endpoint}</div>
            )}
            {singleTestResult.message && (
              <div className={styles.connectivityMessage}>{singleTestResult.message}</div>
            )}
          </div>
        )}

        {isOpenAI && openAIAllTestResult.status !== 'idle' && (
          <div
            className={`${styles.connectivityResultCard} ${
              openAIAllTestResult.status === 'success'
                ? styles.connectivityResultSuccess
                : openAIAllTestResult.status === 'error'
                  ? styles.connectivityResultError
                  : ''
            }`}
            role="status"
          >
            <div className={styles.connectivityResultHeader}>
              {renderStatusPill(openAIAllTestResult.status, t)}
              <div className={styles.connectivityMetrics}>
                {openAIAllTestResult.elapsedMs !== undefined && (
                  <span>{openAIAllTestResult.elapsedMs} ms</span>
                )}
              </div>
            </div>
            {openAIAllTestResult.endpoint && (
              <div className={styles.connectivityEndpoint}>{openAIAllTestResult.endpoint}</div>
            )}
            {openAIAllTestResult.message && (
              <div className={styles.connectivityMessage}>{openAIAllTestResult.message}</div>
            )}
          </div>
        )}
      </section>

      {isOpenAI && (
        <section className={styles.sectionCard}>
          <div className={styles.sectionHeader}>
            <div className={styles.sectionHeading}>
              <h4>{t('ai_providers.per_key_test_title', { defaultValue: '逐密钥测活状态' })}</h4>
            </div>
          </div>

          <div className={styles.keyList}>
            {form.apiKeyEntries.map((entry, idx) => {
              const keyStatus = openAIKeyStatuses[idx];
              const configured = hasOpenAIKeyEntryConfiguration(entry);
              const rawKey = entry.apiKey.trim();

              return (
                <div key={idx} className={styles.openAIKeyTestRow}>
                  <div className={styles.openAIKeyTestHeader}>
                    <div className={styles.openAIKeyTestIdentity}>
                      <span className={styles.keyIndexBadge}>#{idx + 1}</span>
                      <code className={styles.keyValueCode}>
                        {rawKey
                          ? maskApiKey(rawKey)
                          : t('ai_providers.overview_keyless_entry', { defaultValue: '无密钥通道' })}
                      </code>
                      <OpenAIKeyTestStatusIndicator
                        status={keyStatus?.status}
                        message={keyStatus?.message}
                      />
                    </div>
                    <div className={styles.sectionMetaActions}>
                      {keyStatus?.statusCode !== undefined && (
                        <span className={styles.connectivityMetrics}>HTTP {keyStatus.statusCode}</span>
                      )}
                      {keyStatus?.elapsedMs !== undefined && (
                        <span className={styles.connectivityMetrics}>{keyStatus.elapsedMs} ms</span>
                      )}
                      <Button
                        variant="secondary"
                        size="xs"
                        disabled={controlsDisabled || isTesting || !configured}
                        onClick={() => void onTestSingleOpenAIKey(idx)}
                      >
                        {t('ai_providers.test_single_key_btn', { defaultValue: '测试' })}
                      </Button>
                    </div>
                  </div>
                  {keyStatus?.status === 'error' && keyStatus.message && (
                    <div className={styles.inlineError}>{keyStatus.message}</div>
                  )}
                </div>
              );
            })}
          </div>
        </section>
      )}
    </div>
  );
}
