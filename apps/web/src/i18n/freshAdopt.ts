// Adopt copy, in all four languages, in one module.
//
// It is a TS module rather than locale JSON because it is wizard chrome, not
// Full Web workspace copy: `consoleShell.ts` owns the wizard's shared words and
// this file owns the ones only an adoption says. Chinese is the primary text, so
// `zh` and `tw` are real translations rather than an English fallback.
import { fullWebLanguage } from './fullweb';

const en = {
  title: 'Adopt an existing CPA',
  help: 'Copy the configuration of another CPA into the CPA this Manager runs. The source CPA is only read, never written.',
  sourceTitle: 'Source CPA',
  sourceHelp:
    'Enter the address and management key of the CPA you are adopting from. It must be CPA v8.0.13 with remote management enabled.',
  baseURL: 'Source address',
  baseURLHelp: 'For example http://cpa.internal:8317. The destination is always this Manager.',
  managementKey: 'Source management key',
  managementKeyHelp:
    'Used only to read the source and to verify this attempt. It stays in this page’s memory and is never saved.',
  plugins: 'Enable the plugin runtime on the destination',
  pluginsHelp:
    'Off by default. The source plugin configuration is never copied; this only decides whether the new CPA runs plugins.',
  scopeTitle: 'What migrates',
  scopeMigrate:
    'Credentials, gateway configuration, provider sections and client keys are copied to the destination.',
  scopeKeep: 'Usage history, logs and plugins stay behind. The source CPA is never modified.',
  readInventory: 'Read the source inventory',
  inventoryTitle: 'Review the inventory',
  inventoryHelp:
    'Nothing has been written to the destination yet. Confirming copies these resources to the CPA this Manager runs.',
  migrateGroup: 'Copies to the destination',
  manualGroup: 'Needs manual work',
  unsupportedGroup: 'Cannot be migrated',
  confirmInventory: 'I reviewed the inventory and want to copy it to this CPA.',
  confirmAndMigrate: 'Confirm and migrate',
  migrationTitle: 'Migration',
  migrationHelp:
    'The destination is committed. The copy continues in the background and this page polls its recorded progress.',
  waiting: 'Reading the migration record…',
  progress: '{settled} of {planned} items settled',
  migratedGroup: 'Migrated',
  manualBadge: 'Needs manual work',
  failedGroup: 'Failed',
  retryableBadge: 'Retryable',
  absentBadge: 'Not set at the source',
  resumeTitle: 'Resume the migration',
  resumeHelp:
    'A run that stops can continue from the items it already recorded. The source key is not kept in this browser, so enter it again.',
  resumeKeyNotice:
    'Enter the source address and the same management key. The plugin choice was recorded with the attempt and cannot change.',
  resume: 'Resume migration',
  complete: 'Complete the installation',
  completeHelp: 'Completion proves the destination CPA and starts the usage collector.',
  completing: 'Completing…',
  runningRefusal: 'The migration is still running. Progress will refresh automatically.',
  incompleteRefusal: 'The migration has not processed every planned item yet.',
  retryableRefusal:
    '{count} item(s) could not be copied. Resume the migration, or finish anyway and handle them on the destination.',
  acceptRetryable: 'Finish anyway; I will handle the failed items myself.',
  unprovenRefusal:
    'The destination CPA could not be proven with the management key this Manager installed. Retry once the CPA is ready.',
  done: 'Adoption complete',
  doneHelp: 'The console is ready.',
  // The settled-but-incomplete state: the run stopped, but the installation is
  // not complete. The title never says "complete", and the body names what is
  // left instead.
  settledTitle: 'Migration settled',
  settledHelp:
    'The copy has stopped before the installation was complete. Resume the migration to finish what is left.',
  remainingItems: '{count} planned item(s) were never processed.',
  retryProgress: 'Retry the progress read',
  progressUnavailable:
    'The migration record could not be read, so this page stopped polling. Check the Manager and try again.',
  refresh: 'Check the status',
  unknownHelp:
    'The reply was lost, so the write may or may not have been applied. Check the status before trying again.',
  failure: 'The adoption is paused',
  reasonFallback: 'This item needs attention on the destination CPA.',
  details: 'Server instruction',
  kindCredential: 'Credentials',
  kindConfigLeaf: 'Gateway settings',
  kindConfigFamily: 'Payload rules',
  kindProviderSection: 'Provider sections',
  pluginConflict:
    'This attempt recorded a different plugin choice. Reload the page and resume with the recorded choice.',
  keyMismatch:
    'The source key does not match this attempt. Enter the same key you used for the inventory.',
};

