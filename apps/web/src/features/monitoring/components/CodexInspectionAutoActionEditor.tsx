import type { TFunction } from 'i18next';
import {
  IconCheck,
  IconCrosshair,
  IconRefreshCw,
  IconShield,
  IconTrash2,
} from '@/components/ui/icons';
import type { CodexInspectionAutoActionMode } from '@/features/monitoring/codexInspection';
import {
  CODEX_INSPECTION_PROBLEM_ACTION_MODES,
  composeCodexInspectionAutoActionMode,
  getCodexInspectionProblemActionMode,
  isCodexInspectionAutoExecutionEnabled,
  type CodexInspectionProblemActionMode,
} from '@/features/monitoring/model/codexInspectionPresentation';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionAutoActionEditorProps = {
  value: CodexInspectionAutoActionMode | string;
  autoRecoverEnabled: boolean;
  t: TFunction;
  onChange: (value: CodexInspectionAutoActionMode) => void;
  onAutoRecoverChange: (value: boolean) => void;
};

const normalizeAutoActionMode = (value: CodexInspectionAutoActionMode | string) => {
  if (value === 'enable' || value === 'disable' || value === 'delete') return value;
  return 'none';
};

const problemActionToneClass: Record<CodexInspectionProblemActionMode, string> = {
  none: styles.settingsAutoOptionEnable,
  disable: styles.settingsAutoOptionDisable,
  delete: styles.settingsAutoOptionDelete,
};

const problemActionIcon: Record<CodexInspectionProblemActionMode, typeof IconCrosshair> = {
  none: IconCrosshair,
  disable: IconShield,
  delete: IconTrash2,
};

