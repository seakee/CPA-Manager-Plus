import { describe, expect, it } from 'vitest';
import routesSource from '@/router/MainRoutes.tsx?raw';
import layoutSource from '@/components/layout/MainLayout.tsx?raw';
import accountsPageSource from '@/features/accounts/AccountsPage.tsx?raw';
import localStatusPanelSource from '@/features/monitoring/components/CodexInspectionStatusPanel.tsx?raw';
import serverInspectionPageSource from '@/features/monitoring/ServerCodexInspectionPage.tsx?raw';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';

const legacyPagesRoot = ['@', 'pages'].join('/');

describe('accounts workspace wiring', () => {
  it('uses Accounts as the single credential-management sidebar entry', () => {
    expect(layoutSource).toContain("path: '/accounts'");
    expect(layoutSource).toContain("label: t('nav.accounts'");
    expect(layoutSource).not.toContain("path: '/auth-files',\n        label: t('nav.auth_files')");
    expect(layoutSource).not.toContain("path: '/quota',\n        label: t('nav.quota_management')");
    expect(layoutSource).not.toContain("path: '/codex-inspection'");
    expect(layoutSource).not.toContain("label: t('nav.codex_inspection')");
  });

  it('removes legacy credential and quota routes while retaining inspection compatibility', () => {
    expect(routesSource).not.toContain("path: '/auth-files'");
    expect(routesSource).not.toContain("path: '/quota'");
    expect(routesSource).toContain("{ path: '/oauth', element: <OAuthPage /> }");
    expect(routesSource).toContain('<LegacyAccountsRedirect healthMode="local" />');
    expect(routesSource).toContain('<LegacyAccountsRedirect healthMode="server" />');
    expect(routesSource).not.toContain(`${legacyPagesRoot}/CodexInspectionPage`);
    expect(routesSource).not.toContain(`${legacyPagesRoot}/ServerCodexInspectionPage`);
  });

  it('embeds credential health as an Accounts tab and isolates header tabs from actions', () => {
    expect(accountsPageSource).toContain(
      "import { CredentialHealthInspectionWorkspace } from '@/features/monitoring/components/CredentialHealthInspectionWorkspace';"
    );
    expect(accountsPageSource).toContain("id: 'health'");
    expect(accountsPageSource).toContain('<CredentialHealthInspectionWorkspace');
    expect(accountsPageSource).not.toContain("id: 'inspection'");
    expect(localStatusPanelSource).toContain(
      '<div className={styles.statusPanelTabs}>{modeControl}</div>'
    );
    expect(serverInspectionPageSource).toContain(
      '<div className={styles.statusPanelTabs}>{modeControl}</div>'
    );
    expect(localStatusPanelSource).not.toContain(
      '<div className={styles.statusPanelActions}>\n            {modeControl}'
    );
    expect(serverInspectionPageSource).not.toContain(
      '<div className={styles.statusPanelActions}>\n            {modeControl}'
    );
  });

  it('separates credential list filters from the workspace navigation', () => {
    expect(accountsPageSource).toContain(
      '<section className={styles.controlsFilterPanel}>{renderToolbar()}</section>'
    );
    expect(accountsPageSource).not.toContain('controlsFilterSection');
  });

  it.each([
    [en, 'Credential Management', 'Credential List'],
    [ru, 'Управление учётными данными', 'Список учётных данных'],
    [zhCN, '凭证管理', '凭证列表'],
    [zhTW, '憑證管理', '憑證清單'],
  ])('uses credential terminology and omits removed workspace copy', (locale, title, list) => {
    expect(locale.nav.accounts).toBe(title);
    expect(locale.accounts.title).toBe(title);
    expect(locale.accounts.tab_accounts).toBe(list);
    expect(locale.accounts.tab_health).toBeTypeOf('string');
    expect(locale.accounts.metric_attention).toBeTypeOf('string');
    expect(locale.accounts.metric_quota_risk).toBeTypeOf('string');
    expect(locale.accounts.metric_unconfirmed).toBeTypeOf('string');
    expect(locale.accounts.account_display_masked).toBeTypeOf('string');
    expect(locale.accounts.account_display_full).toBeTypeOf('string');
    expect(locale.accounts.show_full_credentials_hint).toBeTypeOf('string');
    expect(locale.accounts.show_masked_credentials_hint).toBeTypeOf('string');
    expect(locale.monitoring.observed_from_usage_headers).toBeTypeOf('string');
    expect(locale.monitoring.observed_from_usage_headers_at).toBeTypeOf('string');
    expect(locale.accounts).not.toHaveProperty('tab_inspection');
    expect(locale.accounts).not.toHaveProperty('tab_quota');
    expect(locale.accounts).not.toHaveProperty('tab_value');
    expect(locale.accounts).not.toHaveProperty('metric_low');
    expect(locale.accounts).not.toHaveProperty('metric_value');
    expect(locale.accounts).not.toHaveProperty('legacy_quota_entry');
    expect(locale.nav).not.toHaveProperty('codex_inspection');
    expect(locale.nav).not.toHaveProperty('codex_inspection_short');
    expect(locale.nav).not.toHaveProperty('server_codex_inspection');
    expect(locale).not.toHaveProperty('quota_management');
  });

  it.each([
    [en, 'Health Inspection'],
    [ru, 'Проверка состояния'],
    [zhCN, '健康巡检'],
    [zhTW, '健康巡檢'],
  ])('localizes the health inspection tab', (locale, expectedLabel) => {
    expect(locale.accounts.tab_health).toBe(expectedLabel);
  });

  it.each([
    [en, ['Overview', 'Quota', 'Settings', 'Models', 'Diagnostics']],
    [ru, ['Обзор', 'Квота', 'Настройки', 'Модели', 'Диагностика']],
    [zhCN, ['概览', '额度', '配置', '模型', '诊断']],
    [zhTW, ['概覽', '額度', '設定', '模型', '診斷']],
  ])('uses concise credential detail tab labels', (locale, expectedLabels) => {
    expect([
      locale.accounts.detail_tab_overview,
      locale.accounts.detail_tab_quota,
      locale.accounts.detail_tab_config,
      locale.accounts.detail_tab_models,
      locale.accounts.detail_tab_diagnostics,
    ]).toEqual(expectedLabels);
  });

  it.each([en, ru, zhCN, zhTW])('keeps credential-configuration copy complete', (locale) => {
    const accounts = locale.accounts as Record<string, unknown>;
    [
      'detail_tab_config',
      'detail_tab_models',
      'detail_runtime_config_unavailable',
      'config_view_raw_data',
      'config_raw_data_redacted',
      'config_raw_data_hint',
      'detail_overview_open_config',
    ].forEach((key) => expect(accounts[key]).toBeTypeOf('string'));
    expect(accounts).not.toHaveProperty('detail_tab_credential');
  });

  it.each([en, ru, zhCN, zhTW])('keeps codex reset action copy complete', (locale) => {
    const codexQuota = locale.codex_quota as Record<string, unknown>;
    [
      'reset_verify_in_progress',
      'reset_unavailable_no_credits',
      'reset_unsupported_credential',
      'reset_requires_verification_hint',
      'reset_verify_failed_message',
      'reset_no_credits_message',
    ].forEach((key) => expect(codexQuota[key]).toBeTypeOf('string'));
  });

  it.each([
    [en, 'OAuth Configuration'],
    [ru, 'Настройки OAuth'],
    [zhCN, 'OAuth 配置'],
    [zhTW, 'OAuth 設定'],
  ])('localizes the OAuth configuration tab', (locale, expectedLabel) => {
    expect(locale.accounts.tab_oauth).toBe(expectedLabel);
  });

  it.each([
    [en, 'List', 'Relationship Diagram', 'Delete Rules'],
    [ru, 'Список', 'Схема связей', 'Удалить правила'],
    [zhCN, '列表', '关系图', '删除规则'],
    [zhTW, '清單', '關係圖', '刪除規則'],
  ])(
    'uses precise OAuth list, relationship and rule-deletion terminology',
    (locale, listLabel, diagramLabel, deleteLabel) => {
      expect(locale.oauth_model_alias.view_mode_list).toBe(listLabel);
      expect(locale.oauth_model_alias.view_mode_diagram).toBe(diagramLabel);
      expect(locale.oauth_excluded.delete).toBe(deleteLabel);
      expect(locale.oauth_model_alias.delete).toBe(deleteLabel);
      expect(locale.accounts.oauth_preview_provider_label).toBeTypeOf('string');
      expect(locale.accounts.oauth_preview_direct_summary).toBeTypeOf('string');
    }
  );

  it.each([
    [
      en,
      [
        'Credential / Account',
        'Availability',
        'Recent Requests',
        'Historical Usage',
        'Quota Details',
        'Actions',
      ],
    ],
    [
      ru,
      [
        'Учётные данные / аккаунт',
        'Доступность',
        'Последние запросы',
        'История использования',
        'Сведения о квоте',
        'Действия',
      ],
    ],
    [zhCN, ['凭证/账号', '可用状态', '最近请求', '历史用量', '额度信息', '操作']],
    [zhTW, ['憑證/帳號', '可用狀態', '最近請求', '歷史用量', '額度資訊', '操作']],
  ])('localizes the six credential list headers', (locale, expectedHeaders) => {
    expect([
      locale.accounts.list_header_credential,
      locale.accounts.list_header_availability,
      locale.accounts.list_header_recent_requests,
      locale.accounts.list_header_historical_usage,
      locale.accounts.list_header_quota,
      locale.accounts.list_header_actions,
    ]).toEqual(expectedHeaders);
  });
});