const zh: typeof en = {
  title: '接管现有 CPA',
  help: '把另一个 CPA 的配置复制到本管理程序运行的 CPA。源 CPA 只会被读取，绝不会被写入。',
  sourceTitle: '源 CPA',
  sourceHelp:
    '填写要接管来源 CPA 的地址与管理密钥。该 CPA 必须是已开启远程管理、版本为 v8.0.13 的 CPA。',
  baseURL: '源地址',
  baseURLHelp: '例如 http://cpa.internal:8317。目标始终是本管理程序运行的 CPA。',
  managementKey: '源管理密钥',
  managementKeyHelp: '仅用于读取源 CPA 与校验本次操作。只保留在当前页面内存中，不会被保存。',
  plugins: '在目标启用插件运行时',
  pluginsHelp: '默认关闭。源的插件配置不会被复制，此项只决定新 CPA 是否运行插件。',
  scopeTitle: '迁移范围',
  scopeMigrate: '凭证、网关配置、提供商配置段与客户端密钥会复制到目标。',
  scopeKeep: '用量历史、日志和插件不会迁移。源 CPA 不会被修改。',
  readInventory: '读取源清单',
  inventoryTitle: '确认迁移清单',
  inventoryHelp: '此时尚未写入目标。确认后才会把这些资源复制到本管理程序运行的 CPA。',
  migrateGroup: '将复制到目标',
  manualGroup: '需要手动处理',
  unsupportedGroup: '无法迁移',
  confirmInventory: '我已确认清单，并同意复制到本 CPA。',
  confirmAndMigrate: '确认并开始迁移',
  migrationTitle: '正在迁移',
  migrationHelp: '目标已提交。复制在后台继续，本页会轮询已记录的进度。',
  waiting: '正在读取迁移记录…',
  progress: '已处理 {settled} / {planned} 项',
  migratedGroup: '已迁移',
  manualBadge: '需要手动处理',
  failedGroup: '失败',
  retryableBadge: '可重试',
  absentBadge: '源中未设置',
  resumeTitle: '继续迁移',
  resumeHelp: '中断的迁移会从已记录的条目继续。源密钥不会保存在浏览器中，请重新输入。',
  resumeKeyNotice: '请重新输入源地址与同一个管理密钥。插件选项已随本次操作记录，不能再更改。',
  resume: '继续迁移',
  complete: '完成安装',
  completeHelp: '完成时会校验目标 CPA 并启动用量采集。',
  completing: '正在完成…',
  runningRefusal: '迁移仍在进行，进度会自动刷新。',
  incompleteRefusal: '迁移尚未处理完计划中的全部条目。',
  retryableRefusal: '有 {count} 项未能复制。可以继续迁移重试，或选择仍然完成，稍后在目标手动处理。',
  acceptRetryable: '仍然完成，我会自行处理失败项。',
  unprovenRefusal: '无法用本管理程序安装的管理密钥校验目标 CPA。请等 CPA 就绪后重试。',
  done: '接管完成',
  doneHelp: '控制台已就绪。',
  settledTitle: '迁移已停止复制',
  settledHelp: '复制在安装完成前停止。可以继续迁移以处理剩余条目。',
  remainingItems: '有 {count} 项计划中的条目从未处理。',
  retryProgress: '重试读取进度',
  progressUnavailable: '无法读取迁移记录，本页已停止轮询。请检查管理程序后重试。',
  refresh: '检查状态',
  unknownHelp: '响应已丢失，本次写入可能已生效也可能没有。请先检查状态，再决定是否重试。',
  failure: '接管已暂停',
  reasonFallback: '该项需要在目标 CPA 手动处理。',
  details: '服务端说明',
  kindCredential: '凭证',
  kindConfigLeaf: '网关配置项',
  kindConfigFamily: '载荷规则',
  kindProviderSection: '提供商配置段',
  pluginConflict: '本次操作记录的插件选项不同。请刷新页面并按已记录的选项继续。',
  keyMismatch: '源密钥与本次操作不一致。请填入读取清单时使用的同一个密钥。',
};

