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
  blockerDismiss: ['Dismiss', '关闭', '關閉', 'Закрыть'],
  details: ['Details', '详情', '詳情', 'Подробности'],
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
  rebindAuthenticationHelp: [
    'Your Manager session expired before the key was proven. Sign in again, then repeat the rebind; nothing was changed.',
    '验证密钥前 Manager 会话已过期。请重新登录后再次绑定，当前未做任何更改。',
    '驗證金鑰前 Manager 工作階段已過期。請重新登入後再次綁定，目前未做任何變更。',
    'Сеанс Manager завершился до проверки ключа. Войдите снова и повторите привязку; ничего не изменено.',
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
  usageCollectorRunning: ['Running', '运行中', '執行中', 'Работает'],
  usageCollectorStarting: ['Starting', '启动中', '啟動中', 'Запускается'],
  usageCollectorStopped: ['Stopped', '已停止', '已停止', 'Остановлен'],
  usageCollectorError: ['Failed', '失败', '失敗', 'Сбой'],
  usageCollectorUnknown: ['Unknown', '未知', '未知', 'Неизвестно'],
  usageCollectionStarted: ['Collection started', '已开始采集', '已開始擷取', 'Сбор начат'],
  usageNotStarted: ['not started', '尚未开始', '尚未開始', 'не начат'],
  usageVersion: ['CPA version', 'CPA 版本', 'CPA 版本', 'Версия CPA'],
  usageWritesBlocked: ['Writes blocked by this version', '该版本阻止写入', '該版本阻止寫入', 'Запись заблокирована этой версией'],
  usageWritesAllowed: ['Writes allowed', '允许写入', '允許寫入', 'Запись разрешена'],
  usageBlockedReason: ['Collection blocked by', '采集中断原因', '擷取中斷原因', 'Причина остановки сбора'],
  usageBlockedAuthority: [
    'the Manager has no collection authority for this installation',
    'Manager 尚未取得该安装的采集授权',
    'Manager 尚未取得此安裝的擷取授權',
    'у Manager нет прав на сбор для этой установки',
  ],
  usageBlockedCollector: [
    'the Collector could not start',
    '采集器未能启动',
    '擷取器未能啟動',
    'сборщик не запустился',
  ],
  usageBlockedStatistics: [
    'usage statistics are turned off on the CPA',
    'CPA 的用量统计已关闭',
    'CPA 的用量統計已關閉',
    'статистика использования на CPA отключена',
  ],
  usageBlockedMonitoring: [
    'request monitoring is turned off in Manager',
    'Manager 的请求监控已关闭',
    'Manager 的請求監控已關閉',
    'мониторинг запросов в Manager отключён',
  ],
  usageBlockedExternalAuthority: [
    'the external CPA could not complete the usage statistics check or update',
    '外部 CPA 当前无法完成用量统计校验或写入',
    '外部 CPA 目前無法完成用量統計驗證或寫入',
    'внешний CPA не смог выполнить проверку или запись настройки статистики',
  ],
  usageBlockedUnknown: [
    'the last attempt was stopped for a reason this Console cannot name',
    '上次尝试因本控制台无法识别的原因此中断',
    '上次嘗試因本控制台無法識別的原因此中斷',
    'последняя попытка прервана по неизвестной этой консоли причине',
  ],
  usageEnableAction: ['Enable usage statistics on your CPA', '在你的 CPA 上启用用量统计', '在你的 CPA 上啟用用量統計', 'Включить статистику на вашем CPA'],
  usageStartAction: ['Start usage collection', '开始采集用量', '開始擷取用量', 'Запустить сбор статистики'],
  usageConfirmTitle: ['Enable usage statistics on your CPA?', '要在你的 CPA 上启用用量统计吗？', '要在你的 CPA 上啟用用量統計嗎？', 'Включить статистику использования на вашем CPA?'],
  usageConfirmHelp: [
    'This writes usage-statistics-enabled: true to the CPA at this connection. It changes a setting on a CPA you administer, and it is the only write Manager makes before it collects anything.',
    '此操作会向该连接指向的 CPA 写入 usage-statistics-enabled: true。它会改变由你管理的 CPA 上的一项设置，也是 Manager 在开始采集前唯一的写入。',
    '此操作會向該連線指向的 CPA 寫入 usage-statistics-enabled: true。它會改變由你管理的 CPA 上的一項設定，也是 Manager 在開始擷取前唯一的寫入。',
    'Будет записано usage-statistics-enabled: true в CPA по этому подключению. Это изменение настройки CPA, которым вы управляете, и единственная запись Manager до начала сбора.',
  ],
  usageConfirmAction: ['Write and start collection', '写入并开始采集', '寫入並開始擷取', 'Записать и начать сбор'],
  usageConfirmCancel: ['Cancel', '取消', '取消', 'Отмена'],
  usageConfirmRetry: ['Try again', '重试', '重試', 'Повторить'],
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

  // The External setup form. The wizard sends one strategy write, so a refusal
  // arrives as a bounded code: each of them names the cause the operator can
  // act on instead of the shared "setup did not finish" sentence.
  setupExternalNoMigration: [
    'This connects an existing CPA. Nothing is copied from it: its history, requests and logs stay on that CPA.',
    '这里连接的是已有 CPA，不会从它复制任何内容：它的历史用量、请求记录与日志都保留在原 CPA 上。',
    '這裡連接的是既有 CPA，不會從它複製任何內容：它的歷史用量、請求記錄與日誌都保留在原 CPA 上。',
    'Здесь подключается существующий CPA. Из него ничего не копируется: история, запросы и журналы остаются на этом CPA.',
  ],
  setupAddressInvalid: [
    'Enter the CPA base address as a complete http:// or https:// URL, without credentials, query or fragment.',
    '请填写 CPA 基础地址：完整的 http:// 或 https:// 地址，不能带凭据、查询参数或片段。',
    '請填寫 CPA 基礎位址：完整的 http:// 或 https:// 位址，不能包含憑證、查詢參數或片段。',
    'Укажите базовый адрес CPA как полный URL http:// или https://, без учётных данных, параметров запроса и фрагмента.',
  ],
  setupKeyInvalid: [
    'Enter the CPA management key. It cannot contain spaces or control characters.',
    '请填写 CPA 管理密钥；密钥不能包含空白或控制字符。',
    '請填寫 CPA 管理金鑰；金鑰不能包含空白或控制字元。',
    'Укажите ключ управления CPA. Он не может содержать пробелы и управляющие символы.',
  ],
  setupBlockerConnectionInvalid: [
    'That address or management key is not in a usable form. Enter the CPA base URL, for example http://10.0.0.5:8317, and its management key.',
    '地址或管理密钥格式不可用。请填写 CPA 基础地址（例如 http://10.0.0.5:8317）及其管理密钥。',
    '位址或管理金鑰格式不可用。請填寫 CPA 基礎位址（例如 http://10.0.0.5:8317）及其管理金鑰。',
    'Адрес или ключ управления указаны в недопустимом виде. Укажите базовый адрес CPA (например, http://10.0.0.5:8317) и его ключ управления.',
  ],
  setupBlockerKeyRejected: [
    'The CPA rejected this management key. Check the key in the CPA management settings, then try again.',
    'CPA 拒绝了这个管理密钥。请在 CPA 管理设置中核对密钥后重试。',
    'CPA 拒絕了這個管理金鑰。請在 CPA 管理設定中核對金鑰後重試。',
    'CPA отклонил этот ключ управления. Проверьте ключ в настройках управления CPA и повторите попытку.',
  ],
  // The Manager only knows that no safe read of the management configuration was
  // obtained: an unanswered connection, a proxy in between, a non-naming HTTP
  // status or an unreadable body all arrive as this one code. The sentence names
  // what the operator can check instead of asserting why the read failed.
  setupBlockerUnreachable: [
    'The CPA management configuration could not be read safely, so this build records nothing about that CPA. Check the address, connectivity from this Manager, that the target is CPA v8, and its logs.',
    '无法安全读取 CPA 管理配置，因此本程序没有记录该 CPA 的任何状态。请检查地址、本管理程序到该地址的连通性、目标是否为 CPA v8，以及 CPA 日志。',
    '無法安全讀取 CPA 管理設定，因此本程式沒有記錄該 CPA 的任何狀態。請檢查位址、本管理程式到該位址的連通性、目標是否為 CPA v8，以及 CPA 日誌。',
    'Конфигурацию управления CPA не удалось безопасно прочитать, поэтому Manager ничего не записывает об этом CPA. Проверьте адрес, доступность из этого Manager, что цель — CPA v8, и журналы CPA.',
  ],
  setupBlockerManagementUnavailable: [
    'That address answered, but it does not expose the CPA v8 management interface. Connect a CPA v8, or adopt the existing installation instead.',
    '该地址有响应，但没有提供 CPA v8 管理接口。请连接 CPA v8，或改为接管已有安装。',
    '該位址有回應，但沒有提供 CPA v8 管理介面。請連接 CPA v8，或改為接管既有安裝。',
    'Адрес ответил, но интерфейс управления CPA v8 там недоступен. Подключите CPA v8 или перенесите существующую установку.',
  ],
  setupBlockerConfigUnreadable: [
    'The CPA answered with a configuration this Manager cannot read safely. Check that the address is CPA v8, then try again.',
    'CPA 返回了本管理程序无法安全读取的配置。请确认该地址是 CPA v8 后重试。',
    'CPA 回傳了本管理程式無法安全讀取的設定。請確認該位址是 CPA v8 後重試。',
    'CPA вернул конфигурацию, которую Manager не может безопасно прочитать. Убедитесь, что адрес указывает на CPA v8, и повторите попытку.',
  ],
  setupBlockerQualification: [
    'This build has not qualified external CPA connections, so it cannot connect one. Use an embedded installation instead.',
    '当前构建尚未开放外部 CPA 连接，无法连接外部 CPA。请改用内置安装。',
    '目前建置尚未開放外部 CPA 連線，無法連接外部 CPA。請改用內建安裝。',
    'Эта сборка не поддерживает внешние подключения CPA, поэтому подключить внешний CPA нельзя. Используйте встроенную установку.',
  ],
  setupBlockerOwnerConflict: [
    'This installation already owns a different method or source. Start a new installation.',
    '该安装已归属其他方式或来源。请新建一次安装。',
    '該安裝已歸屬其他方式或來源。請新建一次安裝。',
    'Эта установка уже закреплена за другим способом или источником. Начните новую установку.',
  ],
  setupBlockerStorage: [
    'The management key could not be stored securely. Check the Manager encryption key and storage, then try again.',
    '无法安全保存管理密钥。请检查管理程序的加密密钥与存储后重试。',
    '無法安全保存管理金鑰。請檢查管理程式的加密金鑰與儲存後重試。',
    'Не удалось безопасно сохранить ключ управления. Проверьте ключ шифрования и хранилище Manager и повторите попытку.',
  ],
  // The Manager refuses Connect External while it cannot prove the embedded CPA
  // runtime is stopped with crash recovery disarmed; the four sentences below
  // name the runtime state that refused and the recovery the operator performs.
  // The Manager's own `Action` sentence is English only, so the browser states
  // the cause in the operator's language instead of rendering that text.
  setupBlockerSupervisorUnavailable: [
    "The Manager cannot reach cpamp-runtime-supervisor, so it cannot prove the embedded CPA runtime is stopped and nothing is recorded. In the official image, check or restart cpamp-runtime-supervisor or the container so it reports offline with recovery disarmed, then retry. On a standalone deployment without an embedded supervisor, unset CPAMP_RUNTIME_URL and CPAMP_RUNTIME_TOKEN_FILE and restart the Manager.",
    "安装程序无法连接 cpamp-runtime-supervisor，因此无法确认内置 CPA 运行时已停止，本次没有记录任何状态。使用官方镜像时，请检查或重启 cpamp-runtime-supervisor 或容器，使其报告离线且未启用自动恢复后重试。若部署中没有内置 Supervisor，请取消设置 CPAMP_RUNTIME_URL 与 CPAMP_RUNTIME_TOKEN_FILE 并重启安装程序。",
    "安裝程式無法連線 cpamp-runtime-supervisor，因此無法確認內建 CPA 執行環境已停止，本次沒有記錄任何狀態。使用官方映像時，請檢查或重新啟動 cpamp-runtime-supervisor 或容器，使其回報離線且未啟用自動復原後重試。若部署中沒有內建 Supervisor，請取消設定 CPAMP_RUNTIME_URL 與 CPAMP_RUNTIME_TOKEN_FILE 並重新啟動安裝程式。",
    "Manager не может связаться с cpamp-runtime-supervisor, поэтому не может подтвердить, что встроенная среда CPA остановлена, и ничего не записывает. В официальном образе проверьте или перезапустите cpamp-runtime-supervisor либо контейнер, чтобы он сообщал о состоянии «остановлено» с отключённым автозапуском, и повторите попытку. Если в развёртывании нет встроенного супервизора, снимите переменные CPAMP_RUNTIME_URL и CPAMP_RUNTIME_TOKEN_FILE и перезапустите Manager.",
  ],
  setupBlockerRuntimeStartUnresolved: [
    "An embedded CPA start or update reported by the Manager was never confirmed finished, so Connect External stays blocked. Wait for it to finish, or restart cpamp-runtime-supervisor or the container to establish a fresh stopped runtime generation, then retry.",
    "安装程序记录的某次内置 CPA 启动或更新尚未确认结束，因此暂时无法连接外部 CPA。请等待该操作完成，或重启 cpamp-runtime-supervisor 或容器以建立新的已停止运行环境后重试。",
    "安裝程式記錄的某次內建 CPA 啟動或更新尚未確認結束，因此暫時無法連接外部 CPA。請等待該操作完成，或重新啟動 cpamp-runtime-supervisor 或容器以建立新的已停止執行環境後重試。",
    "Manager зафиксировал незавершённый запуск или обновление встроенной среды CPA, поэтому подключение внешнего CPA заблокировано. Дождитесь завершения операции либо перезапустите cpamp-runtime-supervisor или контейнер, чтобы получить новое остановленное поколение среды, и повторите попытку.",
  ],
  setupBlockerRuntimeNotStopped: [
    "The embedded CPA runtime is still running or has automatic crash recovery armed, so Connect External cannot be committed. Stop the embedded runtime, or restart cpamp-runtime-supervisor or the container, so it stays offline with recovery disarmed, then retry.",
    "内置 CPA 运行时仍在运行，或已启用崩溃自动恢复，因此无法提交外部连接。请停止内置运行时，或重启 cpamp-runtime-supervisor 或容器，使其保持离线且未启用自动恢复后重试。",
    "內建 CPA 執行環境仍在運行，或已啟用當機自動復原，因此無法提交外部連線。請停止內建執行環境，或重新啟動 cpamp-runtime-supervisor 或容器，使其保持離線且未啟用自動復原後重試。",
    "Встроенная среда CPA всё ещё работает или для неё включён автоперезапуск после сбоя, поэтому подключение внешнего CPA нельзя зафиксировать. Остановите встроенную среду либо перезапустите cpamp-runtime-supervisor или контейнер, чтобы она оставалась остановленной без автозапуска, и повторите попытку.",
  ],
  setupBlockerRuntimeRecoveryUnverified: [
    "cpamp-runtime-supervisor reports the embedded CPA runtime offline without its recovery state, so the Manager cannot prove no restart will be retried. Check or restart cpamp-runtime-supervisor so it reports recovery disarmed (inactive or manual_intervention), then retry.",
    "cpamp-runtime-supervisor 报告内置 CPA 运行时已离线，但没有给出恢复状态，安装程序无法确认不会自动重启。请检查或重启 cpamp-runtime-supervisor，使其报告自动恢复已停用（inactive 或 manual_intervention）后重试。",
    "cpamp-runtime-supervisor 回報內建 CPA 執行環境已離線，但沒有提供復原狀態，安裝程式無法確認不會自動重新啟動。請檢查或重新啟動 cpamp-runtime-supervisor，使其回報自動復原已停用（inactive 或 manual_intervention）後重試。",
    "cpamp-runtime-supervisor сообщает, что встроенная среда CPA остановлена, но не передаёт состояние восстановления, поэтому Manager не может подтвердить, что перезапуск не произойдёт. Проверьте или перезапустите cpamp-runtime-supervisor, чтобы он сообщал об отключённом восстановлении (inactive или manual_intervention), и повторите попытку.",
  ],
} satisfies Record<string, readonly [string, string, string, string]>;

