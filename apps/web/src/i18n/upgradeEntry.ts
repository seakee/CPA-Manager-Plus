import { fullWebLanguage } from './fullweb';

const en = {
  title: 'Upgrade to CPAMP v2',
  mode: 'Upgrade',
  verified: 'Administrator identity verified',
  retained: 'Your existing administrator key is retained. No new key is required.',
  blocked: 'Migration is not available for this source yet.',
  boundary:
    'Authentication is complete. Backup, migration, CPA changes and management entry changes have not started.',
  observationUnavailable: 'The source cannot be summarized publicly.',
  observationUnavailableHelp:
    'Your administrator identity is verified, but this source keeps recent writes in a log its recovery path owns, so the public summary is unavailable. Nothing was written and migration is not available; the read-only operation view below still shows what this installation recorded.',
  recordedObservationHelp:
    'Your administrator identity is verified. The source keeps recent writes in a log its recovery path owns, so its public summary is unavailable — the read-only operation view below shows what this installation recorded, and it is the record this page acts on.',
  next: 'Further upgrade decisions will become available when a supported migration path is implemented. Keep your existing data and key.',
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
  verified: '管理员身份已验证',
  retained: '沿用现有管理员密钥，无需重新设置。',
  blocked: '当前来源尚无可执行的迁移路径。',
  boundary: '身份验证已完成；备份、迁移、CPA 变更和管理入口变更均未开始。',
  observationUnavailable: '当前来源无法生成公开摘要。',
  observationUnavailableHelp:
    '管理员身份已验证，但该来源将最近写入保留在由恢复流程掌管的日志中，因此公开摘要不可用。没有写入任何数据，也没有可执行的迁移；下方只读操作视图仍会显示本安装已记录的内容。',
  recordedObservationHelp:
    '管理员身份已验证。该来源将最近写入保留在由恢复流程掌管的日志中，因此无法生成公开摘要；下方只读操作视图会显示本安装已记录的内容，本页面的操作只依据这条记录。',
  next: '支持的迁移路径实现后才能继续升级决策。请保留原有数据和管理员密钥。',
  verify: '重新验证当前身份',
  logout: '返回登录',
  checking: '正在验证原管理员身份…',
  checkingShort: '正在验证身份',
  checkingHelp: '正在核验当前安装，验证完成前不显示升级详情，也不开放升级操作。',
  recoveryLabel: '无法使用原密钥？',
  recoveryHelp:
    '请保留现有数据和密钥文件，停止 Manager 后在部署主机上使用与已安装版本匹配的工具恢复管理员密钥，再回到这里验证。不要重新初始化原安装；此处不提供在线密钥重置。',
  footer: 'CPA Manager Plus · 安装与升级',
  invalid: '无法验证原 Manager 管理员密钥，请重新输入。此处不能使用 CPA 管理密钥。',
  unavailable: '当前来源无法安全验证。请在宿主机停机检查原安装，不要重新初始化。',
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
  verified: '管理員身分已驗證',
  retained: '沿用現有管理員金鑰，無需重新設定。',
  blocked: '目前來源尚無可執行的移轉路徑。',
  boundary: '身分驗證已完成；備份、移轉、CPA 變更與管理入口變更均未開始。',
  observationUnavailable: '目前來源無法產生公開摘要。',
  observationUnavailableHelp:
    '管理員身分已驗證，但此來源將最近寫入保留在由復原流程掌管的日誌中，因此公開摘要不可用。沒有寫入任何資料，也沒有可執行的移轉；下方唯讀操作檢視仍會顯示本安裝已記錄的內容。',
  recordedObservationHelp:
    '管理員身分已驗證。此來源將最近寫入保留在由復原流程掌管的記錄檔中，因此無法產生公開摘要；下方唯讀操作檢視會顯示本安裝已記錄的內容，本頁面的操作只依據這筆記錄。',
  next: '支援的移轉路徑實作後才能繼續升級決策。請保留原有資料與管理員金鑰。',
  verify: '重新驗證目前身分',
  logout: '返回登入',
  checking: '正在驗證原管理員身分…',
  checkingShort: '正在驗證身分',
  checkingHelp: '正在核驗目前安裝，驗證完成前不顯示升級詳情，也不開放升級操作。',
  recoveryLabel: '無法使用原金鑰？',
  recoveryHelp:
    '請保留現有資料與金鑰檔案，停止 Manager 後在部署主機上使用與已安裝版本相符的工具復原管理員金鑰，再回到這裡驗證。不要重新初始化原安裝；此處不提供線上金鑰重設。',
  footer: 'CPA Manager Plus · 安裝與升級',
  invalid: '無法驗證原 Manager 管理員金鑰，請重新輸入。此處不能使用 CPA 管理金鑰。',
  unavailable: '目前來源無法安全驗證。請在主機停機檢查原安裝，不要重新初始化。',
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
  blocked: 'Для этого источника пока нет поддерживаемого пути миграции.',
  boundary:
    'Проверка личности завершена. Резервное копирование, миграция, изменения CPA и точки входа управления не начаты.',
  observationUnavailable: 'Источник нельзя описать публично.',
  observationUnavailableHelp:
    'Личность администратора подтверждена, но этот источник хранит последние записи в журнале, которым владеет путь восстановления, поэтому публичная сводка недоступна. Ничего не записано, миграция недоступна; доступный только для чтения обзор операции ниже по-прежнему показывает, что записала эта установка.',
  recordedObservationHelp:
    'Личность администратора подтверждена. Источник хранит последние записи в журнале, которым управляет путь восстановления, поэтому публичная сводка недоступна; ниже показано, что записала эта установка, и действия страницы опираются только на эту запись.',
  next: 'Дальнейшие решения об обновлении появятся, когда будет реализован поддерживаемый путь миграции. Сохраните существующие данные и ключ.',
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