const tw: typeof en = {
  title: '接管現有 CPA',
  help: '把另一個 CPA 的設定複製到本管理程式執行的 CPA。來源 CPA 只會被讀取，絕不會被寫入。',
  sourceTitle: '來源 CPA',
  sourceHelp:
    '填寫要接管來源 CPA 的位址與管理金鑰。該 CPA 必須是已開啟遠端管理、版本為 v8.0.13 的 CPA。',
  baseURL: '來源位址',
  baseURLHelp: '例如 http://cpa.internal:8317。目標一律是本管理程式執行的 CPA。',
  managementKey: '來源管理金鑰',
  managementKeyHelp: '僅用於讀取來源 CPA 與驗證本次操作。只保留在目前頁面的記憶體中，不會被儲存。',
  plugins: '在目標啟用外掛執行環境',
  pluginsHelp: '預設關閉。來源的外掛設定不會被複製，此選項只決定新 CPA 是否執行外掛。',
  scopeTitle: '遷移範圍',
  scopeMigrate: '憑證、閘道設定、供應商設定區段與用戶端金鑰會複製到目標。',
  scopeKeep: '用量歷史、日誌和外掛不會遷移。來源 CPA 不會被修改。',
  readInventory: '讀取來源清單',
  inventoryTitle: '確認遷移清單',
  inventoryHelp: '此時尚未寫入目標。確認後才會把這些資源複製到本管理程式執行的 CPA。',
  migrateGroup: '將複製到目標',
  manualGroup: '需要手動處理',
  unsupportedGroup: '無法遷移',
  confirmInventory: '我已確認清單，並同意複製到本 CPA。',
  confirmAndMigrate: '確認並開始遷移',
  migrationTitle: '正在遷移',
  migrationHelp: '目標已提交。複製會在背景繼續，本頁會輪詢已記錄的進度。',
  waiting: '正在讀取遷移記錄…',
  progress: '已處理 {settled} / {planned} 項',
  migratedGroup: '已遷移',
  manualBadge: '需要手動處理',
  failedGroup: '失敗',
  retryableBadge: '可重試',
  absentBadge: '來源未設定',
  resumeTitle: '繼續遷移',
  resumeHelp: '中斷的遷移會從已記錄的項目繼續。來源金鑰不會保存在瀏覽器中，請重新輸入。',
  resumeKeyNotice: '請重新輸入來源位址與同一個管理金鑰。外掛選項已隨本次操作記錄，不能再變更。',
  resume: '繼續遷移',
  complete: '完成安裝',
  completeHelp: '完成時會驗證目標 CPA 並啟動用量收集。',
  completing: '正在完成…',
  runningRefusal: '遷移仍在進行，進度會自動重新整理。',
  incompleteRefusal: '遷移尚未處理完計畫中的全部項目。',
  retryableRefusal: '有 {count} 項未能複製。可以繼續遷移重試，或選擇仍然完成，稍後在目標手動處理。',
  acceptRetryable: '仍然完成，我會自行處理失敗項目。',
  unprovenRefusal: '無法用本管理程式安裝的管理金鑰驗證目標 CPA。請等 CPA 就緒後重試。',
  done: '接管完成',
  doneHelp: '主控台已就緒。',
  settledTitle: '遷移已停止複製',
  settledHelp: '複製在安裝完成前停止。可以繼續遷移以處理剩餘項目。',
  remainingItems: '有 {count} 項計畫中的項目從未處理。',
  retryProgress: '重試讀取進度',
  progressUnavailable: '無法讀取遷移記錄，本頁已停止輪詢。請檢查管理程式後重試。',
  refresh: '檢查狀態',
  unknownHelp: '回應已遺失，本次寫入可能已生效也可能沒有。請先檢查狀態，再決定是否重試。',
  failure: '接管已暫停',
  reasonFallback: '該項目需要在目標 CPA 手動處理。',
  details: '伺服器說明',
  kindCredential: '憑證',
  kindConfigLeaf: '閘道設定項',
  kindConfigFamily: '載荷規則',
  kindProviderSection: '供應商設定區段',
  pluginConflict: '本次操作記錄的外掛選項不同。請重新整理頁面並依已記錄的選項繼續。',
  keyMismatch: '來源金鑰與本次操作不一致。請填入讀取清單時使用的同一個金鑰。',
};