export function externalWorkspaceText(key: keyof typeof messages): string {
  const language = fullWebLanguage();
  const index =
    language === 'zh-TW' ? 2 : language === 'ru' ? 3 : language.startsWith('zh') ? 1 : 0;
  return messages[key][index];
}

// blockedReasonText names why the Collector refused to start. The Manager
// reports a stable code, never a sentence for a person: a known code reads as
// its cause, and one this Console does not know still reads as an explanation
// instead of the wire word.
export function blockedReasonText(reason: string | undefined): string {
  switch (reason) {
    case 'usage_collection_authority_unavailable':
      return externalWorkspaceText('usageBlockedAuthority');
    case 'usage_collector_unavailable':
      return externalWorkspaceText('usageBlockedCollector');
    case 'usage_statistics_disabled':
      return externalWorkspaceText('usageBlockedStatistics');
    case 'usage_monitoring_disabled':
      return externalWorkspaceText('usageBlockedMonitoring');
    case 'external_cpa_authority_unavailable':
      return externalWorkspaceText('usageBlockedExternalAuthority');
    default:
      return externalWorkspaceText('usageBlockedUnknown');
  }
}

// collectorStateText names the Collector phase the Manager reports. The raw value
// is a Manager phase name, never something to show a person: an unknown one reads
// as unknown rather than as the wire word.
export function collectorStateText(collector: string | undefined): string {
  switch (collector) {
    case 'running':
      return externalWorkspaceText('usageCollectorRunning');
    case 'starting':
      return externalWorkspaceText('usageCollectorStarting');
    case 'stopped':
      return externalWorkspaceText('usageCollectorStopped');
    case 'error':
      return externalWorkspaceText('usageCollectorError');
    default:
      return externalWorkspaceText('usageCollectorUnknown');
  }
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

// The strategy refusals an External setup can receive, each with the cause it
// names. `management_safe_read_unavailable` is the Manager's only "no safe read
// came back" code, so its sentence lists what to check instead of claiming the
// address did not answer; an address that answered without the v8 management
// interface is `owner_upgrade_required` and keeps its own sentence.
const setupBlockers: Record<string, keyof typeof messages> = {
  management_connection_invalid: 'setupBlockerConnectionInvalid',
  management_authentication_rejected: 'setupBlockerKeyRejected',
  management_safe_read_unavailable: 'setupBlockerUnreachable',
  owner_upgrade_required: 'setupBlockerManagementUnavailable',
  management_v8_config_invalid: 'setupBlockerConfigUnreadable',
  required_v8_profile_qualification_required: 'setupBlockerQualification',
  fresh_strategy_owner_conflict: 'setupBlockerOwnerConflict',
  management_secret_storage_unavailable: 'setupBlockerStorage',
  // The Manager's guarded Connect External refusals. Its JSON carries an English
  // `Action` sentence beside each code; the browser keeps the code and names the
  // recovery in the operator's own language instead of rendering that text.
  embedded_supervisor_status_unavailable: 'setupBlockerSupervisorUnavailable',
  embedded_runtime_start_unresolved: 'setupBlockerRuntimeStartUnresolved',
  embedded_runtime_not_stopped: 'setupBlockerRuntimeNotStopped',
  embedded_runtime_recovery_unverified: 'setupBlockerRuntimeRecoveryUnverified',
};

// externalSetupBlockerKey names the sentence for a code this module knows, and
// leaves every other refusal `undefined` so the caller can show its own generic
// message rather than a guess about the cause. The caller renders the key, so a
// language switch still retranslates the message that is on screen.
export function externalSetupBlockerKey(
  code: string | undefined
): keyof typeof messages | undefined {
  return code ? setupBlockers[code] : undefined;
}
