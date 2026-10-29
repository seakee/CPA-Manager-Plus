import { fullWebLanguage } from './fullweb';

// Every string of the External workspace, in the four supported locales. The
// tuple order is en, zh-CN, zh-TW, ru, matching `configurationWorkspace`: the
// External surface is small enough that one table per key keeps the four
// translations of a term adjacent and reviewable.
const messages = {
  blockerKeyRejectedTitle: [
    'Your CPA refused the saved management key',
    '你的 CPA 拒绝了已保存的管理密钥',
    '你的 CPA 拒絕了已儲存的管理金鑰',
    'Ваш CPA отклонил сохранённый ключ управления',
  ],
  blockerKeyRejectedHelp: [
    'The saved key no longer authenticates against your CPA. Supply the current key to reconnect; nothing else is retried automatically.',
    '已保存的密钥无法再通过 CPA 的身份校验。请提交当前密钥重新连接，其他操作不会自动重试。',
    '已儲存的金鑰無法再通過 CPA 的身分驗證。請提交目前金鑰重新連線，其他操作不會自動重試。',
    'Сохранённый ключ больше не проходит проверку на вашем CPA. Укажите текущий ключ, чтобы восстановить соединение; остальное не повторяется автоматически.',
  ],
  blockerKeyRejectedForbidden: [
    'Your CPA refused this Manager client, or its address is banned after repeated failed authentications. Wait out the ban window, then rebind with the current key.',
    'CPA 拒绝了本 Manager 客户端，或本机地址因多次认证失败被封禁。请等待封禁窗口结束后，用当前密钥重新绑定。',
    'CPA 拒絕了本 Manager 用戶端，或本機位址因多次驗證失敗被封鎖。請等待封鎖時間結束後，用目前金鑰重新綁定。',
    'CPA отклонил клиент Manager или заблокировал адрес после нескольких неудачных проверок. Дождитесь окончания блокировки и привяжите текущий ключ.',
  ],
  blockerTargetMovedTitle: [
    'The CPA address changed; reconnect is required',
    'CPA 地址已变更，需要重新连接',
    'CPA 位址已變更，需要重新連線',
    'Адрес CPA изменился, требуется повторное подключение',
  ],
  blockerTargetMovedHelp: [
    'This installation was committed to another address. A rebind replaces a key, never an address, so start a new External connection instead.',
    '本安装已绑定到另一个地址。重新绑定只更换密钥、不更换地址，请改为新建外部连接。',
    '本安裝已綁定到另一個位址。重新綁定只更換金鑰、不更換位址，請改為建立新的外部連線。',
    'Эта установка привязана к другому адресу. Перепривязка меняет только ключ, но не адрес, поэтому создайте новое внешнее подключение.',
  ],
  blockerVersionUnsupportedTitle: [
    'This CPA version does not support Manager writes',
    '该 CPA 版本不支持由 Manager 写入',
    '該 CPA 版本不支援由 Manager 寫入',
    'Эта версия CPA не поддерживает запись из Manager',
  ],
  blockerVersionUnsupportedHelp: [
    'Reads stay available; saving configuration and changing keys are refused until the CPA runs a v8 or later release.',
    '读取仍可用；在 CPA 升级到 v8 或更高版本之前，保存配置与变更密钥都会被拒绝。',
    '讀取仍可用；在 CPA 升級到 v8 或更高版本之前，儲存設定與變更金鑰都會被拒絕。',
    'Чтение доступно; сохранение конфигурации и изменение ключей отклоняются, пока CPA не обновится до v8 или новее.',
  ],
  blockerVersionLabel: [
    'Observed version',
    '观测到的版本',
    '觀測到的版本',
    'Наблюдаемая версия',
  ],
  blockerVersionUnknown: [
    'Not reported',
    '未上报',
    '未回報',
    'Не сообщается',
  ],
  supportSupported: ['Supported', '受支持', '受支援', 'Поддерживается'],
  supportUnsupported: ['Unsupported', '不受支持', '不受支援', 'Не поддерживается'],
  supportUnknown: ['Unverified', '未验证', '未驗證', 'Не подтверждено'],
  blockerLegacyReplacedTitle: [
    'This write is unavailable on an External CPA',
    '外部 CPA 上不提供该写入',
    '外部 CPA 上不提供該寫入',
    'Эта запись недоступна для внешнего CPA',
  ],
  blockerLegacyReplacedHelp: [
    'The Manager owns this change through its own screen. Edit it there so the connection and the saved configuration stay in step.',
    '该变更由 Manager 自己的页面负责。请在该页面修改，以确保连接与已保存的配置保持一致。',
    '該變更由 Manager 自己的頁面負責。請在該頁面修改，以確保連線與已儲存的設定保持一致。',
    'Это изменение выполняется на собственной странице Manager. Изменяйте его там, чтобы подключение и сохранённая конфигурация не расходились.',
  ],
  blockerDismiss: ['Dismiss', '关闭', '關閉', 'Закрыть'],
  rebindAction: ['Rebind management key', '重新绑定管理密钥', '重新綁定管理金鑰', 'Привязать ключ заново'],
  rebindTitle: ['Rebind management key', '重新绑定管理密钥', '重新綁定管理金鑰', 'Привязать ключ заново'],
  rebindHelp: [
    'Enter the management key your CPA currently holds. The key is sent once, proven against this same address, and never stored in this browser.',
    '请输入 CPA 当前使用的管理密钥。密钥只提交一次，在同一个地址上验证，绝不会保存在此浏览器中。',
    '請輸入 CPA 目前使用的管理金鑰。金鑰只提交一次，在同一個位址上驗證，絕不會儲存在此瀏覽器中。',
    'Укажите ключ управления, который сейчас использует ваш CPA. Ключ отправляется один раз, проверяется на этом же адресе и не сохраняется в браузере.',
  ],
  rebindKeyLabel: ['Management key', '管理密钥', '管理金鑰', 'Ключ управления'],
  rebindSubmit: ['Verify and rebind', '验证并重新绑定', '驗證並重新綁定', 'Проверить и привязать'],
  rebindSubmitting: ['Verifying…', '验证中…', '驗證中…', 'Проверка…'],
  rebindCancel: ['Cancel', '取消', '取消', 'Отмена'],
  rebindSucceededRebound: [
    'The management key was replaced and the connection works again.',
    '管理密钥已替换，连接已恢复。',
    '管理金鑰已替換，連線已恢復。',
    'Ключ управления заменён, соединение восстановлено.',
  ],
  rebindSucceededReverified: [
    'The same key verified again; nothing was replaced.',
    '同一密钥验证通过，未做任何替换。',
    '同一金鑰驗證通過，未做任何替換。',
    'Тот же ключ подтверждён повторно, замены не было.',
  ],
  rebindRefusedHelp: [
    'A rebind replaces a key, never the CPA address. Check the address of this installation, or start a new External connection.',
    '重新绑定只更换密钥，不更换 CPA 地址。请核对该安装的地址，或新建外部连接。',
    '重新綁定只更換金鑰，不更換 CPA 位址。請核對該安裝的位址，或建立新的外部連線。',
    'Перепривязка меняет только ключ, но не адрес CPA. Проверьте адрес этой установки или создайте новое внешнее подключение.',
  ],
  rebindStaleHelp: [
    'The installation changed while the key was being proven. Reload the page and send the key again.',
    '验证密钥期间安装状态已变更。请重新加载页面后再次提交密钥。',
    '驗證金鑰期間安裝狀態已變更。請重新載入頁面後再次提交金鑰。',
    'Установка изменилась во время проверки ключа. Перезагрузите страницу и отправьте ключ снова.',
  ],
  rebindUnavailableHelp: [
    'The Manager could not resolve this installation\'s CPA address. Nothing was sent to your CPA.',
    'Manager 无法解析该安装的 CPA 地址，未向 CPA 发送任何请求。',
    'Manager 無法解析該安裝的 CPA 位址，未向 CPA 傳送任何請求。',
    'Manager не смог определить адрес CPA этой установки. Запросы к CPA не отправлялись.',
  ],
  rebindFailedHelp: [
    'The rebind did not complete. Keep the same request and try again without changing the installation.',
    '重新绑定未完成。请保留同一请求并重试，不要改动安装状态。',
    '重新綁定未完成。請保留同一請求並重試，不要改動安裝狀態。',
    'Перепривязка не завершена. Повторите тот же запрос, не меняя установку.',
  ],
  usageTitle: ['CPA usage collection', 'CPA 用量采集', 'CPA 用量擷取', 'Сбор статистики CPA'],
  usageHelp: [
    'Collection reads your CPA\'s usage queue over its management API. Manager never writes to your CPA without your confirmation.',
    '采集通过管理 API 读取 CPA 的用量队列。未经你确认，Manager 不会写入你的 CPA。',
    '擷取透過管理 API 讀取 CPA 的用量佇列。未經你確認，Manager 不會寫入你的 CPA。',
    'Сбор читает очередь статистики CPA через management API. Manager ничего не записывает в CPA без вашего подтверждения.',
  ],
  usageStatistics: ['Usage statistics on the CPA', 'CPA 用量统计', 'CPA 用量統計', 'Статистика использования на CPA'],
  usageEnabled: ['Enabled', '已启用', '已啟用', 'Включена'],
  usageDisabled: ['Disabled', '已关闭', '已關閉', 'Отключена'],
  usageCollector: ['Collector', '采集器', '擷取器', 'Сборщик'],
  usageCollectionStarted: ['Collection started', '已开始采集', '已開始擷取', 'Сбор начат'],
  usageNotStarted: ['not started', '尚未开始', '尚未開始', 'не начат'],
  usageVersion: ['CPA version', 'CPA 版本', 'CPA 版本', 'Версия CPA'],
  usageWritesBlocked: ['Writes blocked by this version', '该版本阻止写入', '該版本阻止寫入', 'Запись заблокирована этой версией'],
  usageWritesAllowed: ['Writes verified', '写入已验证', '寫入已驗證', 'Запись подтверждена'],
  usageBlockedReason: ['Collection blocked by', '采集中断原因', '擷取中斷原因', 'Причина остановки сбора'],
  usageEnableAction: ['Enable usage statistics on your CPA', '在你的 CPA 上启用用量统计', '在你的 CPA 上啟用用量統計', 'Включить статистику на вашем CPA'],
  usageConfirmTitle: ['Enable usage statistics on your CPA?', '要在你的 CPA 上启用用量统计吗？', '要在你的 CPA 上啟用用量統計嗎？', 'Включить статистику использования на вашем CPA?'],
  usageConfirmHelp: [
    'This writes usage-statistics-enabled: true to the CPA at this connection. It changes a setting on a CPA you administer, and it is the only write Manager makes before it collects anything.',
    '此操作会向该连接指向的 CPA 写入 usage-statistics-enabled: true。它会改变由你管理的 CPA 上的一项设置，也是 Manager 在开始采集前唯一的写入。',
    '此操作會向該連線指向的 CPA 寫入 usage-statistics-enabled: true。它會改變由你管理的 CPA 上的一項設定，也是 Manager 在開始擷取前唯一的寫入。',
    'Будет записано usage-statistics-enabled: true в CPA по этому подключению. Это изменение настройки CPA, которым вы управляете, и единственная запись Manager до начала сбора.',
  ],
  usageConfirmAction: ['Write and start collection', '写入并开始采集', '寫入並開始擷取', 'Записать и начать сбор'],
  usageConfirmCancel: ['Cancel', '取消', '取消', 'Отмена'],
  usageConfirmed: [
    'Usage statistics were enabled and collection started.',
    '用量统计已启用，采集已开始。',
    '用量統計已啟用，擷取已開始。',
    'Статистика включена, сбор начат.',
  ],
  usageEnableFailed: [
    'The confirmation could not be applied. Nothing is claimed about your CPA until it is read back.',
    '确认未能生效。在重新读取 CPA 之前，不会对 CPA 状态作出任何声明。',
    '確認未能生效。在重新讀取 CPA 之前，不會對 CPA 狀態作出任何聲明。',
    'Подтверждение не применилось. Состояние CPA не считается известным, пока оно не прочитано снова.',
  ],
  usageReadFailed: ['Usage state could not be read', '无法读取用量状态', '無法讀取用量狀態', 'Не удалось прочитать состояние сбора'],
  reload: ['Reload page', '重新加载页面', '重新載入頁面', 'Перезагрузить страницу'],
} satisfies Record<string, readonly [string, string, string, string]>;

export function externalWorkspaceText(key: keyof typeof messages): string {
  const language = fullWebLanguage();
  const index =
    language === 'zh-TW' ? 2 : language === 'ru' ? 3 : language.startsWith('zh') ? 1 : 0;
  return messages[key][index];
}

// versionSupportText names the class the Manager proved for the observed CPA
// version. The three classes are the whole contract: a version is either proven
// to speak the written surface, proven not to, or not proven at all.
export function versionSupportText(support: string | undefined): string {
  switch (support) {
    case 'supported':
      return externalWorkspaceText('supportSupported');
    case 'unsupported':
      return externalWorkspaceText('supportUnsupported');
    default:
      return externalWorkspaceText('supportUnknown');
  }
}
