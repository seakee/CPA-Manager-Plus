import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  IconCheck,
  IconEye,
  IconEyeOff,
  IconInfo,
  IconKey,
  IconLanguages,
  IconMoon,
  IconShield,
  IconSun,
  IconTimer,
} from '@/components/ui/icons';
import {
  useAuthStore,
  useLanguageStore,
  useNotificationStore,
  useThemeStore,
  useUsageServiceStore,
} from '@/stores';
import {
  LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY,
  USAGE_SERVICE_LAST_CPA_BASE_KEY,
  getUsageServiceErrorCode,
  usageServiceApi,
} from '@/services/api/usageService';
import {
  detectApiBaseFromLocation,
  normalizeApiBase,
  resolveDefaultCPAConnectionBase,
} from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import type { ApiError } from '@/types';
import { resolveUsageServiceLoginMode } from './loginMode';
import styles from './LoginPage.module.scss';

type RedirectState = { from?: { pathname?: string } };
type UsageSetupStep = 'admin' | 'connection' | 'cpaKey' | 'monitoring' | 'polling' | 'review';
const CONFIG_TAB_STORAGE_KEY = 'config-management:tab';

function getLocalizedErrorMessage(
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  const usageServiceCode = getUsageServiceErrorCode(error);
  if (usageServiceCode) {
    return t(`usage_service_errors.${usageServiceCode}`, {
      defaultValue: t('usage_service_errors.request_failed'),
    });
  }

  const apiError = error as Partial<ApiError>;
  const status = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';

  const withHttpStatus = (summary: string) => {
    if (!status) return summary;

    const genericAxiosMessage = `Request failed with status code ${status}`;
    const detail = message.trim();
    const backendDetail =
      detail && detail !== genericAxiosMessage
        ? ` (${t('login.error_backend_detail')}: ${detail})`
        : '';

    return `HTTP ${status}: ${summary}${backendDetail}`;
  };

  if (status === 401) return withHttpStatus(t('login.error_unauthorized'));
  if (status === 403) return withHttpStatus(t('login.error_forbidden'));
  if (status === 404) return withHttpStatus(t('login.error_not_found'));
  if (status && status >= 500) return withHttpStatus(t('login.error_server'));
  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  return withHttpStatus(t('login.error_invalid'));
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const theme = useThemeStore((state) => state.theme);
  const cycleTheme = useThemeStore((state) => state.cycleTheme);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const login = useAuthStore((state) => state.login);
  const restoreSession = useAuthStore((state) => state.restoreSession);
  const storedBase = useAuthStore((state) => state.apiBase);
  const storedKey = useAuthStore((state) => state.managementKey);
  const storedRememberPassword = useAuthStore((state) => state.rememberPassword);
  const setUsageServiceConfig = useUsageServiceStore((state) => state.setUsageServiceConfig);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);

  const [apiBase, setApiBase] = useState('');
  const [adminKey, setAdminKey] = useState('');
  const [cpaManagementKey, setCPAManagementKey] = useState('');
  const [showCustomBase, setShowCustomBase] = useState(false);
  const [showAdminKey, setShowAdminKey] = useState(false);
  const [showCPAManagementKey, setShowCPAManagementKey] = useState(false);
  const [rememberCredential, setRememberCredential] = useState(false);
  const [requestMonitoringEnabled, setRequestMonitoringEnabled] = useState(true);
  const [pollIntervalMs, setPollIntervalMs] = useState('500');
  const [loading, setLoading] = useState(false);
  const [autoLoading, setAutoLoading] = useState(true);
  const [autoLoginSuccess, setAutoLoginSuccess] = useState(false);
  const [error, setError] = useState('');
  const [hostedByUsageService, setHostedByUsageService] = useState(false);
  const [usageServiceNeedsSetup, setUsageServiceNeedsSetup] = useState(false);
  const [languageMenuOpen, setLanguageMenuOpen] = useState(false);
  const [hasHistoricalData, setHasHistoricalData] = useState(false);
  const [migrationStatus, setMigrationStatus] = useState('');
  const [usageSetupStep, setUsageSetupStep] = useState<UsageSetupStep>('admin');

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  const isManagerServerMode = hostedByUsageService;
  const loginCredential = isManagerServerMode ? adminKey : cpaManagementKey;
  const loginCredentialLabel = isManagerServerMode
    ? t('login.admin_key_label')
    : t('login.cpa_management_key_label');
  const loginCredentialPlaceholder = isManagerServerMode
    ? t('login.admin_key_placeholder')
    : t('login.cpa_management_key_placeholder');
  const loginCredentialHint = isManagerServerMode
    ? t('login.admin_key_hint')
    : t('login.cpa_management_key_hint');

  const usageSetupSteps = useMemo<UsageSetupStep[]>(
    () => [
      'admin',
      'connection',
      'cpaKey',
      'monitoring',
      ...(requestMonitoringEnabled ? (['polling'] as UsageSetupStep[]) : []),
      'review',
    ],
    [requestMonitoringEnabled]
  );
  const usageSetupStepIndex = Math.max(0, usageSetupSteps.indexOf(usageSetupStep));
  const usageSetupIsFirstStep = usageSetupStepIndex <= 0;
  const usageSetupIsLastStep = usageSetupStep === 'review';
  const usageSetupStepLabels = useMemo<Record<UsageSetupStep, string>>(
    () => ({
      admin: t('login.step_admin_key'),
      connection: t('login.step_connection'),
      cpaKey: t('login.step_cpa_key'),
      monitoring: t('login.step_monitoring'),
      polling: t('login.step_polling'),
      review: t('login.step_review'),
    }),
    [t]
  );
  const toggleLanguageMenu = useCallback(() => {
    setLanguageMenuOpen((prev) => !prev);
  }, []);

  const handleLanguageSelect = useCallback(
    (selectedLanguage: string) => {
      if (!isSupportedLanguage(selectedLanguage)) {
        return;
      }

      setLanguage(selectedLanguage);
      setLanguageMenuOpen(false);
    },
    [setLanguage]
  );

  useEffect(() => {
    if (!languageMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!languageMenuRef.current?.contains(event.target as Node)) {
        setLanguageMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setLanguageMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [languageMenuOpen]);

  useEffect(() => {
    const init = async () => {
      try {
        let detectedUsageService = false;
        let detectedUsageServiceConfigured = false;
        try {
          const info = await usageServiceApi.getInfo(detectedBase);
          const mode = resolveUsageServiceLoginMode(info);
          detectedUsageService = mode.hostedByUsageService;
          detectedUsageServiceConfigured = detectedUsageService && !mode.usageServiceNeedsSetup;
          setHostedByUsageService(mode.hostedByUsageService);
          setUsageServiceNeedsSetup(mode.usageServiceNeedsSetup);
          setHasHistoricalData(Boolean(info.hasHistoricalData));
          setMigrationStatus(info.migrationStatus || '');
        } catch {
          detectedUsageService = false;
          detectedUsageServiceConfigured = false;
          setHostedByUsageService(false);
          setUsageServiceNeedsSetup(false);
          setHasHistoricalData(false);
          setMigrationStatus('');
        }

        const hostedManagementPage =
          typeof window !== 'undefined' && /\/management\.html$/i.test(window.location.pathname);
        const autoLoginExpectedPanelBase =
          detectedUsageService || hostedManagementPage ? detectedBase : undefined;
        const autoLoggedIn = await restoreSession({
          expectedMode: detectedUsageService ? 'manager_embedded' : 'external_panel',
          expectedPanelBase: autoLoginExpectedPanelBase,
        });
        if (detectedUsageService) {
          setUsageServiceConfig(
            { enabled: true, serviceBase: detectedBase },
            { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
          );
        }
        if (autoLoggedIn) {
          setAutoLoginSuccess(true);
          setTimeout(() => {
            const redirect =
              autoLoggedIn.recoveryMode === 'manager_config'
                ? '/config'
                : (location.state as RedirectState | null)?.from?.pathname || '/';
            if (autoLoggedIn.recoveryMode === 'manager_config') {
              localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'manager');
            }
            navigate(redirect, { replace: true });
          }, 1500);
          return;
        }

        const lastCPAForUsageService =
          localStorage.getItem(USAGE_SERVICE_LAST_CPA_BASE_KEY) ||
          localStorage.getItem(LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY) ||
          '';
        const defaultCPAConnectionBase = resolveDefaultCPAConnectionBase({
          hostedByUsageService: detectedUsageService,
          currentBase: detectedBase,
        });
        setApiBase(
          detectedUsageService
            ? detectedUsageServiceConfigured
              ? detectedBase
              : lastCPAForUsageService || defaultCPAConnectionBase
            : storedBase || detectedBase
        );
        setShowCustomBase(detectedUsageService && !detectedUsageServiceConfigured);
        if (detectedUsageService) {
          setAdminKey(storedKey || '');
          setCPAManagementKey('');
        } else {
          setAdminKey('');
          setCPAManagementKey(storedKey || '');
        }
        setRememberCredential(storedRememberPassword || Boolean(storedKey));
      } finally {
        if (!autoLoginSuccess) {
          setAutoLoading(false);
        }
      }
    };

    init();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!usageSetupSteps.includes(usageSetupStep)) {
      setUsageSetupStep('review');
    }
  }, [usageSetupStep, usageSetupSteps]);

  const validateUsageSetupStep = useCallback(
    (step: UsageSetupStep) => {
      if (step === 'admin' && !adminKey.trim()) {
        setError(t('login.admin_key_required'));
        return false;
      }
      if (step === 'connection' && !apiBase.trim()) {
        setError(t('login.cpa_address_required'));
        return false;
      }
      if (step === 'cpaKey' && !cpaManagementKey.trim()) {
        setError(t('login.cpa_management_key_required'));
        return false;
      }
      if (step === 'polling') {
        const parsedPollIntervalMs = Number(pollIntervalMs);
        if (
          !/^\d+$/.test(pollIntervalMs.trim()) ||
          !Number.isFinite(parsedPollIntervalMs) ||
          parsedPollIntervalMs <= 0
        ) {
          setError(t('login.poll_interval_invalid'));
          return false;
        }
      }
      setError('');
      return true;
    },
    [adminKey, apiBase, cpaManagementKey, pollIntervalMs, t]
  );

  const handleUsageSetupNext = useCallback(() => {
    if (!validateUsageSetupStep(usageSetupStep)) return;
    const currentIndex = usageSetupSteps.indexOf(usageSetupStep);
    const nextStep = usageSetupSteps[Math.min(currentIndex + 1, usageSetupSteps.length - 1)];
    setUsageSetupStep(nextStep);
  }, [usageSetupStep, usageSetupSteps, validateUsageSetupStep]);

  const handleUsageSetupBack = useCallback(() => {
    setError('');
    const currentIndex = usageSetupSteps.indexOf(usageSetupStep);
    const previousStep = usageSetupSteps[Math.max(currentIndex - 1, 0)];
    setUsageSetupStep(previousStep);
  }, [usageSetupStep, usageSetupSteps]);

  const handleSubmit = useCallback(async () => {
    if (usageServiceNeedsSetup && !usageSetupIsLastStep) {
      handleUsageSetupNext();
      return;
    }

    const trimmedAdminKey = adminKey.trim();
    const trimmedCPAKey = cpaManagementKey.trim();
    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;

    if (usageServiceNeedsSetup) {
      if (!trimmedAdminKey) {
        setError(t('login.admin_key_required'));
        return;
      }
      if (!apiBase.trim()) {
        setError(t('login.cpa_address_required'));
        return;
      }
      if (!trimmedCPAKey) {
        setError(t('login.cpa_management_key_required'));
        return;
      }
    } else if (isManagerServerMode) {
      if (!trimmedAdminKey) {
        setError(t('login.admin_key_required'));
        return;
      }
    } else if (!trimmedCPAKey) {
      setError(t('login.cpa_management_key_required'));
      return;
    }

    const parsedPollIntervalMs = Number(pollIntervalMs);
    if (
      usageServiceNeedsSetup &&
      requestMonitoringEnabled &&
      (!/^\d+$/.test(pollIntervalMs.trim()) ||
        !Number.isFinite(parsedPollIntervalMs) ||
        parsedPollIntervalMs <= 0)
    ) {
      setError(t('login.poll_interval_invalid'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      if (usageServiceNeedsSetup) {
        await usageServiceApi.setup(
          detectedBase,
          {
            cpaBaseUrl: baseToUse,
            cpaManagementKey: trimmedCPAKey,
            pollIntervalMs: requestMonitoringEnabled ? parsedPollIntervalMs : undefined,
            ensureUsageStatisticsEnabled: requestMonitoringEnabled,
            requestMonitoringEnabled,
          },
          trimmedAdminKey
        );
        setUsageServiceConfig(
          { enabled: true, serviceBase: detectedBase },
          { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
        );
        localStorage.setItem(USAGE_SERVICE_LAST_CPA_BASE_KEY, baseToUse);
      } else if (isManagerServerMode) {
        setUsageServiceConfig(
          { enabled: true, serviceBase: detectedBase },
          { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
        );
      }

      const loginResult = await login({
        apiBase: isManagerServerMode ? detectedBase : baseToUse,
        managementKey: isManagerServerMode ? trimmedAdminKey : trimmedCPAKey,
        rememberPassword: rememberCredential,
        sessionMode: isManagerServerMode ? 'manager_embedded' : 'external_panel',
        sessionPanelBase: detectedBase,
      });
      showNotification(t('common.connected_status'), 'success');
      if (loginResult.recoveryMode === 'manager_config') {
        localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'manager');
        navigate('/config', { replace: true });
      } else {
        navigate('/', { replace: true });
      }
    } catch (err: unknown) {
      const message = getLocalizedErrorMessage(err, t);
      setError(message);
      showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    adminKey,
    apiBase,
    cpaManagementKey,
    detectedBase,
    handleUsageSetupNext,
    isManagerServerMode,
    login,
    navigate,
    pollIntervalMs,
    rememberCredential,
    requestMonitoringEnabled,
    setUsageServiceConfig,
    showNotification,
    t,
    usageServiceNeedsSetup,
    usageSetupIsLastStep,
  ]);

  const handleSubmitKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Enter' && !loading) {
        event.preventDefault();
        handleSubmit();
      }
    },
    [handleSubmit, loading]
  );

  if (isAuthenticated && !autoLoading && !autoLoginSuccess) {
    const redirect = (location.state as RedirectState | null)?.from?.pathname || '/';
    return <Navigate to={redirect} replace />;
  }

  const showSplash = autoLoading || autoLoginSuccess;

  const renderKeyToggle = (visible: boolean, toggle: () => void) => (
    <button
      type="button"
      className="btn btn-ghost btn-xs btn-icon-only"
      onClick={toggle}
      aria-label={visible ? t('login.hide_key') : t('login.show_key')}
      title={visible ? t('login.hide_key') : t('login.show_key')}
    >
      {visible ? <IconEyeOff size={16} /> : <IconEye size={16} />}
    </button>
  );

  return (
    <div className={styles.container}>
      <div className={styles.toolBar}>
        <button
          type="button"
          className={styles.toolButton}
          onClick={cycleTheme}
          aria-label={t('theme.switch')}
          title={t('theme.switch')}
        >
          {theme === 'dark' ? <IconMoon size={17} /> : <IconSun size={17} />}
        </button>
        <div className={styles.languageMenu} ref={languageMenuRef}>
          <button
            type="button"
            className={styles.toolButton}
            onClick={toggleLanguageMenu}
            aria-label={t('language.switch')}
            title={t('language.switch')}
            aria-haspopup="menu"
            aria-expanded={languageMenuOpen}
          >
            <IconLanguages size={17} />
          </button>
          {languageMenuOpen && (
            <div
              className={styles.languagePopover}
              role="menu"
              aria-label={t('language.switch')}
            >
              {LANGUAGE_ORDER.map((lang) => (
                <button
                  key={lang}
                  type="button"
                  className={`${styles.languageOption} ${
                    language === lang ? styles.languageOptionActive : ''
                  }`}
                  onClick={() => handleLanguageSelect(lang)}
                  role="menuitemradio"
                  aria-checked={language === lang}
                >
                  {t(LANGUAGE_LABEL_KEYS[lang])}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      <div className={styles.formPanel}>
        {showSplash ? (
          <div className={styles.splashContent}>
            <img src={INLINE_LOGO_JPEG} alt="CPAMP" className={styles.splashLogo} />
            <h1 className={styles.splashTitle}>{t('splash.title')}</h1>
            <p className={styles.splashSubtitle}>{t('splash.subtitle')}</p>
            <div className={styles.splashLoader}>
              <div className={styles.splashLoaderBar} />
            </div>
          </div>
        ) : (
          <div
            className={`${styles.formContent} ${
              usageServiceNeedsSetup ? styles.setupFormContent : ''
            }`}
          >
            <div className={`${styles.loginCard} ${usageServiceNeedsSetup ? styles.setupCard : ''}`}>
              <div className={styles.cardBranding}>
                <img src={INLINE_LOGO_JPEG} alt="CPA Manager Plus" className={styles.logo} />
                <h1>CPA Manager Plus</h1>
                <p>
                  {usageServiceNeedsSetup
                    ? t('login.docker_setup_subtitle')
                    : isManagerServerMode
                      ? t('login.docker_login_subtitle')
                      : t('login.subtitle')}
                </p>
              </div>

              {usageServiceNeedsSetup && (
                <div className={styles.setupFlow}>
                  <div className={styles.stepper} aria-label={t('login.setup_steps')}>
                    {usageSetupSteps.map((step, index) => {
                      const isActive = index === usageSetupStepIndex;
                      const isDone = index < usageSetupStepIndex;
                      return (
                        <div
                          key={step}
                          className={`${styles.stepItem} ${isActive ? styles.stepItemActive : ''} ${
                            isDone ? styles.stepItemDone : ''
                          }`}
                          aria-current={isActive ? 'step' : undefined}
                        >
                          <span className={styles.stepIndex}>
                            {isDone ? <IconCheck size={18} /> : index + 1}
                          </span>
                          <span className={styles.stepLabel}>{usageSetupStepLabels[step]}</span>
                        </div>
                      );
                    })}
                  </div>

                  <div className={styles.stepPanel}>
                    <div className={styles.stepHeader}>
                      <span className={styles.stepEyebrow}>
                        {t('login.step_count', {
                          current: usageSetupStepIndex + 1,
                          total: usageSetupSteps.length,
                        })}
                      </span>
                      <h2>{usageSetupStepLabels[usageSetupStep]}</h2>
                    </div>

                    {usageSetupStep === 'admin' && (
                      <div className={styles.stepFields}>
                        <div className={styles.connectionBox}>
                          <div className={styles.connectionIcon}>
                            <IconShield size={18} />
                          </div>
                          <div className={styles.connectionCopy}>
                            <div className={styles.label}>{t('login.usage_service_address')}</div>
                            <div className={styles.value}>{detectedBase}</div>
                            <div className={styles.hint}>
                              {hasHistoricalData || migrationStatus
                                ? t('login.migration_detected_hint')
                                : t('login.admin_key_setup_hint')}
                            </div>
                          </div>
                        </div>
                        <Input
                          autoFocus
                          label={t('login.admin_key_label')}
                          placeholder={t('login.admin_key_placeholder')}
                          type={showAdminKey ? 'text' : 'password'}
                          value={adminKey}
                          onChange={(event) => setAdminKey(event.target.value)}
                          onKeyDown={handleSubmitKeyDown}
                          hint={t('login.admin_key_hint')}
                          rightElement={renderKeyToggle(showAdminKey, () =>
                            setShowAdminKey((prev) => !prev)
                          )}
                        />
                      </div>
                    )}

                    {usageSetupStep === 'connection' && (
                      <div className={styles.stepFields}>
                        <Input
                          autoFocus
                          label={t('login.cpa_connection_label')}
                          placeholder={t('login.cpa_connection_placeholder')}
                          value={apiBase}
                          onChange={(event) => setApiBase(event.target.value)}
                          onKeyDown={handleSubmitKeyDown}
                          hint={t('login.cpa_connection_hint')}
                        />
                      </div>
                    )}

                    {usageSetupStep === 'cpaKey' && (
                      <div className={styles.stepFields}>
                        <Input
                          autoFocus
                          label={t('login.cpa_management_key_label')}
                          placeholder={t('login.cpa_management_key_placeholder')}
                          type={showCPAManagementKey ? 'text' : 'password'}
                          value={cpaManagementKey}
                          onChange={(event) => setCPAManagementKey(event.target.value)}
                          onKeyDown={handleSubmitKeyDown}
                          hint={t('login.cpa_management_key_hint')}
                          rightElement={renderKeyToggle(showCPAManagementKey, () =>
                            setShowCPAManagementKey((prev) => !prev)
                          )}
                        />
                      </div>
                    )}

                    {usageSetupStep === 'monitoring' && (
                      <div className={styles.stepFields}>
                        <div className={styles.optionBox}>
                          <SelectionCheckbox
                            checked={requestMonitoringEnabled}
                            onChange={setRequestMonitoringEnabled}
                            ariaLabel={t('login.request_monitoring_enabled')}
                            label={t('login.request_monitoring_enabled')}
                            labelClassName={styles.toggleLabel}
                          />
                          <p>
                            {requestMonitoringEnabled
                              ? t('login.request_monitoring_enabled_hint')
                              : t('login.request_monitoring_disabled_hint')}
                          </p>
                        </div>
                      </div>
                    )}

                    {usageSetupStep === 'polling' && (
                      <div className={styles.stepFields}>
                        <Input
                          autoFocus
                          label={t('login.poll_interval_label')}
                          type="number"
                          min="1"
                          placeholder="500"
                          value={pollIntervalMs}
                          onChange={(event) => setPollIntervalMs(event.target.value)}
                          onKeyDown={handleSubmitKeyDown}
                          hint={t('login.poll_interval_hint')}
                        />
                      </div>
                    )}

                    {usageSetupStep === 'review' && (
                      <div className={styles.stepFields}>
                        <div className={styles.optionBox}>
                          <SelectionCheckbox
                            checked={rememberCredential}
                            onChange={setRememberCredential}
                            ariaLabel={t('login.remember_credential_label')}
                            label={t('login.remember_credential_label')}
                            labelClassName={styles.toggleLabel}
                          />
                        </div>
                        <div className={styles.reviewGrid}>
                          <div>
                            <span className={styles.reviewIcon}>
                              <IconShield size={18} />
                            </span>
                            <span>{t('login.admin_key_label')}</span>
                            <strong>{adminKey ? '************' : '-'}</strong>
                          </div>
                          <div>
                            <span className={styles.reviewIcon}>
                              <IconKey size={18} />
                            </span>
                            <span>{t('login.remember_credential_label')}</span>
                            <strong>
                              {rememberCredential ? t('common.enabled') : t('common.disabled')}
                            </strong>
                          </div>
                          <div>
                            <span className={styles.reviewIcon}>
                              <IconInfo size={18} />
                            </span>
                            <span>{t('login.cpa_connection_label')}</span>
                            <strong>{apiBase || '-'}</strong>
                          </div>
                          <div>
                            <span className={styles.reviewIcon}>
                              <IconKey size={18} />
                            </span>
                            <span>{t('login.cpa_management_key_label')}</span>
                            <strong>{cpaManagementKey ? '************' : '-'}</strong>
                          </div>
                          <div>
                            <span className={styles.reviewIcon}>
                              <IconEye size={18} />
                            </span>
                            <span>{t('login.request_monitoring_enabled')}</span>
                            <strong>
                              {requestMonitoringEnabled
                                ? t('common.enabled')
                                : t('common.disabled')}
                            </strong>
                          </div>
                          {requestMonitoringEnabled && (
                            <div>
                              <span className={styles.reviewIcon}>
                                <IconTimer size={18} />
                              </span>
                              <span>{t('login.poll_interval_label')}</span>
                              <strong>{pollIntervalMs}</strong>
                            </div>
                          )}
                        </div>
                      </div>
                    )}
                  </div>

                  {error && <div className={styles.errorBox}>{error}</div>}

                  <div className={styles.stepActions}>
                    <Button
                      variant="secondary"
                      className={styles.setupBackButton}
                      onClick={handleUsageSetupBack}
                      disabled={usageSetupIsFirstStep || loading}
                    >
                      {t('common.previous')}
                    </Button>
                    {usageSetupIsLastStep ? (
                      <Button
                        className={styles.setupNextButton}
                        onClick={handleSubmit}
                        loading={loading}
                      >
                        {loading ? t('login.initializing') : t('login.initialize_button')}
                      </Button>
                    ) : (
                      <Button
                        className={styles.setupNextButton}
                        onClick={handleUsageSetupNext}
                        disabled={loading}
                      >
                        {t('common.next')}
                      </Button>
                    )}
                  </div>
                </div>
              )}

              {!usageServiceNeedsSetup && (
                <div className={styles.loginForm}>
                  <div className={styles.connectionBox}>
                    <div className={styles.label}>{t('login.connection_current')}</div>
                    <div className={styles.value}>{apiBase || detectedBase}</div>
                    <div className={styles.hint}>
                      {isManagerServerMode
                        ? t('login.usage_service_configured_hint')
                        : t('login.connection_auto_hint')}
                    </div>
                  </div>

                  {!isManagerServerMode && (
                    <>
                      <div className={styles.toggleAdvanced}>
                        <SelectionCheckbox
                          checked={showCustomBase}
                          onChange={setShowCustomBase}
                          ariaLabel={t('login.custom_connection_label')}
                          label={t('login.custom_connection_label')}
                          labelClassName={styles.toggleLabel}
                        />
                      </div>

                      {showCustomBase && (
                        <Input
                          label={t('login.custom_connection_label')}
                          placeholder={t('login.custom_connection_placeholder')}
                          value={apiBase}
                          onChange={(event) => setApiBase(event.target.value)}
                          hint={t('login.custom_connection_hint')}
                        />
                      )}
                    </>
                  )}

                  <Input
                    autoFocus
                    label={loginCredentialLabel}
                    placeholder={loginCredentialPlaceholder}
                    type={
                      (isManagerServerMode ? showAdminKey : showCPAManagementKey) ? 'text' : 'password'
                    }
                    value={loginCredential}
                    onChange={(event) =>
                      isManagerServerMode
                        ? setAdminKey(event.target.value)
                        : setCPAManagementKey(event.target.value)
                    }
                    onKeyDown={handleSubmitKeyDown}
                    hint={loginCredentialHint}
                    rightElement={renderKeyToggle(
                      isManagerServerMode ? showAdminKey : showCPAManagementKey,
                      () =>
                        isManagerServerMode
                          ? setShowAdminKey((prev) => !prev)
                          : setShowCPAManagementKey((prev) => !prev)
                    )}
                  />

                  <div className={styles.toggleAdvanced}>
                    <SelectionCheckbox
                      checked={rememberCredential}
                      onChange={setRememberCredential}
                      ariaLabel={t('login.remember_credential_label')}
                      label={t('login.remember_credential_label')}
                      labelClassName={styles.toggleLabel}
                    />
                  </div>

                  <Button fullWidth onClick={handleSubmit} loading={loading}>
                    {loading ? t('login.submitting') : t('login.submit_button')}
                  </Button>

                  {error && <div className={styles.errorBox}>{error}</div>}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
