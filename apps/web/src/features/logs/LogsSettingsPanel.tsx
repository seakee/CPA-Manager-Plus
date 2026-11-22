import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { Config } from '@/types';
import type { LogsConfigAuthority } from './useLogsConfigAuthority';
import styles from './LogsPage.module.scss';

export type LogsSettingField =
  | 'logging-to-file'
  | 'debug'
  | 'request-log'
  | 'logs-max-total-size-mb'
  | 'error-logs-max-files';

interface NumericDraft {
  value: string;
  baseline: number;
}

interface LogsSettingsPanelProps {
  config: Config | null;
  authority: LogsConfigAuthority;
  savingField: LogsSettingField | null;
  saveError: string;
  onToggleField: (
    field: 'logging-to-file' | 'debug' | 'request-log',
    next: boolean
  ) => Promise<void>;
  onSaveNumberField: (
    field: 'logs-max-total-size-mb' | 'error-logs-max-files',
    next: number
  ) => Promise<boolean>;
}

const MAX_SAFE_LOG_INTEGER = 1_048_576;

function parseNonNegativeInt(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  if (!Number.isSafeInteger(parsed) || parsed < 0 || parsed > MAX_SAFE_LOG_INTEGER) {
    return null;
  }
  return parsed;
}

export function LogsSettingsPanel({
  config,
  authority,
  savingField,
  saveError,
  onToggleField,
  onSaveNumberField,
}: LogsSettingsPanelProps) {
  const { t } = useTranslation();

  const currentMaxSize = config?.logsMaxTotalSizeMb ?? 0;
  const currentErrorMaxFiles = config?.errorLogsMaxFiles ?? 10;

  const [maxSizeDraft, setMaxSizeDraft] = useState<NumericDraft | null>(null);
  const [errorMaxFilesDraft, setErrorMaxFilesDraft] = useState<NumericDraft | null>(null);
  const [maxSizeValidationError, setMaxSizeValidationError] = useState('');
  const [errorMaxFilesValidationError, setErrorMaxFilesValidationError] = useState('');

  const maxSizeInputValue =
    maxSizeDraft && maxSizeDraft.baseline === currentMaxSize
      ? maxSizeDraft.value
      : String(currentMaxSize);

  const errorMaxFilesInputValue =
    errorMaxFilesDraft && errorMaxFilesDraft.baseline === currentErrorMaxFiles
      ? errorMaxFilesDraft.value
      : String(currentErrorMaxFiles);

  const parsedMaxSize = parseNonNegativeInt(maxSizeInputValue);
  const parsedErrorMaxFiles = parseNonNegativeInt(errorMaxFilesInputValue);

  const isMaxSizeDirty = parsedMaxSize === null || parsedMaxSize !== currentMaxSize;
  const isErrorMaxFilesDirty =
    parsedErrorMaxFiles === null || parsedErrorMaxFiles !== currentErrorMaxFiles;

  const controlsDisabled = !authority.canEditLogs || savingField !== null;

  const handleSaveMaxSize = async () => {
    const parsed = parseNonNegativeInt(maxSizeInputValue);
    if (parsed === null) {
      setMaxSizeValidationError(t('logs.settings_non_negative_integer'));
      return;
    }
    setMaxSizeValidationError('');
    const ok = await onSaveNumberField('logs-max-total-size-mb', parsed);
    if (ok) {
      setMaxSizeDraft(null);
    }
  };

  const handleSaveErrorMaxFiles = async () => {
    const parsed = parseNonNegativeInt(errorMaxFilesInputValue);
    if (parsed === null) {
      setErrorMaxFilesValidationError(t('logs.settings_non_negative_integer'));
      return;
    }
    setErrorMaxFilesValidationError('');
    const ok = await onSaveNumberField('error-logs-max-files', parsed);
    if (ok) {
      setErrorMaxFilesDraft(null);
    }
  };

  const pprofReason =
    authority.mode === 'managed'
      ? t('logs.pprof_managed_posture_reason')
      : t('logs.pprof_deployment_reason');

  return (
    <div className={styles.settingsStack} data-testid="logs-settings-panel">
      <section className={styles.settingsCard}>
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>{t('logs.settings_title')}</h2>
            <p className={styles.panelDescription}>{t('logs.settings_desc')}</p>
          </div>
        </div>

        {(!authority.loading && !authority.canEditLogs && authority.reasonText) || saveError ? (
          <div className={styles.settingsAlerts}>
            {!authority.loading && !authority.canEditLogs && authority.reasonText ? (
              <div
                className={styles.settingsAuthorityNotice}
                role="status"
                data-testid="logs-settings-authority-notice"
              >
                {authority.reasonText}
              </div>
            ) : null}
            {saveError ? (
              <div className="error-box" role="alert" data-testid="logs-settings-error">
                {saveError}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className={styles.settingsList}>
          <div className={styles.settingsRow} data-testid="logs-setting-row-logging-to-file">
            <div className={styles.settingsMeta}>
              <div className={styles.settingsTitleRow}>
                <span className={styles.settingsTitle}>
                  {t('config_management.visual.sections.system.logging_to_file')}
                </span>
                <code className={styles.settingsPath}>observability.logs.logging-to-file</code>
              </div>
              <div className={styles.settingsDescription}>
                {t('logs.settings_logging_to_file_desc')}
              </div>
            </div>
            <div className={styles.settingsControl}>
              <ToggleSwitch
                checked={Boolean(config?.loggingToFile)}
                onChange={(next) => {
                  void onToggleField('logging-to-file', next);
                }}
                disabled={controlsDisabled}
                ariaLabel={t('config_management.visual.sections.system.logging_to_file')}
              />
            </div>
          </div>

          <div className={styles.settingsRow} data-testid="logs-setting-row-debug">
            <div className={styles.settingsMeta}>
              <div className={styles.settingsTitleRow}>
                <span className={styles.settingsTitle}>
                  {t('config_management.visual.sections.system.debug')}
                </span>
                <code className={styles.settingsPath}>observability.logs.debug</code>
              </div>
              <div className={styles.settingsDescription}>
                {t('config_management.visual.sections.system.debug_desc')}
              </div>
            </div>
            <div className={styles.settingsControl}>
              <ToggleSwitch
                checked={Boolean(config?.debug)}
                onChange={(next) => {
                  void onToggleField('debug', next);
                }}
                disabled={controlsDisabled}
                ariaLabel={t('config_management.visual.sections.system.debug')}
              />
            </div>
          </div>

          <div className={styles.settingsRow} data-testid="logs-setting-row-request-log">
            <div className={styles.settingsMeta}>
              <div className={styles.settingsTitleRow}>
                <span className={styles.settingsTitle}>
                  {t('basic_settings.request_log_enable')}
                </span>
                <code className={styles.settingsPath}>observability.logs.request-log</code>
              </div>
              <div className={styles.settingsDescription}>
                {t('basic_settings.request_log_warning')}
              </div>
            </div>
            <div className={styles.settingsControl}>
              <ToggleSwitch
                checked={Boolean(config?.requestLog)}
                onChange={(next) => {
                  void onToggleField('request-log', next);
                }}
                disabled={controlsDisabled}
                ariaLabel={t('basic_settings.request_log_enable')}
              />
            </div>
          </div>

          <div className={styles.settingsRow} data-testid="logs-setting-row-max-size">
            <div className={styles.settingsMeta}>
              <div className={styles.settingsTitleRow}>
                <span className={styles.settingsTitle}>
                  {t('config_management.visual.sections.system.logs_max_size')}
                </span>
                <code className={styles.settingsPath}>
                  observability.logs.logs-max-total-size-mb
                </code>
              </div>
              <div className={styles.settingsDescription}>
                {t('logs.settings_logs_max_size_hint')}
              </div>
            </div>
            <div className={styles.settingsNumberField}>
              <div className={styles.settingsNumberControls}>
                <div className={styles.settingsNumberInput}>
                  <Input
                    type="number"
                    min={0}
                    step={1}
                    inputMode="numeric"
                    aria-label={t('config_management.visual.sections.system.logs_max_size')}
                    value={maxSizeInputValue}
                    onChange={(event) => {
                      setMaxSizeValidationError('');
                      setMaxSizeDraft({ value: event.target.value, baseline: currentMaxSize });
                    }}
                    aria-invalid={Boolean(maxSizeValidationError) || undefined}
                    disabled={controlsDisabled}
                  />
                </div>
                <Button
                  size="sm"
                  onClick={() => {
                    void handleSaveMaxSize();
                  }}
                  loading={savingField === 'logs-max-total-size-mb'}
                  disabled={controlsDisabled || !isMaxSizeDirty}
                  data-testid="logs-setting-save-max-size"
                >
                  {t('common.save')}
                </Button>
              </div>
              {maxSizeValidationError ? (
                <div className={styles.settingsValidationError} role="alert">
                  {maxSizeValidationError}
                </div>
              ) : null}
            </div>
          </div>

          <div className={styles.settingsRow} data-testid="logs-setting-row-error-max-files">
            <div className={styles.settingsMeta}>
              <div className={styles.settingsTitleRow}>
                <span className={styles.settingsTitle}>
                  {t('config_management.visual.sections.system.error_logs_max_files')}
                </span>
                <code className={styles.settingsPath}>
                  observability.logs.error-logs-max-files
                </code>
              </div>
              <div className={styles.settingsDescription}>
                {t('logs.settings_error_logs_max_files_hint')}
              </div>
            </div>
            <div className={styles.settingsNumberField}>
              <div className={styles.settingsNumberControls}>
                <div className={styles.settingsNumberInput}>
                  <Input
                    type="number"
                    min={0}
                    step={1}
                    inputMode="numeric"
                    aria-label={t('config_management.visual.sections.system.error_logs_max_files')}
                    value={errorMaxFilesInputValue}
                    onChange={(event) => {
                      setErrorMaxFilesValidationError('');
                      setErrorMaxFilesDraft({
                        value: event.target.value,
                        baseline: currentErrorMaxFiles,
                      });
                    }}
                    aria-invalid={Boolean(errorMaxFilesValidationError) || undefined}
                    disabled={controlsDisabled}
                  />
                </div>
                <Button
                  size="sm"
                  onClick={() => {
                    void handleSaveErrorMaxFiles();
                  }}
                  loading={savingField === 'error-logs-max-files'}
                  disabled={controlsDisabled || !isErrorMaxFilesDirty}
                  data-testid="logs-setting-save-error-max-files"
                >
                  {t('common.save')}
                </Button>
              </div>
              {errorMaxFilesValidationError ? (
                <div className={styles.settingsValidationError} role="alert">
                  {errorMaxFilesValidationError}
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </section>

      <section className={styles.settingsCard} data-testid="logs-settings-pprof-readonly">
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>{t('logs.pprof_readonly_title')}</h2>
            <p className={styles.panelDescription}>{pprofReason}</p>
          </div>
          <span className={styles.readonlyBadge}>{t('logs.readonly_badge')}</span>
        </div>
        <div className={styles.readonlyList}>
          <div className={styles.readonlyRow}>
            <code className={styles.settingsPath}>observability.pprof.enable</code>
            <span
              className={styles.readonlyStatus}
              data-tone={config?.pprofEnable ? 'enabled' : 'disabled'}
              data-testid="logs-readonly-pprof-enable"
            >
              <span className={styles.statusDot} aria-hidden="true" />
              {config?.pprofEnable ? t('common.enabled') : t('common.disabled')}
            </span>
          </div>
          <div className={styles.readonlyRow}>
            <code className={styles.settingsPath}>observability.pprof.addr</code>
            <code className={styles.readonlyValue} data-testid="logs-readonly-pprof-addr">
              {config?.pprofAddr || '127.0.0.1:8316'}
            </code>
          </div>
        </div>
      </section>
    </div>
  );
}