const ru: typeof en = {
  title: 'Подключить существующий CPA',
  help: 'Скопировать конфигурацию другого CPA в CPA, которым управляет этот Manager. Исходный CPA только читается и никогда не изменяется.',
  sourceTitle: 'Исходный CPA',
  sourceHelp:
    'Укажите адрес и ключ управления CPA, который вы подключаете. Это должен быть CPA v8.0.13 с включённым удалённым управлением.',
  baseURL: 'Адрес источника',
  baseURLHelp: 'Например http://cpa.internal:8317. Целевым всегда является CPA этого Manager.',
  managementKey: 'Ключ управления источника',
  managementKeyHelp:
    'Используется только для чтения источника и проверки этой попытки. Хранится в памяти страницы и не сохраняется.',
  plugins: 'Включить среду плагинов на целевом CPA',
  pluginsHelp:
    'По умолчанию выключено. Конфигурация плагинов источника не копируется; это только решает, запускать ли плагины на новом CPA.',
  scopeTitle: 'Что переносится',
  scopeMigrate:
    'Учётные данные, конфигурация шлюза, разделы провайдеров и клиентские ключи копируются в целевой CPA.',
  scopeKeep:
    'История использования, журналы и плагины остаются на источнике. Исходный CPA не изменяется.',
  readInventory: 'Прочитать состав источника',
  inventoryTitle: 'Проверьте состав переноса',
  inventoryHelp:
    'В целевой CPA пока ничего не записано. После подтверждения эти ресурсы будут скопированы в CPA этого Manager.',
  migrateGroup: 'Будет скопировано',
  manualGroup: 'Требует ручной работы',
  unsupportedGroup: 'Перенос невозможен',
  confirmInventory: 'Я проверил состав и хочу скопировать его в этот CPA.',
  confirmAndMigrate: 'Подтвердить и перенести',
  migrationTitle: 'Перенос',
  migrationHelp:
    'Целевой CPA зафиксирован. Копирование идёт в фоне, а страница опрашивает сохранённый прогресс.',
  waiting: 'Чтение записи о переносе…',
  progress: 'Обработано {settled} из {planned}',
  migratedGroup: 'Перенесено',
  manualBadge: 'Требует ручной работы',
  failedGroup: 'Ошибки',
  retryableBadge: 'Можно повторить',
  absentBadge: 'Нет в источнике',
  resumeTitle: 'Продолжить перенос',
  resumeHelp:
    'Прерванный перенос продолжается с уже записанных элементов. Ключ источника не хранится в браузере, поэтому введите его снова.',
  resumeKeyNotice:
    'Введите адрес источника и тот же ключ управления. Выбор плагинов уже записан и не может быть изменён.',
  resume: 'Продолжить перенос',
  complete: 'Завершить установку',
  completeHelp: 'При завершении целевой CPA проверяется и запускается сбор статистики.',
  completing: 'Завершение…',
  runningRefusal: 'Перенос ещё идёт. Прогресс обновится автоматически.',
  incompleteRefusal: 'Перенос ещё не обработал все запланированные элементы.',
  retryableRefusal:
    'Не удалось скопировать элементы: {count}. Продолжите перенос или завершите установку и обработайте их на целевом CPA.',
  acceptRetryable: 'Всё равно завершить; ошибки я обработаю сам.',
  unprovenRefusal:
    'Не удалось подтвердить целевой CPA ключом управления, установленным этим Manager. Повторите, когда CPA будет готов.',
  done: 'Подключение завершено',
  doneHelp: 'Консоль готова.',
  settledTitle: 'Перенос остановлен',
  settledHelp:
    'Копирование остановилось до завершения установки. Продолжите перенос, чтобы обработать оставшееся.',
  remainingItems: 'Не обработано запланированных элементов: {count}.',
  retryProgress: 'Повторить чтение прогресса',
  progressUnavailable:
    'Запись о переносе не читается, поэтому страница перестала опрашивать. Проверьте Manager и повторите.',
  refresh: 'Проверить состояние',
  unknownHelp:
    'Ответ потерян: запись могла примениться, а могла и нет. Проверьте состояние, прежде чем повторять.',
  failure: 'Подключение приостановлено',
  reasonFallback: 'Этот элемент требует внимания на целевом CPA.',
  details: 'Пояснение сервера',
  kindCredential: 'Учётные данные',
  kindConfigLeaf: 'Настройки шлюза',
  kindConfigFamily: 'Правила нагрузки',
  kindProviderSection: 'Разделы провайдеров',
  pluginConflict:
    'Для этой попытки записан другой выбор плагинов. Перезагрузите страницу и продолжите с записанным выбором.',
  keyMismatch:
    'Ключ источника не совпадает с этой попыткой. Введите тот же ключ, что и при чтении состава.',
};

// The source inventory references. Each one is a closed, product-owned name, so
// the inventory can name a resource without trusting anything the source said.
const enResources: Record<string, string> = {
  'client-api-keys': 'Client API keys',
  'cpa-auth-files': 'Credential files',
  'cpa-config-server': 'Server settings',
  'cpa-config-management': 'Management settings',
  'cpa-config-routing': 'Routing settings',
  'cpa-config-logging': 'Logging settings',
  'cpa-config-usage': 'Usage statistics settings',
  'cpa-config-plugins': 'Plugin settings',
  'cpa-config-observability': 'Observability settings',
  'cpa-config-unknown': 'Other configuration',
  'provider-state-providers': 'Provider runtime state',
  'provider-state-api-keys': 'Provider keys',
  'provider-state-client': 'Provider client state',
};
const zhResources: Record<string, string> = {
  'client-api-keys': '客户端 API 密钥',
  'cpa-auth-files': '凭证文件',
  'cpa-config-server': '服务设置',
  'cpa-config-management': '管理设置',
  'cpa-config-routing': '路由设置',
  'cpa-config-logging': '日志设置',
  'cpa-config-usage': '用量统计设置',
  'cpa-config-plugins': '插件设置',
  'cpa-config-observability': '可观测性设置',
  'cpa-config-unknown': '其他配置',
  'provider-state-providers': '提供商运行时状态',
  'provider-state-api-keys': '提供商密钥',
  'provider-state-client': '提供商客户端状态',
};
const twResources: Record<string, string> = {
  'client-api-keys': '用戶端 API 金鑰',
  'cpa-auth-files': '憑證檔案',
  'cpa-config-server': '伺服器設定',
  'cpa-config-management': '管理設定',
  'cpa-config-routing': '路由設定',
  'cpa-config-logging': '日誌設定',
  'cpa-config-usage': '用量統計設定',
  'cpa-config-plugins': '外掛設定',
  'cpa-config-observability': '可觀測性設定',
  'cpa-config-unknown': '其他設定',
  'provider-state-providers': '供應商執行時狀態',
  'provider-state-api-keys': '供應商金鑰',
  'provider-state-client': '供應商用戶端狀態',
};
const ruResources: Record<string, string> = {
  'client-api-keys': 'Клиентские API-ключи',
  'cpa-auth-files': 'Файлы учётных данных',
  'cpa-config-server': 'Настройки сервера',
  'cpa-config-management': 'Настройки управления',
  'cpa-config-routing': 'Настройки маршрутизации',
  'cpa-config-logging': 'Настройки журналов',
  'cpa-config-usage': 'Настройки статистики',
  'cpa-config-plugins': 'Настройки плагинов',
  'cpa-config-observability': 'Настройки наблюдаемости',
  'cpa-config-unknown': 'Прочая конфигурация',
  'provider-state-providers': 'Состояние провайдеров',
  'provider-state-api-keys': 'Ключи провайдеров',
  'provider-state-client': 'Состояние клиента провайдера',
};

