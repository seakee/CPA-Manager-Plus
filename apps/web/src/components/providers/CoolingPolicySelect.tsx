import { Select, type SelectOption } from '@/components/ui/Select';
import { InfoTooltip } from '@/components/ui/InfoTooltip';
import { useAuthStore } from '@/stores/useAuthStore';
import {
  COOLING_OVERRIDE_REFACTOR_COMMIT,
  getCoolingOverrideCompatibility,
  type CoolingPolicy,
} from '@/types';
import { useTranslation } from 'react-i18next';

interface CoolingPolicySelectProps {
  value: CoolingPolicy;
  onChange: (value: CoolingPolicy) => void;
  translationPrefix?: 'ai_providers' | 'auth_files';
  disabled?: boolean;
  id?: string;
  compact?: boolean;
  legacyProviderSupported?: boolean;
  tooltipHint?: boolean;
}

export function CoolingPolicySelect({
  value,
  onChange,
  translationPrefix = 'ai_providers',
  disabled = false,
  id,
  compact = false,
  legacyProviderSupported = true,
  tooltipHint = false,
}: CoolingPolicySelectProps) {
  const { t } = useTranslation();
  const serverVersion = useAuthStore((state) => state.serverVersion);
  const serverCommit = useAuthStore((state) => state.serverCommit);
  const compatibility = getCoolingOverrideCompatibility(serverVersion, serverCommit);
  const supportsThreeState = compatibility === 'supported';
  const isLegacy = compatibility === 'legacy';
  const providerBlocked = !supportsThreeState && !legacyProviderSupported;
  const key = (suffix: string) => `${translationPrefix}.cooling_policy_${suffix}`;
  const allOptions: SelectOption[] = [
    { value: 'inherit', label: t(key('inherit')) },
    { value: 'enabled', label: t(key('enabled')) },
    { value: 'disabled', label: t(key('disabled')) },
  ];
  const options = supportsThreeState
    ? allOptions
    : allOptions.filter((option) => option.value !== 'enabled');
  const displayedValue = !supportsThreeState && value === 'enabled' ? 'inherit' : value;
  const compatibilityHint = providerBlocked
    ? t(key(isLegacy ? 'legacy_provider_unsupported' : 'unverified_provider_unsupported'), {
        commit: COOLING_OVERRIDE_REFACTOR_COMMIT,
      })
    : isLegacy
      ? t(key('legacy_hint'), { commit: COOLING_OVERRIDE_REFACTOR_COMMIT })
      : compatibility === 'unverified'
        ? t(key('unverified_hint'), { commit: COOLING_OVERRIDE_REFACTOR_COMMIT })
        : null;
  const hintId = id ? `${id}-hint` : undefined;
  const compatibilityHintId = id ? `${id}-compatibility` : undefined;
  const describedBy = id
    ? [compact || tooltipHint ? undefined : hintId, compatibilityHint ? compatibilityHintId : undefined]
        .filter(Boolean)
        .join(' ') || undefined
    : undefined;
  const select = (
    <Select
      id={id}
      value={displayedValue}
      options={options}
      onChange={(nextValue) => onChange(nextValue as CoolingPolicy)}
      disabled={disabled || providerBlocked}
      ariaLabel={t(key('label'))}
      ariaDescribedBy={describedBy}
    />
  );

  if (compact) {
    return (
      <div>
        {select}
        {compatibilityHint && (
          <div className="hint" id={compatibilityHintId} role="note">
            {compatibilityHint}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="form-group">
      <label htmlFor={id}>
        <span>{t(key('label'))}</span>
        {tooltipHint ? <InfoTooltip content={t(key('hint'))} ariaLabel={t(key('label'))} /> : null}
      </label>
      {select}
      {!tooltipHint && (
        <div className="hint" id={hintId}>
          {t(key('hint'))}
        </div>
      )}
      {compatibilityHint && (
        <div className="hint" id={compatibilityHintId} role="note">
          {compatibilityHint}
        </div>
      )}
    </div>
  );
}
