import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useOutletContext } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import type { ModelInfo } from '@/utils/models';
import { buildOpenAIModelsEndpoint } from '@/components/providers/utils';
import type { OpenAIEditOutletContext } from './AiProvidersOpenAIEditLayout';
import { discoverOpenAIModels } from './openAIModelDiscovery';
import styles from './AiProvidersPage.module.scss';
import layoutStyles from './AiProvidersEditLayout.module.scss';

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

export function AiProvidersOpenAIModelsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const {
    disableControls,
    loading: initialLoading,
    saving,
    form,
    mergeDiscoveredModels,
  } = useOutletContext<OpenAIEditOutletContext>();

  const [endpoint, setEndpoint] = useState('');
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [fetching, setFetching] = useState(false);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const filteredModels = useMemo(() => {
    const filter = search.trim().toLowerCase();
    if (!filter) return models;
    return models.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const alias = (model.alias || '').toLowerCase();
      const desc = (model.description || '').toLowerCase();
      return name.includes(filter) || alias.includes(filter) || desc.includes(filter);
    });
  }, [models, search]);

  const configuredModelNames = useMemo(
    () =>
      new Set(
        form.modelEntries
          .map((entry) => entry.name.trim().toLowerCase())
          .filter(Boolean)
      ),
    [form.modelEntries]
  );

  const visibleModelNames = useMemo(
    () =>
      filteredModels
        .map((model) => model.name)
        .filter((name) => !configuredModelNames.has(name.trim().toLowerCase())),
    [configuredModelNames, filteredModels]
  );
  const allVisibleSelected = useMemo(
    () => visibleModelNames.length > 0 && visibleModelNames.every((name) => selected.has(name)),
    [selected, visibleModelNames]
  );

  const fetchOpenaiModelDiscovery = useCallback(
    async () => {
      const trimmedBaseUrl = form.baseUrl.trim();
      if (!trimmedBaseUrl) return;

      setFetching(true);
      setError('');
      try {
        const list = await discoverOpenAIModels({
          baseUrl: trimmedBaseUrl,
          headers: form.headers,
          apiKeyEntries: form.apiKeyEntries,
          proxyRequiresSavedEntryMessage: t(
            'ai_providers.openai_models_proxy_requires_saved_entry'
          ),
        });
        setModels(list);
      } catch (err: unknown) {
        setModels([]);
        setError(`${t('ai_providers.openai_models_fetch_error')}: ${getErrorMessage(err)}`);
      } finally {
        setFetching(false);
      }
    },
    [form.apiKeyEntries, form.baseUrl, form.headers, t]
  );

  useEffect(() => {
    if (initialLoading) return;
    setEndpoint(buildOpenAIModelsEndpoint(form.baseUrl));
    setModels([]);
    setSearch('');
    setSelected(new Set());
    setError('');
    void fetchOpenaiModelDiscovery();
  }, [fetchOpenaiModelDiscovery, form.baseUrl, initialLoading]);

  useEffect(() => {
    const availableNames = new Set(models.map((model) => model.name));
    setSelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((name) => {
        if (availableNames.has(name) && !configuredModelNames.has(name.toLowerCase())) {
          next.add(name);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [configuredModelNames, models]);

  const handleBack = useCallback(() => {
    navigate(-1);
  }, [navigate]);

  const swipeRef = useEdgeSwipeBack({ onBack: handleBack });

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        handleBack();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleBack]);

  const toggleSelection = (name: string) => {
    if (configuredModelNames.has(name.toLowerCase())) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });
  };

  const handleSelectVisible = useCallback(() => {
    setSelected((prev) => {
      const next = new Set(prev);
      visibleModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleModelNames]);

  const handleClearSelection = useCallback(() => {
    setSelected(new Set());
  }, []);

  const handleApply = () => {
    const selectedModels = models.filter((model) => selected.has(model.name));
    if (selectedModels.length) {
      mergeDiscoveredModels(selectedModels);
    }
    handleBack();
  };

  const canApply = !disableControls && !saving && !fetching && selected.size > 0;

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={t('ai_providers.openai_models_fetch_title')}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      hideTopBarBackButton
      hideTopBarRightAction
      floatingAction={
        <div className={layoutStyles.floatingActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleBack}
            className={layoutStyles.floatingBackButton}
          >
            {t('common.back')}
          </Button>
          <Button
            size="sm"
            onClick={handleApply}
            disabled={!canApply}
            className={layoutStyles.floatingSaveButton}
          >
            {t('ai_providers.openai_models_fetch_apply')}
          </Button>
        </div>
      }
      isLoading={initialLoading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        <div className={styles.openaiModelsContent}>
          <div className={styles.sectionHint}>{t('ai_providers.openai_models_fetch_hint')}</div>
          <div className={styles.openaiModelsEndpointSection}>
            <label className={styles.openaiModelsEndpointLabel}>
              {t('ai_providers.openai_models_fetch_url_label')}
            </label>
            <div className={styles.openaiModelsEndpointControls}>
              <input
                className={`input ${styles.openaiModelsEndpointInput}`}
                readOnly
                value={endpoint}
              />
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void fetchOpenaiModelDiscovery()}
                loading={fetching}
                disabled={disableControls || saving}
              >
                {t('ai_providers.openai_models_fetch_refresh')}
              </Button>
            </div>
          </div>
          <Input
            label={t('ai_providers.openai_models_search_label')}
            placeholder={t('ai_providers.openai_models_search_placeholder')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            disabled={fetching}
          />
          {models.length > 0 && (
            <div className={styles.modelDiscoveryToolbar}>
              <div className={styles.modelDiscoveryToolbarActions}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={handleSelectVisible}
                  disabled={
                    disableControls ||
                    saving ||
                    fetching ||
                    filteredModels.length === 0 ||
                    allVisibleSelected
                  }
                >
                  {t('ai_providers.model_discovery_select_visible')}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={handleClearSelection}
                  disabled={disableControls || saving || fetching || selected.size === 0}
                >
                  {t('ai_providers.model_discovery_clear_selection')}
                </Button>
              </div>
              <div className={styles.modelDiscoverySelectionSummary}>
                {t('ai_providers.model_discovery_selected_count', { count: selected.size })}
              </div>
            </div>
          )}
          {error && <div className="error-box">{error}</div>}
          {fetching ? (
            <div className={styles.sectionHint}>
              {t('ai_providers.openai_models_fetch_loading')}
            </div>
          ) : models.length === 0 ? (
            <div className={styles.sectionHint}>{t('ai_providers.openai_models_fetch_empty')}</div>
          ) : filteredModels.length === 0 ? (
            <div className={styles.sectionHint}>{t('ai_providers.openai_models_search_empty')}</div>
          ) : (
            <div className={styles.modelDiscoveryList}>
              {filteredModels.map((model) => {
                const checked = selected.has(model.name);
                const alreadyConfigured = configuredModelNames.has(model.name.trim().toLowerCase());
                return (
                  <SelectionCheckbox
                    key={model.name}
                    checked={checked}
                    onChange={() => toggleSelection(model.name)}
                    disabled={disableControls || saving || fetching || alreadyConfigured}
                    ariaLabel={model.name}
                    className={`${styles.modelDiscoveryRow} ${
                      checked ? styles.modelDiscoveryRowSelected : ''
                    }`}
                    labelClassName={styles.modelDiscoverySelectionLabel}
                    label={
                      <div className={styles.modelDiscoveryMeta}>
                        <div className={styles.modelDiscoveryName}>
                          <div className={styles.modelDiscoveryNameText}>
                            {model.name}
                            {model.alias && (
                              <span className={styles.modelDiscoveryAlias}>{model.alias}</span>
                            )}
                          </div>
                          {alreadyConfigured && (
                            <span className={styles.modelDiscoveryAddedBadge}>
                              {t('ai_providers.model_discovery_already_added')}
                            </span>
                          )}
                        </div>
                        {model.description && (
                          <div className={styles.modelDiscoveryDesc}>{model.description}</div>
                        )}
                      </div>
                    }
                  />
                );
              })}
            </div>
          )}
        </div>
      </Card>
    </SecondaryScreenShell>
  );
}
