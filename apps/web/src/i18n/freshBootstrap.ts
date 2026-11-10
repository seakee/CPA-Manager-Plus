import { fullWebLanguage } from './fullweb';

// The Bootstrap entry renders before any Console chrome exists, so its blocked,
// recovery and transport states carry their own copy. Security terms (Handoff,
// Claim, data.key, commitId, Bootstrap Transport Unavailable) stay untranslated
// so an operator can match them against the protocol and the host logs.
const en = {
  guidanceFreshTitle: 'Fresh initialization requires a host claim',
  guidanceFreshDetail:
    'This is a read-only identification result. Obtain a one-time Handoff link from the trusted host; this page never creates or claims an installation on its own.',
  guidanceUpgradeTitle: 'An existing source without upgrade qualification was detected',
  guidanceUpgradeDetail:
    'The original source can only be inspected on the trusted host. Fresh setup and the Upgrade switch are unavailable here; a successful preflight does not grant import or overwrite authority.',
  guidanceRecoveryTitle: 'Recovery requires host intervention',
  guidanceRecoveryDetail:
    'The source is not eligible for direct initialization. Run a read-only preflight on the trusted host first and protect the database, data.key and existing backups; do not attempt Fresh/Adopt/Upgrade writes from this page.',
  guidanceUnavailableTitle: 'The source state cannot be confirmed yet',
  guidanceUnavailableDetail:
    'The read-only source check produced no trusted classification. Investigate on the trusted host; this state must not be treated as Fresh, and it does not authorize initialization or migration.',
  errorTransportTitle: 'Network error (Bootstrap Transport Unavailable)',
  errorTransportCommitDetail:
    'The submission result is not confirmed. The current key and idempotent commit ID (commitId) are kept in memory. Check the network or service, then sync the server state; retry only after the server confirms it is still uninitialized. Do not reload the page or use a different key.',
  errorTransportStartDetail:
    'Cannot reach the Manager Bootstrap server. You may retry the connection once; if the server already consumed this one-time Claim Token and returned HTTP 401, run cpa-manager-plus initialize on the host again for a new Handoff link.',
  errorTransportOtherDetail:
    'Cannot reach the Manager Bootstrap server. Confirm the local supervised container or service process is running, then retry.',
  errorRejectedTitle: 'Bootstrap authorization expired or was rejected (HTTP 401)',
  errorRejectedStartDetail:
    'The one-time claim token (Claim Token) was consumed, expired, or replaced by a newer Claim Generation. Run cpa-manager-plus initialize on the host again for the latest Handoff link.',
  errorRejectedOtherDetail:
    'The current Bootstrap session authorization has expired, or this operation is closed at the current stage. Sync the server state to check the latest progress, or run the initialization command again on the host.',
  errorMismatchTitle: 'Installation identity mismatch (Bootstrap Authority Mismatch)',
  errorMismatchDetail:
    'The FreshOperation identity returned by the server (admissionId / installationId / generation / operationId) does not match the Handoff credential; the operation was stopped under the fail-closed policy.',
  errorBadRequestTitle: 'The request failed server validation (HTTP 400)',
  errorBadRequestDetail:
    'The administrator key format, byte length or character complexity does not meet the server security requirements. Check the key rules or generate a new key.',
  errorConflictTitle: 'Operation state conflict (HTTP 409)',
  errorConflictDetail:
    'The server-side FreshOperation state has changed (for example the administrator already finished setup, or a submission used a different idempotency token). Sync the server state for the latest state.',
  errorServerTitle: 'The server is temporarily unavailable',
  errorServerTitleStatus: 'The server is temporarily unavailable (HTTP {status})',
  errorServerDetail:
    'The Bootstrap server could not complete this request. Retry later or sync the server state.',
  errorUnexpectedTitle: 'The operation did not finish',
  errorUnexpectedDetail:
    'An unexpected client error occurred. Refresh the server state or obtain a new Handoff link.',
  gateTagBootstrap: 'Bootstrap Gate · Host authorization required',
  gateTagPreflight: 'Source Preflight · Read-only source check',
  gateHostTitle: 'This source needs a host preflight; it cannot be initialized as Fresh directly',
  gateUnconfirmedTitle: 'The source state is unconfirmed; direct initialization is not allowed',
  gateCheckingTitle: 'Confirming the source state; direct initialization is not allowed',
  gateHostDesc:
    'Public discovery only hints at the source type and never reads or modifies existing data; every later step must run on the trusted host.',
  gateUnconfirmedDesc:
    'No trusted Fresh classification was obtained. Check the source and service state on the host; this page cannot be used to initialize or migrate.',
  preflightTitle: 'Next step: run a read-only source preflight on the host',
  preflightDesc:
    'Replace the path with the real source database path. This command does not authorize migration, and do not claim the source is stopped while writes are still running.',
  handoffHelpTitle: 'How do I obtain or restore the Bootstrap authorization link?',
  handoffHelpLead:
    'Run the initialization command on the trusted host, then reopen this page with the full URL that contains the ',
  handoffHelpTail: ' fragment:',
  unconfirmedTitle: 'The source state is unconfirmed',
  checkingTitle: 'Reading the source state',
  unconfirmedDesc:
    'No Fresh initialization command is provided. Confirm the source, logs and service state on the trusted host; an unknown result grants no write authority.',
  recheck: 'Check the source state again',
  diagnosticsMode: 'Authorization mode',
  diagnosticsModeResume: 'Resume Session (operation GET)',
  diagnosticsModeClaim: 'Claim Exchange (POST claim/exchange)',
  diagnosticsOperationId: 'Operation ID',
  diagnosticsInstallation: 'Installation / Gen',
  authorityFailedTag: 'Bootstrap Authority Failed',
  authorityFailedTitle: 'Cannot establish the controlled Fresh Bootstrap session',
  authorityFailedDesc:
    'The client already removed the sensitive fragment from the address bar, but the Manager Bootstrap backend did not verify the authorization exchange or resume.',
  claimGeneration: 'Claim Generation',
  handoffMode: 'Handoff mode',
  handoffModeResume: 'Resume',
  handoffModeFresh: 'Fresh Claim',
  reconnectNote:
    'You can retry the connection once. If it returns HTTP 401 (the token has already been consumed), run cpa-manager-plus initialize on the host again for a new Handoff.',
  retryBootstrap: 'Retry the Bootstrap connection',
  languageSwitch: 'Language',
  languageZhCN: '简体中文',
  languageZhTW: '繁體中文',
  languageEnglish: 'English',
  languageRussian: 'Русский',
};
const zh: typeof en = {
  guidanceFreshTitle: '检测到需要宿主机认领的 Fresh 初始化状态',
  guidanceFreshDetail:
    '此状态仅为只读识别结果。请从受信宿主机获取一次性 Handoff 链接；本页面不会自行创建或认领安装。',
  guidanceUpgradeTitle: '检测到尚未获得升级资格的现有来源',
  guidanceUpgradeDetail:
    '只能在受信宿主机检查原始来源。当前无法进入 Fresh 初始化或 Upgrade 切换；预检成功也不代表获得导入或覆盖授权。',
  guidanceRecoveryTitle: '检测到需要宿主机介入的恢复状态',
  guidanceRecoveryDetail:
    '来源不具备直接初始化资格。请先在受信宿主机进行只读预检，保护数据库、data.key 和已有备份；不得从此页面尝试 Fresh/Adopt/Upgrade 写入。',
  guidanceUnavailableTitle: '来源状态暂无法确认',
  guidanceUnavailableDetail:
    '来源只读检查未获得可信分类。请在受信宿主机排查；不能根据此状态认定为 Fresh，也不能据此执行初始化或迁移。',
  errorTransportTitle: '网络连接异常（Bootstrap Transport Unavailable）',
  errorTransportCommitDetail:
    '提交结果尚未确认，当前密钥与幂等提交标识（commitId）已保留在内存中。请检查网络或服务状态后点击“同步服务端状态”；确认服务端仍未初始化后才可重试。请勿刷新页面或更换密钥。',
  errorTransportStartDetail:
    '无法连接至 Manager Bootstrap 服务端。您可尝试重新连接一次；若服务端已消费本次一次性 Claim Token 并返回 HTTP 401，请在宿主机重新执行 cpa-manager-plus initialize 获取新的 Handoff 链接。',
  errorTransportOtherDetail:
    '无法连接至 Manager Bootstrap 服务端，请确认本地受控容器或服务进程正在运行后重试。',
  errorRejectedTitle: 'Bootstrap 授权已失效或被拒绝（HTTP 401）',
  errorRejectedStartDetail:
    '一次性认领令牌（Claim Token）已被消费、过期，或被更新的初始化代际（Claim Generation）取代。请在宿主机重新执行 cpa-manager-plus initialize 获取最新 Handoff 链接。',
  errorRejectedOtherDetail:
    '当前 Bootstrap 会话授权已失效，或该操作在当前阶段已关闭。请点击“同步服务端状态”检查最新进度，或在宿主机重新执行初始化命令。',
  errorMismatchTitle: '安装实例身份不匹配（Bootstrap Authority Mismatch）',
  errorMismatchDetail:
    '服务端返回的 FreshOperation 身份（admissionId / installationId / generation / operationId）与 Handoff 凭证不一致，已按 Fail-Closed 策略中止操作。',
  errorBadRequestTitle: '请求参数未通过服务端校验（HTTP 400）',
  errorBadRequestDetail:
    '管理员密钥格式、字节长度或字符复杂度不符合服务端安全要求，请检查密钥规则或使用“一键生成”重新生成。',
  errorConflictTitle: '操作状态冲突（HTTP 409）',
  errorConflictDetail:
    '服务端 FreshOperation 状态已发生变更（例如管理员已完成初始化或正在使用不同幂等标识提交）。请点击“同步服务端状态”获取最新状态。',
  errorServerTitle: '服务端暂时不可用',
  errorServerTitleStatus: '服务端暂时不可用（HTTP {status}）',
  errorServerDetail: 'Bootstrap 服务端未能完成当前请求，请稍后重试或同步服务端状态。',
  errorUnexpectedTitle: '操作未能完成',
  errorUnexpectedDetail: '发生未预期的客户端异常，请刷新服务端状态或重新获取 Handoff 链接。',
  gateTagBootstrap: 'Bootstrap Gate · 需要安装主机授权',
  gateTagPreflight: 'Source Preflight · 只读来源检查',
  gateHostTitle: '当前来源需要宿主机预检，不能直接 Fresh 初始化',
  gateUnconfirmedTitle: '来源状态无法确认，禁止直接初始化',
  gateCheckingTitle: '正在确认来源状态，禁止直接初始化',
  gateHostDesc:
    '公共 Discovery 仅提供来源类型提示，不读取或修改现有数据；后续操作必须在受信宿主机执行。',
  gateUnconfirmedDesc:
    '尚未取得可信的 Fresh 来源分类；请检查宿主机上的来源与服务状态，不能依据当前页面执行初始化或迁移。',
  preflightTitle: '下一步：在宿主机执行只读来源预检',
  preflightDesc:
    '把路径替换为真实来源数据库路径。此命令不会授权迁移，且不要在来源写入仍运行时声称已停止。',
  handoffHelpTitle: '如何获取或恢复 Bootstrap 授权链接？',
  handoffHelpLead: '请在受信宿主机运行初始化命令，并使用输出的带 ',
  handoffHelpTail: ' 片段的完整 URL 重新打开本页面：',
  unconfirmedTitle: '来源状态无法确认',
  checkingTitle: '正在读取来源状态',
  unconfirmedDesc:
    '不提供 Fresh 初始化命令；请在受信宿主机确认来源、日志与服务状态。未知结果不会授予任何写入权限。',
  recheck: '重新检查来源状态',
  diagnosticsMode: '授权模式',
  diagnosticsModeResume: '恢复会话（GET 操作）',
  diagnosticsModeClaim: '认领交换（POST claim/exchange）',
  diagnosticsOperationId: '操作 ID',
  diagnosticsInstallation: '安装实例 / 代际',
  authorityFailedTag: 'Bootstrap 授权建立失败',
  authorityFailedTitle: '无法建立 Fresh Bootstrap 受控会话',
  authorityFailedDesc:
    '客户端已完成地址栏敏感片段清理，但与 Manager Bootstrap 后端交换或恢复授权时未通过验证。',
  claimGeneration: '认领代际',
  handoffMode: 'Handoff 模式',
  handoffModeResume: '恢复',
  handoffModeFresh: '全新认领',
  reconnectNote:
    '可尝试重新连接一次；若返回 HTTP 401（令牌已被消费），请在宿主机重新运行 cpa-manager-plus initialize 获取新 Handoff',
  retryBootstrap: '重试连接 Bootstrap 服务',
  languageSwitch: '语言',
  languageZhCN: '简体中文',
  languageZhTW: '繁體中文',
  languageEnglish: 'English',
  languageRussian: 'Русский',
};
const tw: typeof en = {
  guidanceFreshTitle: '偵測到需要主機認領的 Fresh 初始化狀態',
  guidanceFreshDetail:
    '此狀態僅為唯讀識別結果。請從受信主機取得一次性 Handoff 連結；本頁面不會自行建立或認領安裝。',
  guidanceUpgradeTitle: '偵測到尚未取得升級資格的現有來源',
  guidanceUpgradeDetail:
    '只能在受信主機檢查原始來源。目前無法進入 Fresh 初始化或 Upgrade 切換；預檢成功也不代表取得匯入或覆寫授權。',
  guidanceRecoveryTitle: '偵測到需要主機介入的恢復狀態',
  guidanceRecoveryDetail:
    '來源不具備直接初始化資格。請先在受信主機進行唯讀預檢，保護資料庫、data.key 和現有備份；不得從此頁面嘗試 Fresh/Adopt/Upgrade 寫入。',
  guidanceUnavailableTitle: '來源狀態暫時無法確認',
  guidanceUnavailableDetail:
    '來源唯讀檢查未取得可信分類。請在受信主機排查；不能根據此狀態認定為 Fresh，也不能據此執行初始化或遷移。',
  errorTransportTitle: '網路連線異常（Bootstrap Transport Unavailable）',
  errorTransportCommitDetail:
    '提交結果尚未確認，目前金鑰與冪等提交識別（commitId）已保留在記憶體中。請檢查網路或服務狀態後同步伺服端狀態；確認伺服端仍未初始化後才可重試。請勿重新整理頁面或更換金鑰。',
  errorTransportStartDetail:
    '無法連線至 Manager Bootstrap 伺服端。您可嘗試重新連線一次；若伺服端已消費本次一次性 Claim Token 並傳回 HTTP 401，請在主機重新執行 cpa-manager-plus initialize 取得新的 Handoff 連結。',
  errorTransportOtherDetail:
    '無法連線至 Manager Bootstrap 伺服端，請確認本機受控容器或服務程序正在執行後重試。',
  errorRejectedTitle: 'Bootstrap 授權已失效或被拒絕（HTTP 401）',
  errorRejectedStartDetail:
    '一次性認領權杖（Claim Token）已被消費、過期，或被更新的初始化世代（Claim Generation）取代。請在主機重新執行 cpa-manager-plus initialize 取得最新 Handoff 連結。',
  errorRejectedOtherDetail:
    '目前 Bootstrap 工作階段授權已失效，或該操作在目前階段已關閉。請同步伺服端狀態檢查最新進度，或在主機重新執行初始化命令。',
  errorMismatchTitle: '安裝實例身分不匹配（Bootstrap Authority Mismatch）',
  errorMismatchDetail:
    '伺服端傳回的 FreshOperation 身分（admissionId / installationId / generation / operationId）與 Handoff 憑證不一致，已依 Fail-Closed 策略中止操作。',
  errorBadRequestTitle: '請求參數未通過伺服端驗證（HTTP 400）',
  errorBadRequestDetail:
    '管理員金鑰格式、位元組長度或字元複雜度不符合伺服端安全要求，請檢查金鑰規則或使用「一鍵產生」重新產生。',
  errorConflictTitle: '操作狀態衝突（HTTP 409）',
  errorConflictDetail:
    '伺服端 FreshOperation 狀態已變更（例如管理員已完成初始化或正在使用不同冪等識別提交）。請同步伺服端狀態取得最新狀態。',
  errorServerTitle: '伺服端暫時無法使用',
  errorServerTitleStatus: '伺服端暫時無法使用（HTTP {status}）',
  errorServerDetail: 'Bootstrap 伺服端未能完成目前請求，請稍後重試或同步伺服端狀態。',
  errorUnexpectedTitle: '操作未能完成',
  errorUnexpectedDetail: '發生未預期的用戶端異常，請重新整理伺服端狀態或重新取得 Handoff 連結。',
  gateTagBootstrap: 'Bootstrap Gate · 需要安裝主機授權',
  gateTagPreflight: 'Source Preflight · 唯讀來源檢查',
  gateHostTitle: '目前來源需要主機預檢，不能直接 Fresh 初始化',
  gateUnconfirmedTitle: '來源狀態無法確認，禁止直接初始化',
  gateCheckingTitle: '正在確認來源狀態，禁止直接初始化',
  gateHostDesc:
    '公開 Discovery 僅提供來源類型提示，不讀取或修改現有資料；後續操作必須在受信主機執行。',
  gateUnconfirmedDesc:
    '尚未取得可信的 Fresh 來源分類；請檢查主機上的來源與服務狀態，不能依據目前頁面執行初始化或遷移。',
  preflightTitle: '下一步：在主機執行唯讀來源預檢',
  preflightDesc:
    '把路徑替換為真實來源資料庫路徑。此命令不會授權遷移，且不要在來源寫入仍在執行時聲稱已停止。',
  handoffHelpTitle: '如何取得或恢復 Bootstrap 授權連結？',
  handoffHelpLead: '請在受信主機執行初始化命令，並使用輸出的帶 ',
  handoffHelpTail: ' 片段的完整 URL 重新開啟本頁面：',
  unconfirmedTitle: '來源狀態無法確認',
  checkingTitle: '正在讀取來源狀態',
  unconfirmedDesc:
    '不提供 Fresh 初始化命令；請在受信主機確認來源、日誌與服務狀態。未知結果不會授予任何寫入權限。',
  recheck: '重新檢查來源狀態',
  diagnosticsMode: '授權模式',
  diagnosticsModeResume: '恢復工作階段（GET 操作）',
  diagnosticsModeClaim: '認領交換（POST claim/exchange）',
  diagnosticsOperationId: '操作 ID',
  diagnosticsInstallation: '安裝實例 / 世代',
  authorityFailedTag: 'Bootstrap 授權建立失敗',
  authorityFailedTitle: '無法建立 Fresh Bootstrap 受控工作階段',
  authorityFailedDesc:
    '用戶端已完成網址列敏感片段清理，但與 Manager Bootstrap 後端交換或恢復授權時未通過驗證。',
  claimGeneration: '認領世代',
  handoffMode: 'Handoff 模式',
  handoffModeResume: '恢復',
  handoffModeFresh: '全新認領',
  reconnectNote:
    '可嘗試重新連線一次；若傳回 HTTP 401（權杖已被消費），請在主機重新執行 cpa-manager-plus initialize 取得新的 Handoff',
  retryBootstrap: '重試連線 Bootstrap 服務',
  languageSwitch: '語言',
  languageZhCN: '简体中文',
  languageZhTW: '繁體中文',
  languageEnglish: 'English',
  languageRussian: 'Русский',
};
const ru: typeof en = {
  guidanceFreshTitle: 'Обнаружено состояние инициализации Fresh, требующее Claim на хосте',
  guidanceFreshDetail:
    'Это результат только для чтения. Получите одноразовую ссылку Handoff на доверенном хосте; эта страница сама не создаёт установку и не выполняет Claim.',
  guidanceUpgradeTitle: 'Обнаружен существующий источник без права на обновление',
  guidanceUpgradeDetail:
    'Проверить исходный источник можно только на доверенном хосте. Инициализация Fresh и переключение Upgrade здесь недоступны; успешная предварительная проверка не даёт права на импорт или перезапись.',
  guidanceRecoveryTitle: 'Обнаружено состояние восстановления, требующее вмешательства хоста',
  guidanceRecoveryDetail:
    'Источник не подходит для прямой инициализации. Сначала выполните проверку только для чтения на доверенном хосте и защитите базу данных, data.key и существующие резервные копии; не пытайтесь выполнять записи Fresh/Adopt/Upgrade с этой страницы.',
  guidanceUnavailableTitle: 'Состояние источника пока не удаётся подтвердить',
  guidanceUnavailableDetail:
    'Проверка источника только для чтения не дала достоверной классификации. Разберитесь на доверенном хосте; это состояние нельзя считать Fresh, и оно не даёт права на инициализацию или миграцию.',
  errorTransportTitle: 'Сбой сети (Bootstrap Transport Unavailable)',
  errorTransportCommitDetail:
    'Результат отправки не подтверждён: текущий ключ и идемпотентный идентификатор отправки (commitId) сохранены в памяти. Проверьте сеть или службу и синхронизируйте состояние сервера; повторяйте попытку только после того, как сервер подтвердит, что инициализация не выполнена. Не перезагружайте страницу и не меняйте ключ.',
  errorTransportStartDetail:
    'Не удалось подключиться к серверу Manager Bootstrap. Можно повторить подключение один раз; если сервер уже использовал одноразовый Claim Token и вернул HTTP 401, снова выполните cpa-manager-plus initialize на хосте, чтобы получить новую ссылку Handoff.',
  errorTransportOtherDetail:
    'Не удалось подключиться к серверу Manager Bootstrap. Убедитесь, что локальный управляемый контейнер или процесс службы запущен, и повторите попытку.',
  errorRejectedTitle: 'Авторизация Bootstrap истекла или отклонена (HTTP 401)',
  errorRejectedStartDetail:
    'Одноразовый токен Claim Token уже использован, истёк или заменён более новым поколением Claim Generation. Снова выполните cpa-manager-plus initialize на хосте, чтобы получить актуальную ссылку Handoff.',
  errorRejectedOtherDetail:
    'Авторизация текущего сеанса Bootstrap истекла или операция закрыта на текущем этапе. Синхронизируйте состояние сервера, чтобы проверить прогресс, или снова выполните команду инициализации на хосте.',
  errorMismatchTitle: 'Идентичность установки не совпадает (Bootstrap Authority Mismatch)',
  errorMismatchDetail:
    'Идентичность FreshOperation, полученная от сервера (admissionId / installationId / generation / operationId), не совпадает с учетными данными Handoff; операция остановлена по стратегии fail-closed.',
  errorBadRequestTitle: 'Параметры запроса не прошли проверку на сервере (HTTP 400)',
  errorBadRequestDetail:
    'Формат ключа администратора, длина в байтах или сложность символов не соответствуют требованиям безопасности сервера. Проверьте правила ключа или создайте новый ключ.',
  errorConflictTitle: 'Конфликт состояния операции (HTTP 409)',
  errorConflictDetail:
    'Состояние FreshOperation на сервере изменилось (например, администратор уже завершил настройку или отправка использовала другой ключ идемпотентности). Синхронизируйте состояние сервера, чтобы получить актуальное состояние.',
  errorServerTitle: 'Сервер временно недоступен',
  errorServerTitleStatus: 'Сервер временно недоступен (HTTP {status})',
  errorServerDetail:
    'Сервер Bootstrap не смог выполнить этот запрос. Повторите попытку позже или синхронизируйте состояние сервера.',
  errorUnexpectedTitle: 'Операция не завершена',
  errorUnexpectedDetail:
    'Произошла непредвиденная ошибка клиента. Обновите состояние сервера или получите новую ссылку Handoff.',
  gateTagBootstrap: 'Bootstrap Gate · Нужна авторизация хоста',
  gateTagPreflight: 'Source Preflight · Проверка источника только для чтения',
  gateHostTitle:
    'Этому источнику нужна проверка на хосте; прямая инициализация Fresh невозможна',
  gateUnconfirmedTitle: 'Состояние источника не подтверждено; прямая инициализация запрещена',
  gateCheckingTitle: 'Проверка состояния источника; прямая инициализация запрещена',
  gateHostDesc:
    'Публичный Discovery только подсказывает тип источника и не читает и не изменяет существующие данные; все дальнейшие действия должны выполняться на доверенном хосте.',
  gateUnconfirmedDesc:
    'Достоверная классификация Fresh не получена. Проверьте источник и состояние служб на хосте; по этой странице нельзя выполнять инициализацию или миграцию.',
  preflightTitle: 'Следующий шаг: выполните проверку источника только для чтения на хосте',
  preflightDesc:
    'Замените путь на реальный путь к базе данных источника. Эта команда не даёт права на миграцию; не утверждайте, что источник остановлен, пока в него ещё идёт запись.',
  handoffHelpTitle: 'Как получить или восстановить ссылку авторизации Bootstrap?',
  handoffHelpLead:
    'Выполните команду инициализации на доверенном хосте и откройте эту страницу заново по полному URL, содержащему фрагмент ',
  handoffHelpTail: ':',
  unconfirmedTitle: 'Состояние источника не подтверждено',
  checkingTitle: 'Чтение состояния источника',
  unconfirmedDesc:
    'Команда инициализации Fresh не предоставляется. Проверьте источник, журналы и состояние служб на доверенном хосте; неизвестный результат не даёт права на запись.',
  recheck: 'Проверить состояние источника снова',
  diagnosticsMode: 'Режим авторизации',
  diagnosticsModeResume: 'Возобновление сеанса (операция GET)',
  diagnosticsModeClaim: 'Обмен Claim (POST claim/exchange)',
  diagnosticsOperationId: 'ID операции',
  diagnosticsInstallation: 'Установка / поколение',
  authorityFailedTag: 'Сбой авторизации Bootstrap',
  authorityFailedTitle: 'Не удалось установить управляемый сеанс Fresh Bootstrap',
  authorityFailedDesc:
    'Клиент уже очистил конфиденциальный фрагмент из адресной строки, но Manager Bootstrap не подтвердил обмен или возобновление авторизации.',
  claimGeneration: 'Поколение Claim',
  handoffMode: 'Режим Handoff',
  handoffModeResume: 'Возобновление',
  handoffModeFresh: 'Новый Claim',
  reconnectNote:
    'Можно повторить подключение один раз. Если вернётся HTTP 401 (токен уже использован), снова выполните cpa-manager-plus initialize на хосте, чтобы получить новый Handoff.',
  retryBootstrap: 'Повторить подключение к службе Bootstrap',
  languageSwitch: 'Язык',
  languageZhCN: '简体中文',
  languageZhTW: '繁體中文',
  languageEnglish: 'English',
  languageRussian: 'Русский',
};

export type FreshBootstrapTextKey = keyof typeof en;

export const bootstrapText = (
  key: FreshBootstrapTextKey,
  vars?: Record<string, string | number>,
  language = fullWebLanguage()
): string => {
  const table =
    language === 'zh-TW' ? tw : language.startsWith('zh') ? zh : language === 'ru' ? ru : en;
  let value = table[key];
  if (vars) {
    for (const [name, replacement] of Object.entries(vars)) {
      value = value.split(`{${name}}`).join(String(replacement));
    }
  }
  return value;
};
