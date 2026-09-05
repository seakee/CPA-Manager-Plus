import { useTranslation } from 'react-i18next';
import type { AccountDetailField } from '@/features/accounts/model/accountDetailViewModel';
import {
  formatMoney,
  formatPercent,
  formatQuotaResetTimestamp,
  formatTimestamp,
  getProviderLabel,
  translateDetailEnum,
} from '@/features/accounts/model/accountsPagePresentation';
import { CopyableText } from '@/features/accounts/components/CopyableText';
import { formatCompactNumber } from '@/utils/usage';

const formatDetailNumber = (value: number): string => {
  if (!Number.isFinite(value)) return '-';
  return Math.abs(value) < 1_000 ? String(value) : formatCompactNumber(value);
};

export function AccountDetailFieldValue({ field }: { field: AccountDetailField }) {
  const { t, i18n } = useTranslation();
  if (field.value === null || field.value === '') return <>-</>;
  if (field.key === 'provider') return <>{getProviderLabel(String(field.value), t)}</>;
  if (field.key === 'actionStatus') {
    return <>{translateDetailEnum(t, 'accounts.action_status_', field.value)}</>;
  }
  if (field.key === 'errorKind') {
    return <>{translateDetailEnum(t, 'accounts.quota_error_kind_', field.value)}</>;
  }
  if (field.key === 'errorCode') {
    return <>{translateDetailEnum(t, 'accounts.quota_error_code_', field.value)}</>;
  }
  if (field.key === 'rateLimitReachedType') {
    return <>{translateDetailEnum(t, 'accounts.quota_rate_limit_type_', field.value)}</>;
  }
  if (field.valueKind === 'i18n') {
    return <>{t(String(field.value), { defaultValue: String(field.value) })}</>;
  }
  if (field.valueKind === 'percent') {
    return (
      <>
        {typeof field.value === 'number'
          ? formatPercent(field.value, field.key === 'successRate' ? 1 : 0)
          : String(field.value)}
      </>
    );
  }
  if (field.valueKind === 'money') {
    return <>{typeof field.value === 'number' ? formatMoney(field.value) : String(field.value)}</>;
  }
  if (field.valueKind === 'timestamp') {
    return (
      <>{typeof field.value === 'number' ? formatTimestamp(field.value, i18n.language) : '-'}</>
    );
  }
  if (field.valueKind === 'quota_reset') {
    return (
      <>
        {typeof field.value === 'number'
          ? formatQuotaResetTimestamp(field.value, i18n.language)
          : '-'}
      </>
    );
  }
  if (field.valueKind === 'number') {
    return (
      <>
        {typeof field.value === 'number' ? formatDetailNumber(field.value) : String(field.value)}
      </>
    );
  }
  if (field.key === 'trace' && typeof field.value === 'string' && field.value.length > 8) {
    return <CopyableText value={field.value} />;
  }
  return <>{String(field.value)}</>;
}

export function AccountDetailFieldList({ fields }: { fields: AccountDetailField[] }) {
  const { t } = useTranslation();
  if (fields.length === 0) return <p>{t('accounts.detail_no_data')}</p>;
  return (
    <dl>
      {fields.map((field) => (
        <div key={field.key}>
          <dt>{t(field.labelKey, { defaultValue: field.labelKey })}</dt>
          <dd>
            <AccountDetailFieldValue field={field} />
          </dd>
        </div>
      ))}
    </dl>
  );
}