export function CodexInspectionAutoActionEditor({
  value,
  autoRecoverEnabled,
  t,
  onChange,
  onAutoRecoverChange,
}: CodexInspectionAutoActionEditorProps) {
  const normalizedValue = normalizeAutoActionMode(value);
  const autoExecutionEnabled = isCodexInspectionAutoExecutionEnabled(
    normalizedValue,
    autoRecoverEnabled
  );
  const problemActionMode = getCodexInspectionProblemActionMode(normalizedValue);

  const selectAutoExecution = (enabled: boolean) => {
    if (!enabled) {
      onAutoRecoverChange(false);
      onChange('none');
      return;
    }
    if (problemActionMode === 'none' && !autoRecoverEnabled) {
      onAutoRecoverChange(true);
    }
    onChange(composeCodexInspectionAutoActionMode(true, problemActionMode));
  };

  const selectProblemAction = (mode: CodexInspectionProblemActionMode) => {
    if (mode === 'none' && !autoRecoverEnabled) {
      onChange('none');
      return;
    }
    onChange(composeCodexInspectionAutoActionMode(true, mode));
  };

  const selectAutoRecovery = (enabled: boolean) => {
    onAutoRecoverChange(enabled);
    if (enabled && normalizedValue === 'none') {
      onChange('enable');
    } else if (!enabled && problemActionMode === 'none') {
      onChange('none');
    }
  };

  const warningClass =
    problemActionMode === 'delete'
      ? styles.settingsAutoWarningDelete
      : problemActionMode === 'disable'
        ? styles.settingsAutoWarningDisable
        : styles.settingsAutoWarningEnable;

  return (
    <div className={styles.settingsAutoContent}>
      <span className={styles.settingsAutoLabel}>
        {t('monitoring.codex_inspection_settings_auto_execution_label')}
      </span>
      <div className={`${styles.settingsAutoCards} ${styles.settingsAutoExecutionCards}`}>
        {[
          {
            key: 'none',
            enabled: false,
            title: t('monitoring.codex_inspection_settings_auto_execution_off'),
            desc: t('monitoring.codex_inspection_settings_auto_execution_off_desc'),
            toneClass: styles.settingsAutoOptionNone,
            Icon: IconCrosshair,
          },
          {
            key: 'auto',
            enabled: true,
            title: t('monitoring.codex_inspection_settings_auto_execution_on'),
            desc: t('monitoring.codex_inspection_settings_auto_execution_on_desc'),
            toneClass: styles.settingsAutoOptionEnable,
            Icon: IconRefreshCw,
          },
        ].map((option) => {
          const active = autoExecutionEnabled === option.enabled;
          return (
            <button
              key={option.key}
              type="button"
              className={[
                styles.settingsAutoOption,
                option.toneClass,
                active ? styles.settingsAutoOptionActive : '',
              ]
                .filter(Boolean)
                .join(' ')}
              onClick={() => selectAutoExecution(option.enabled)}
              aria-pressed={active}
            >
              <span className={styles.settingsAutoOptionIcon}>
                <option.Icon size={30} />
              </span>
              <span className={styles.settingsAutoOptionText}>
                <strong>{option.title}</strong>
                <small>{option.desc}</small>
              </span>
              <span className={styles.settingsAutoOptionCheck}>
                {active ? <IconCheck size={14} /> : null}
              </span>
            </button>
          );
        })}
      </div>

      {autoExecutionEnabled ? (
        <>
          <span className={styles.settingsAutoSubLabel}>
            {t('monitoring.codex_inspection_settings_problem_action_label')}
          </span>
          <div className={`${styles.settingsAutoCards} ${styles.settingsAutoProblemCards}`}>
            {CODEX_INSPECTION_PROBLEM_ACTION_MODES.map((mode) => {
              const active = problemActionMode === mode;
              const ProblemIcon = problemActionIcon[mode];
              return (
                <button
                  key={mode}
                  type="button"
                  className={[
                    styles.settingsAutoOption,
                    problemActionToneClass[mode],
                    active ? styles.settingsAutoOptionActive : '',
                  ]
                    .filter(Boolean)
                    .join(' ')}
                  onClick={() => selectProblemAction(mode)}
                  aria-pressed={active}
                >
                  <span className={styles.settingsAutoOptionIcon}>
                    <ProblemIcon size={28} />
                  </span>
                  <span className={styles.settingsAutoOptionText}>
                    <strong>
                      {t(`monitoring.codex_inspection_settings_problem_action_${mode}`)}
                    </strong>
                    <small>
                      {t(`monitoring.codex_inspection_settings_problem_action_${mode}_desc`)}
                    </small>
                  </span>
                  <span className={styles.settingsAutoOptionCheck}>
                    {active ? <IconCheck size={14} /> : null}
                  </span>
                </button>
              );
            })}
          </div>
          <span className={styles.settingsAutoSubLabel}>
            {t('monitoring.codex_inspection_settings_auto_recover_label')}
          </span>
          <div className={`${styles.settingsAutoCards} ${styles.settingsAutoExecutionCards}`}>
            {[
              {
                key: 'recover-off',
                enabled: false,
                title: t('monitoring.codex_inspection_settings_auto_recover_off'),
                desc: t('monitoring.codex_inspection_settings_auto_recover_off_desc'),
                Icon: IconCrosshair,
              },
              {
                key: 'recover-on',
                enabled: true,
                title: t('monitoring.codex_inspection_settings_auto_recover_on'),
                desc: t('monitoring.codex_inspection_settings_auto_recover_on_desc'),
                Icon: IconRefreshCw,
              },
            ].map((option) => {
              const active = autoRecoverEnabled === option.enabled;
              return (
                <button
                  key={option.key}
                  type="button"
                  className={[
                    styles.settingsAutoOption,
                    option.enabled
                      ? styles.settingsAutoOptionEnable
                      : styles.settingsAutoOptionNone,
                    active ? styles.settingsAutoOptionActive : '',
                  ]
                    .filter(Boolean)
                    .join(' ')}
                  onClick={() => selectAutoRecovery(option.enabled)}
                  aria-pressed={active}
                >
                  <span className={styles.settingsAutoOptionIcon}>
                    <option.Icon size={28} />
                  </span>
                  <span className={styles.settingsAutoOptionText}>
                    <strong>{option.title}</strong>
                    <small>{option.desc}</small>
                  </span>
                  <span className={styles.settingsAutoOptionCheck}>
                    {active ? <IconCheck size={14} /> : null}
                  </span>
                </button>
              );
            })}
          </div>
        </>
      ) : null}

      <p className={styles.settingsAutoHint}>
        {t('monitoring.codex_inspection_settings_auto_action_mode_hint')}
      </p>
      {autoExecutionEnabled ? (
        <p className={`${styles.settingsAutoWarning} ${warningClass}`}>
          {t(`monitoring.codex_inspection_settings_auto_action_mode_${normalizedValue}_warning`)}
        </p>
      ) : null}
    </div>
  );
}