// Per-item reasons. Every code the executor records has a sentence; an unknown
// one falls back to the generic line rather than showing a raw token.
const enReasons: Record<string, string> = {
  runtime_only_credential:
    'This credential exists only in the source process and has no file. Re-authenticate it on the destination.',
  not_authorized: 'The current qualification does not allow this resource to migrate.',
  manager_owned: 'This setting belongs to the Manager and is never copied.',
  value_shape_unsupported: 'The source value has a shape this CPA cannot copy.',
  path_unresolved: 'This setting could not be located on the destination.',
  payload_unresolved: 'The destination payload rules could not be read.',
  not_uploadable: 'The destination refused this credential file.',
  download_failed: 'The source did not return this item. Resume the migration to retry it.',
  source_unreadable: 'This item could not be read from the source.',
  type_diverged: 'The destination stores this item with a different type.',
  status_write_failed: 'The item was copied but its enabled state could not be written.',
  status_unverified: 'The item was copied but its state could not be verified.',
  verify_mismatch: 'The destination does not hold the value that was written.',
  family_unresolved: 'This payload rule family could not be resolved.',
  adoption_item_name_too_long: 'This item’s name is too long to record.',
  adoption_inventory_too_large: 'The source holds more items than one run can record.',
  reauthenticate_after_adoption: 'Re-authenticate this provider on the destination.',
  reconfigure_after_adoption: 'Reconfigure this resource on the destination.',
};
const zhReasons: Record<string, string> = {
  runtime_only_credential: '该凭证只存在于源进程中，没有对应文件。请在目标重新认证。',
  not_authorized: '当前资格不允许迁移该资源。',
  manager_owned: '该设置由管理器自己维护，不会被复制。',
  value_shape_unsupported: '源值的结构无法被本 CPA 复制。',
  path_unresolved: '在目标上找不到对应设置。',
  payload_unresolved: '无法读取目标的载荷规则。',
  not_uploadable: '目标拒绝了该凭证文件。',
  download_failed: '源未返回该项。可继续迁移重试。',
  source_unreadable: '无法从源读取该项。',
  type_diverged: '目标以不同类型存储该项。',
  status_write_failed: '已复制该项，但无法写入其启用状态。',
  status_unverified: '已复制该项，但无法校验其状态。',
  verify_mismatch: '目标上保存的值与写入值不一致。',
  family_unresolved: '无法解析该载荷规则族。',
  adoption_item_name_too_long: '该项名称过长，无法记录。',
  adoption_inventory_too_large: '源条目超过单次运行可记录的上限。',
  reauthenticate_after_adoption: '请在目标重新认证该提供商。',
  reconfigure_after_adoption: '请在目标重新配置该资源。',
};
const twReasons: Record<string, string> = {
  runtime_only_credential: '該憑證只存在於來源程序，沒有對應檔案。請在目標重新認證。',
  not_authorized: '目前資格不允許遷移該資源。',
  manager_owned: '該設定由管理器自行維護，不會被複製。',
  value_shape_unsupported: '來源值的結構無法被本 CPA 複製。',
  path_unresolved: '在目標上找不到對應設定。',
  payload_unresolved: '無法讀取目標的載荷規則。',
  not_uploadable: '目標拒絕了該憑證檔案。',
  download_failed: '來源未回傳該項目。可繼續遷移重試。',
  source_unreadable: '無法從來源讀取該項目。',
  type_diverged: '目標以不同型別儲存該項目。',
  status_write_failed: '已複製該項目，但無法寫入其啟用狀態。',
  status_unverified: '已複製該項目，但無法驗證其狀態。',
  verify_mismatch: '目標上保存的值與寫入值不一致。',
  family_unresolved: '無法解析該載荷規則族。',
  adoption_item_name_too_long: '該項目名稱過長，無法記錄。',
  adoption_inventory_too_large: '來源項目超過單次執行可記錄的上限。',
  reauthenticate_after_adoption: '請在目標重新認證該供應商。',
  reconfigure_after_adoption: '請在目標重新設定該資源。',
};
const ruReasons: Record<string, string> = {
  runtime_only_credential:
    'Эти данные существуют только в процессе источника и не имеют файла. Повторно авторизуйтесь на целевом CPA.',
  not_authorized: 'Текущая квалификация не разрешает перенос этого ресурса.',
  manager_owned: 'Эта настройка принадлежит Manager и не копируется.',
  value_shape_unsupported: 'Форма значения источника не может быть скопирована этим CPA.',
  path_unresolved: 'Эта настройка не найдена на целевом CPA.',
  payload_unresolved: 'Не удалось прочитать правила нагрузки целевого CPA.',
  not_uploadable: 'Целевой CPA отказался принять этот файл учётных данных.',
  download_failed: 'Источник не вернул этот элемент. Продолжите перенос, чтобы повторить.',
  source_unreadable: 'Не удалось прочитать этот элемент из источника.',
  type_diverged: 'Целевой CPA хранит этот элемент с другим типом.',
  status_write_failed: 'Элемент скопирован, но состояние включения записать не удалось.',
  status_unverified: 'Элемент скопирован, но состояние проверить не удалось.',
  verify_mismatch: 'Целевой CPA не содержит записанного значения.',
  family_unresolved: 'Не удалось разрешить это семейство правил нагрузки.',
  adoption_item_name_too_long: 'Имя элемента слишком длинное для записи.',
  adoption_inventory_too_large: 'В источнике больше элементов, чем может записать один запуск.',
  reauthenticate_after_adoption: 'Повторно авторизуйте этого провайдера на целевом CPA.',
  reconfigure_after_adoption: 'Настройте этот ресурс на целевом CPA.',
};

