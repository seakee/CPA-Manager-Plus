import { fullWebLanguage } from './fullweb';

const en = {
  title: 'Upgrade to CPAMP v2',
  mode: 'Upgrade',
  verified: 'Administrator identity verified',
  retained: 'Your existing administrator key is retained. No new key is required.',
  blocked: 'Review the upgrade operation below.',
  boundary:
    'Authentication is complete. Backup, migration, CPA changes and management entry changes have not started.',
  recordedBlockedHelp:
    'Administrator identity verified. Continue from the recorded operation below; completed script steps are not repeated.',
  observationUnavailable: 'The source cannot be summarized publicly.',
  observationUnavailableHelp:
    'Administrator identity verified, but the source cannot be summarized publicly. The authenticated operation below determines the available upgrade or recovery steps.',
  recordedObservationHelp:
    'Administrator identity verified. The public source summary is unavailable; use the authenticated operation below to continue or recover.',
  next: '',
  verify: 'Check authentication again',
  logout: 'Return to login',
  checking: 'Verifying the existing administrator…',
  checkingShort: 'Verifying identity',
  checkingHelp:
    'Checking the current installation. Upgrade details and actions are unavailable until verification finishes.',
  recoveryLabel: 'Unable to use your original key?',
  recoveryHelp:
    'Keep the existing data and key files. Stop the Manager and recover the administrator key on its deployment host using tools for that installed version. Then return here to verify it. Do not initialize the installation again; online key reset is not available here.',
  footer: 'CPA Manager Plus · Installation and upgrade',
  invalid:
    'The existing Manager administrator key could not be verified. Enter it again. CPA management keys cannot be used here.',
  unavailable:
    'This source cannot be verified safely. Check the stopped installation on its host; do not initialize it again.',
  source_ownership_unavailable:
    'The stopped source cannot be read by this Manager. For Docker, resume the v2 upgrade script with the same operation so its temporary worker can read the original volume without changing its ownership. For other deployments, follow the matching recovery guide. Keep the original data; do not reinitialize.',
  changed:
    'The source or Manager process has changed. Sign in again to verify the current installation.',
  network: 'Authentication could not be confirmed. Check the connection and sign in again.',
  expired: 'Authentication has expired. Sign in again.',
  origin_rejected:
    'This browser address does not match the configured public console origin (CPAMP_PUBLIC_ORIGIN). Open the canonical public console URL to verify your administrator key and continue the upgrade.',
  origin_rejected_url:
    'This browser address does not match the configured public console origin ({origin}). Open {url} to verify your administrator key and continue the upgrade.',
  placeholder: 'Existing CPAMP administrator key',
  loginHint:
    'Existing installation detected. Verify your original CPAMP administrator key to view the upgrade starting page. Authentication does not start migration or connect to CPA.',
};
const zh: typeof en = {
  title: '升级到 CPAMP v2',
  mode: '升级模式',
  verified: '身份已验证',
  retained: '沿用原管理员密钥',
  blocked: '当前处于只读预检模式',
  boundary: '原管理员身份已验证；备份、数据复制、CPA 变更与管理入口变更均未启动，1.x 原数据保持不变。',
  recordedBlockedHelp:
    '原管理员身份已验证；下方展示当前安装已记录的升级进度，本页操作仅依据该记录执行。',
  observationUnavailable: '当前来源无法生成公开摘要。',
  observationUnavailableHelp:
    '管理员身份已验证，但来源公开摘要不可用。下方已认证的操作记录将决定可继续的升级或恢复步骤。',
  recordedObservationHelp:
    '管理员身份已验证，来源公开摘要不可用；请依据下方已认证的操作记录继续或恢复。',
  next: '',
  verify: '刷新身份',
  logout: '退出',
  checking: '正在验证原管理员身份…',
  checkingShort: '正在验证身份',
  checkingHelp: '正在核验当前安装，验证完成前不显示升级详情，也不开放升级操作。',
  recoveryLabel: '无法使用原密钥？',
  recoveryHelp:
    '请保留现有数据和密钥文件，停止 Manager 后在部署主机上使用与已安装版本匹配的工具恢复管理员密钥，再回到这里验证。不要重新初始化原安装；此处不提供在线密钥重置。',
  footer: 'CPA Manager Plus · 安装与升级',
  invalid: '无法验证原 Manager 管理员密钥，请重新输入。此处不能使用 CPA 管理密钥。',
  unavailable: '当前来源无法安全验证。请在宿主机停机检查原安装，不要重新初始化。',
  source_ownership_unavailable:
    '当前 Manager 无法读取已停止的来源。Docker 部署请使用同一操作重新运行 v2 升级脚本，由临时执行器只读访问原卷，无需修改原文件属主；其他部署请按对应版本的恢复指南处理。请保留原数据，不要重新初始化。',
  changed: '来源或 Manager 进程已变化，请重新登录以验证当前安装。',
  network: '身份验证结果未确认，请检查连接后重新登录。',
  expired: '身份验证已过期，请重新登录。',
  origin_rejected:
    '当前浏览器访问地址与已配置的公开控制台地址（CPAMP_PUBLIC_ORIGIN）不一致。请改为通过部署配置的公开控制台 URL 打开升级入口后再试。',
  origin_rejected_url:
    '当前浏览器访问地址与已配置的公开控制台地址（{origin}）不一致。请改为打开 {url} 验证管理员密钥并继续升级。',
  placeholder: '原 CPAMP 管理员密钥',
  loginHint:
    '检测到现有安装。请验证原 CPAMP 管理员密钥，进入升级起始页；认证不会启动迁移或连接 CPA。',
};
const tw: typeof en = {
  title: '升級至 CPAMP v2',
  mode: '升級模式',
  verified: '身分已驗證',
  retained: '沿用原管理員金鑰',
  blocked: '目前處於唯讀預檢模式',
  boundary: '原管理員身分已驗證；備份、資料複製、CPA 變更與管理入口變更均未啟動，1.x 原資料維持不變。',
  recordedBlockedHelp:
    '原管理員身分已驗證；下方顯示目前安裝已記錄的升級進度，本頁操作僅依據該筆記錄執行。',
  observationUnavailable: '目前來源無法產生公開摘要。',
  observationUnavailableHelp:
    '管理員身分已驗證，但來源公開摘要不可用。下方已驗證的操作記錄將決定可繼續的升級或復原步驟。',
  recordedObservationHelp:
    '管理員身分已驗證，來源公開摘要不可用；請依據下方已驗證的操作記錄繼續或復原。',
  next: '',
  verify: '重新驗證',
  logout: '登出',
  checking: '正在驗證原管理員身分…',
  checkingShort: '正在驗證身分',
  checkingHelp: '正在核驗目前安裝，驗證完成前不顯示升級詳情，也不開放升級操作。',
  recoveryLabel: '無法使用原金鑰？',
  recoveryHelp:
    '請保留現有資料與金鑰檔案，停止 Manager 後在部署主機上使用與已安裝版本相符的工具復原管理員金鑰，再回到這裡驗證。不要重新初始化原安裝；此處不提供線上金鑰重設。',
  footer: 'CPA Manager Plus · 安裝與升級',
  invalid: '無法驗證原 Manager 管理員金鑰，請重新輸入。此處不能使用 CPA 管理金鑰。',
  unavailable: '目前來源無法安全驗證。請在主機停機檢查原安裝，不要重新初始化。',
  source_ownership_unavailable:
    '目前 Manager 無法讀取已停止的來源。Docker 部署請以同一操作重新執行 v2 升級指令碼，由臨時執行器唯讀存取原卷，無需修改原檔案擁有者；其他部署請依對應版本的復原指南處理。請保留原資料，不要重新初始化。',
  changed: '來源或 Manager 程序已變更，請重新登入以驗證目前安裝。',
  network: '身分驗證結果未確認，請檢查連線後重新登入。',
  expired: '身分驗證已過期，請重新登入。',
  origin_rejected:
    '目前瀏覽器存取位址與已設定的公開主控台位址（CPAMP_PUBLIC_ORIGIN）不一致。請改為透過部署設定的公開主控台 URL 開啟升級入口後再試。',
  origin_rejected_url:
    '目前瀏覽器存取位址與已設定的公開主控台位址（{origin}）不一致。請改為開啟 {url} 驗證管理員金鑰並繼續升級。',
  placeholder: '原 CPAMP 管理員金鑰',
  loginHint:
    '偵測到現有安裝。請驗證原 CPAMP 管理員金鑰，進入升級起始頁；驗證不會啟動移轉或連線 CPA。',
};
const ru: typeof en = {
  title: 'Обновление до CPAMP v2',
  mode: 'Обновление',
  verified: 'Личность администратора подтверждена',
  retained: 'Существующий ключ администратора сохраняется. Новый ключ не требуется.',
  blocked: 'Режим предварительной проверки (только чтение)',
  boundary:
    'Исходный ключ подтверждён. Резервное копирование, копирование данных и изменения CPA ещё не запущены.',
  recordedBlockedHelp:
    'Исходный ключ подтверждён. Ниже показано записанное состояние обновления этой установки.',
  observationUnavailable: 'Источник нельзя описать публично.',
  observationUnavailableHelp:
    'Администратор подтверждён, но публичная сводка источника недоступна. Доступные шаги обновления и восстановления определяет авторизованная запись операции ниже.',
  recordedObservationHelp:
    'Администратор подтверждён. Публичная сводка недоступна; продолжайте или восстанавливайте установку по авторизованной записи операции ниже.',
  next: '',
  verify: 'Проверить подлинность ещё раз',
  logout: 'Вернуться ко входу',
  checking: 'Проверка существующего администратора…',
  checkingShort: 'Проверка личности',
  checkingHelp:
    'Проверяется текущая установка. Сведения об обновлении и действия недоступны до завершения проверки.',
  recoveryLabel: 'Не удаётся использовать исходный ключ?',
  recoveryHelp:
    'Сохраните существующие данные и файлы ключей. Остановите Manager и восстановите ключ администратора на хосте развёртывания инструментами для этой установленной версии. Затем вернитесь сюда для проверки. Не инициализируйте установку заново; сброс ключа онлайн здесь недоступен.',
  footer: 'CPA Manager Plus · Установка и обновление',
  invalid:
    'Не удалось проверить ключ администратора существующего Manager. Введите его ещё раз. Ключи управления CPA здесь не принимаются.',
  unavailable:
    'Этот источник нельзя безопасно проверить. Проверьте остановленную установку на её хосте; не инициализируйте её заново.',
  source_ownership_unavailable:
    'Manager не может прочитать остановленный источник. Для Docker повторно запустите скрипт обновления v2 с той же операцией: временный исполнитель читает исходный том без изменения владельца. Для других установок используйте соответствующее руководство восстановления. Сохраните исходные данные и не инициализируйте установку заново.',
  changed:
    'Источник или процесс Manager изменились. Войдите снова, чтобы проверить текущую установку.',
  network: 'Не удалось подтвердить проверку личности. Проверьте соединение и войдите снова.',
  expired: 'Срок проверки личности истёк. Войдите снова.',
  origin_rejected:
    'Текущий адрес в браузере не совпадает с настроенным публичным адресом консоли (CPAMP_PUBLIC_ORIGIN). Откройте канонический публичный URL консоли для подтверждения ключа администратора и продолжения обновления.',
  origin_rejected_url:
    'Текущий адрес в браузере не совпадает с настроенным публичным адресом консоли ({origin}). Откройте {url}, чтобы подтвердить ключ администратора и продолжить обновление.',
  placeholder: 'Ключ администратора существующего CPAMP',
  loginHint:
    'Обнаружена существующая установка. Подтвердите исходный ключ администратора CPAMP, чтобы открыть страницу начала обновления. Проверка не запускает миграцию и не подключается к CPA.',
};

// The entry page is served before the Full Web bundle, so its copy is resolved
// from the same four locales as the wizard rather than from the bundle's i18n.
export const upgradeEntryText = (key: keyof typeof en, language = fullWebLanguage()) => {
  if (language === 'zh-TW') return tw[key];
  if (language.startsWith('zh')) return zh[key];
  if (language === 'ru') return ru[key];
  return en[key];
};