// Strategy blockers. `adoption_source_read_unavailable` also covers a v7 or
// v0-only source, which is why its sentence names the upgrade.
const enBlockers: Record<string, string> = {
  adoption_source_read_unavailable:
    'The source CPA could not be read with this key, or it is not CPA v8. Upgrade it to v8 first.',
  adoption_source_empty:
    'The source CPA has no client keys to adopt. Add at least one client key on the source first.',
  adoption_destination_identity_conflict:
    'The source address is this Manager’s own CPA. Enter the CPA you are migrating from.',
  adoption_source_class_refused:
    'That source type is not supported here. Only a CPA v8 reachable over the network can be adopted.',
  adoption_source_qualification_required:
    'This build has not qualified the network source class. Use a qualified CPA v8.0.13 build.',
  adoption_resource_qualification_required:
    'This build has not qualified the resources Adopt copies. Use a qualified CPA v8.0.13 build.',
  exact_v8_artifact_qualification_required:
    'This build has no qualified CPA v8.0.13 artifact. Use a qualified runtime.',
  adoption_destination_staging_required:
    'This CPA cannot stage an adoption destination yet. Update the runtime and retry.',
  adoption_destination_stage_unresolved:
    'The destination CPA did not report the staging result. Retry once the CPA is available.',
  adoption_destination_stage_failed:
    'The destination CPA failed to start from the source snapshot. Retry the attempt.',
  adoption_destination_start_unresolved:
    'The destination CPA did not report the start result. Retry once the CPA is available.',
  adoption_destination_start_failed: 'The destination CPA failed to start. Retry the attempt.',
  adoption_destination_verification_required:
    'The destination CPA did not verify after the copy. Retry before completing.',
  adoption_snapshot_unavailable:
    'The recorded adoption snapshot could not be read. Retry the attempt.',
  adopt_plugin_optin_conflict:
    'This attempt recorded a different plugin choice. Reload the page and resume with the recorded choice.',
  management_connection_invalid: 'The source address is not a usable http or https URL.',
  management_secret_storage_unavailable:
    'The management key could not be stored securely. Check the Manager’s encryption key and storage.',
  managed_target_observation_unavailable:
    'This Manager could not observe the CPA it runs. Retry once the runtime is ready.',
  managed_target_identity_mismatch:
    'The CPA this Manager runs is not the one this attempt was started for. Retry from the source step.',
  fresh_strategy_owner_conflict:
    'This installation already owns a different strategy or source. Start a new installation.',
  fresh_strategy_authority_unavailable:
    'The installation session is no longer authorized. Reload the page and sign in again.',
  fresh_strategy_invalid: 'The installation refused this strategy.',
};
const zhBlockers: Record<string, string> = {
  adoption_source_read_unavailable: '无法用该密钥读取源 CPA，或它不是 CPA v8。请先升级到 v8。',
  adoption_source_empty: '源 CPA 没有可接管的客户端密钥。请先在源上添加至少一个密钥。',
  adoption_destination_identity_conflict:
    '源地址就是本管理程序自己的 CPA。请填写你要迁移的源 CPA。',
  adoption_source_class_refused: '不支持该来源类型。这里只能接管可通过网络访问的 CPA v8。',
  adoption_source_qualification_required:
    '当前构建尚未取得网络来源类别的资格。请使用已取得资格的 CPA v8.0.13 构建。',
  adoption_resource_qualification_required:
    '当前构建尚未取得接管所需资源的资格。请使用已取得资格的 CPA v8.0.13 构建。',
  exact_v8_artifact_qualification_required:
    '当前构建没有已取得资格的 CPA v8.0.13 制品。请使用已取得资格的运行时。',
  adoption_destination_staging_required: '该 CPA 目前无法准备接管目标。请更新运行时后重试。',
  adoption_destination_stage_unresolved: '目标 CPA 未返回准备结果。请等 CPA 可用后重试。',
  adoption_destination_stage_failed: '目标 CPA 无法从源快照启动。请重试本次操作。',
  adoption_destination_start_unresolved: '目标 CPA 未返回启动结果。请等 CPA 可用后重试。',
  adoption_destination_start_failed: '目标 CPA 启动失败。请重试本次操作。',
  adoption_destination_verification_required: '复制后未能校验目标 CPA。请重试后再完成。',
  adoption_snapshot_unavailable: '无法读取已记录的快照。请重试本次操作。',
  adopt_plugin_optin_conflict: '本次操作记录的插件选项不同。请刷新页面并按已记录的选项继续。',
  management_connection_invalid: '源地址不是可用的 http 或 https 地址。',
  management_secret_storage_unavailable: '无法安全保存管理密钥。请检查管理程序的加密密钥与存储。',
  managed_target_observation_unavailable: '本管理程序无法观测它运行的 CPA。请等运行时就绪后重试。',
  managed_target_identity_mismatch: '当前运行的 CPA 不是本次操作的目标。请从源信息步骤重新开始。',
  fresh_strategy_owner_conflict: '该安装已归属其他方式或来源。请新建一次安装。',
  fresh_strategy_authority_unavailable: '安装会话已失效。请刷新页面并重新登录。',
  fresh_strategy_invalid: '安装拒绝了该方式。',
};
const twBlockers: Record<string, string> = {
  adoption_source_read_unavailable: '無法用該金鑰讀取來源 CPA，或它不是 CPA v8。請先升級到 v8。',
  adoption_source_empty: '來源 CPA 沒有可接管的用戶端金鑰。請先在來源加入至少一個金鑰。',
  adoption_destination_identity_conflict:
    '來源位址就是本管理程式自己的 CPA。請填寫你要遷移的來源 CPA。',
  adoption_source_class_refused: '不支援該來源類型。此處只能接管可透過網路存取的 CPA v8。',
  adoption_source_qualification_required:
    '目前建置尚未取得網路來源類別的資格。請使用已取得資格的 CPA v8.0.13 建置。',
  adoption_resource_qualification_required:
    '目前建置尚未取得接管所需資源的資格。請使用已取得資格的 CPA v8.0.13 建置。',
  exact_v8_artifact_qualification_required:
    '目前建置沒有已取得資格的 CPA v8.0.13 產物。請使用已取得資格的執行環境。',
  adoption_destination_staging_required: '該 CPA 目前無法準備接管目標。請更新執行環境後重試。',
  adoption_destination_stage_unresolved: '目標 CPA 未回傳準備結果。請等 CPA 可用後重試。',
  adoption_destination_stage_failed: '目標 CPA 無法從來源快照啟動。請重試本次操作。',
  adoption_destination_start_unresolved: '目標 CPA 未回傳啟動結果。請等 CPA 可用後重試。',
  adoption_destination_start_failed: '目標 CPA 啟動失敗。請重試本次操作。',
  adoption_destination_verification_required: '複製後無法驗證目標 CPA。請重試後再完成。',
  adoption_snapshot_unavailable: '無法讀取已記錄的快照。請重試本次操作。',
  adopt_plugin_optin_conflict: '本次操作記錄的外掛選項不同。請重新整理頁面並依已記錄的選項繼續。',
  management_connection_invalid: '來源位址不是可用的 http 或 https 位址。',
  management_secret_storage_unavailable: '無法安全保存管理金鑰。請檢查管理程式的加密金鑰與儲存。',
  managed_target_observation_unavailable:
    '本管理程式無法觀測它執行的 CPA。請等執行環境就緒後重試。',
  managed_target_identity_mismatch: '目前執行的 CPA 不是本次操作的目標。請從來源資訊步驟重新開始。',
  fresh_strategy_owner_conflict: '該安裝已歸屬其他方式或來源。請新建一次安裝。',
  fresh_strategy_authority_unavailable: '安裝工作階段已失效。請重新整理頁面並重新登入。',
  fresh_strategy_invalid: '安裝拒絕了該方式。',
};
const ruBlockers: Record<string, string> = {
  adoption_source_read_unavailable:
    'Не удалось прочитать исходный CPA этим ключом, либо это не CPA v8. Сначала обновите его до v8.',
  adoption_source_empty:
    'В исходном CPA нет клиентских ключей для переноса. Добавьте хотя бы один ключ на источнике.',
  adoption_destination_identity_conflict:
    'Адрес источника совпадает с CPA этого Manager. Укажите CPA, с которого выполняется перенос.',
  adoption_source_class_refused:
    'Этот тип источника здесь не поддерживается. Перенести можно только CPA v8, доступный по сети.',
  adoption_source_qualification_required:
    'Эта сборка не квалифицировала сетевой класс источника. Используйте квалифицированную сборку CPA v8.0.13.',
  adoption_resource_qualification_required:
    'Эта сборка не квалифицировала ресурсы, которые переносит Adopt. Используйте квалифицированную сборку CPA v8.0.13.',
  exact_v8_artifact_qualification_required:
    'В этой сборке нет квалифицированного артефакта CPA v8.0.13. Используйте квалифицированную среду выполнения.',
  adoption_destination_staging_required:
    'Этот CPA пока не может подготовить целевой CPA. Обновите среду выполнения и повторите.',
  adoption_destination_stage_unresolved:
    'Целевой CPA не сообщил результат подготовки. Повторите, когда CPA будет доступен.',
  adoption_destination_stage_failed:
    'Целевому CPA не удалось запуститься из снимка источника. Повторите попытку.',
  adoption_destination_start_unresolved:
    'Целевой CPA не сообщил результат запуска. Повторите, когда CPA будет доступен.',
  adoption_destination_start_failed: 'Целевому CPA не удалось запуститься. Повторите попытку.',
  adoption_destination_verification_required:
    'Целевой CPA не прошёл проверку после копирования. Повторите перед завершением.',
  adoption_snapshot_unavailable:
    'Не удалось прочитать сохранённый снимок переноса. Повторите попытку.',
  adopt_plugin_optin_conflict:
    'Для этой попытки записан другой выбор плагинов. Перезагрузите страницу и продолжите с записанным выбором.',
  management_connection_invalid: 'Адрес источника не является рабочим URL http или https.',
  management_secret_storage_unavailable:
    'Не удалось безопасно сохранить ключ управления. Проверьте ключ шифрования и хранилище Manager.',
  managed_target_observation_unavailable:
    'Manager не видит CPA, которым управляет. Повторите, когда среда выполнения будет готова.',
  managed_target_identity_mismatch:
    'CPA, которым управляет Manager, не совпадает с целью этой попытки. Начните заново с шага источника.',
  fresh_strategy_owner_conflict:
    'Эта установка уже принадлежит другой стратегии или источнику. Начните новую установку.',
  fresh_strategy_authority_unavailable:
    'Сессия установки больше не авторизована. Перезагрузите страницу и войдите снова.',
  fresh_strategy_invalid: 'Установка отклонила эту стратегию.',
};

type Locale = 'en' | 'zh' | 'tw' | 'ru';

function locale(): Locale {
  const language = fullWebLanguage();
  return language === 'zh-TW'
    ? 'tw'
    : language.startsWith('zh')
      ? 'zh'
      : language === 'ru'
        ? 'ru'
        : 'en';
}

const copy: Record<Locale, typeof en> = { en, zh, tw, ru };
const resources: Record<Locale, Record<string, string>> = {
  en: enResources,
  zh: zhResources,
  tw: twResources,
  ru: ruResources,
};
const reasons: Record<Locale, Record<string, string>> = {
  en: enReasons,
  zh: zhReasons,
  tw: twReasons,
  ru: ruReasons,
};
const blockers: Record<Locale, Record<string, string>> = {
  en: enBlockers,
  zh: zhBlockers,
  tw: twBlockers,
  ru: ruBlockers,
};

/** adoptText reads one label, substituting `{name}` placeholders. */
export const adoptText = (
  key: keyof typeof en,
  values?: Record<string, string | number>
): string => {
  let value = copy[locale()][key];
  for (const [name, replacement] of Object.entries(values ?? {}))
    value = value.split(`{${name}}`).join(String(replacement));
  return value;
};

/** adoptReasonText names one per-item reason, or the generic line. */
export const adoptReasonText = (code: string): string =>
  reasons[locale()][code] || copy[locale()].reasonFallback;

/** adoptBlockerText explains one strategy refusal. */
export const adoptBlockerText = (code: string | undefined): string =>
  (code && blockers[locale()][code]) || copy[locale()].failure;

/** adoptResourceText names one bounded inventory reference. */
export const adoptResourceText = (ref: string): string => resources[locale()][ref] || ref;
