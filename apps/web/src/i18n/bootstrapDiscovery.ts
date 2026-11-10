import { fullWebLanguage } from './fullweb';

const en = {
  languageLabel: 'Switch language',
  freshStepTag: 'Bootstrap Gate · Host Authorization Required',
  ownershipStepTag: 'Upgrade Gate · 1.x Volume Guidance',
  preflightStepTag: 'Source Preflight · Read-Only Source Check',
  ownershipPageTitle: '1.x data volume detected — mount for Upgrade or fix file ownership',
  ownershipPageDesc:
    'v2 never upgrades a 1.x data volume mounted at /data in place. Use a separate new v2 /data volume with a read-only /legacy mount to upgrade, or adjust only the Manager database file ownership to open the read-only Upgrade entry.',
  hostPageTitle: 'This source requires a host preflight and cannot start Fresh setup',
  hostPageDesc:
    'Public Discovery only classifies the source without reading or modifying your data; perform the next step on the deployment host.',
  unconfirmedPageTitle: 'Source status cannot be confirmed — initialization is blocked',
  pendingPageTitle: 'Checking source status — initialization is blocked',
  unconfirmedPageDesc:
    'No verified Fresh classification is available. Check the source volume and service logs on the deployment host; do not initialize or migrate from this page.',
  freshGuidanceTitle: 'Fresh initialization state detected — host claim required',
  freshGuidanceDetail:
    'This status is a read-only check. Obtain a one-time Handoff URL on the deployment host; this page never creates or claims an installation on its own.',
  upgradeGuidanceTitle: 'Existing installation detected without active migration qualification',
  upgradeGuidanceDetail:
    'Inspect the original source only on the deployment host. Neither Fresh setup nor in-place migration runs here; a preflight report never grants import or overwrite permission.',
  ownershipGuidanceTitle: 'Existing 1.x data volume detected (host ownership action required)',
  ownershipGuidanceDetail:
    'Database lock or key files under /data are owned by root (0:0). v2 Manager runs as unprivileged user 10002:10002 and never upgrades a 1.x volume in place. Startup stopped safely in read-only guidance mode without modifying your data.',
  recoveryGuidanceTitle: 'Recovery state detected — host intervention required',
  recoveryGuidanceDetail:
    'This source is not eligible for Fresh setup. Run a read-only preflight on the deployment host first and preserve usage.sqlite, data.key, and backups; never start Fresh, Adopt, or Upgrade writes here.',
  unavailableGuidanceTitle: 'Source status temporarily unconfirmed',
  unavailableGuidanceDetail:
    'The read-only check could not classify this source safely. Inspect it on the deployment host; never treat an unconfirmed source as Fresh or start initialization.',
  ownershipUpgradeTitle: 'Option 1 (Recommended): Upgrade from 1.x v1.14.10 using a separate v2 /data volume',
  ownershipUpgradeDesc:
    'Stop this container, keep the original 1.x volume untouched and mounted read-only at /legacy:ro, mount a new empty volume at /data, and run offline preflight before upgrade run:',
  ownershipChownTitle:
    'Option 2: Fix Manager file ownership on this stopped volume to open the read-only Upgrade entry',
  ownershipChownDesc:
    'To verify your 1.x administrator key and inspect status here, change ownership only on usage.sqlite* and data.key (never run chown -R 10002:10002 /data, because /data/runtime must stay owned by 0:0), then recheck:',
  hostPreflightTitle: 'Next step: run a read-only source preflight on the host',
  hostPreflightDesc:
    'Replace the path with the real database path. This command never authorizes migration; do not assert the source is stopped while a writer is running.',
  freshHowTitle: 'How to obtain or resume a Bootstrap authorization link',
  freshHowPrefix: 'Run the initialize command on the deployment host and reopen this page with the full ',
  freshHowSuffix: ' URL it prints:',
  unconfirmedCardTitle: 'Source status unconfirmed',
  pendingCardTitle: 'Reading source status',
  unconfirmedCardDesc:
    'Fresh initialize commands are withheld until the source is verified. Check the volume, logs, and service status on the deployment host; unknown status confers no write permission.',
  recheckButton: 'Recheck source status',
};

const zh: typeof en = {
  languageLabel: '切换语言',
  freshStepTag: 'Bootstrap Gate · 需要安装主机授权',
  ownershipStepTag: 'Upgrade Gate · 1.x 数据卷引导',
  preflightStepTag: 'Source Preflight · 只读来源检查',
  ownershipPageTitle: '检测到 1.x 数据卷，请按升级方式挂载或修复文件属主',
  ownershipPageDesc:
    'v2 不会原地改写挂在 /data 的 1.x 数据卷。请使用独立的新 v2 数据卷配合只读 /legacy 挂载执行升级，或仅调整数据库与密钥文件属主以进入只读升级入口。',
  hostPageTitle: '当前来源需要宿主机预检，不能直接 Fresh 初始化',
  hostPageDesc: '公共 Discovery 仅提供来源类型提示，不读取或修改现有数据；后续操作必须在受信宿主机执行。',
  unconfirmedPageTitle: '来源状态无法确认，禁止直接初始化',
  pendingPageTitle: '正在确认来源状态，禁止直接初始化',
  unconfirmedPageDesc:
    '尚未取得可信的 Fresh 来源分类；请检查宿主机上的来源与服务状态，不能依据当前页面执行初始化或迁移。',
  freshGuidanceTitle: '检测到需要宿主机认领的 Fresh 初始化状态',
  freshGuidanceDetail:
    '此状态仅为只读识别结果。请从受信宿主机获取一次性 Handoff 链接；本页面不会自行创建或认领安装。',
  upgradeGuidanceTitle: '检测到尚未获得升级资格的现有来源',
  upgradeGuidanceDetail:
    '只能在受信宿主机检查原始来源。当前无法进入 Fresh 初始化或 Upgrade 切换；预检成功也不代表获得导入或覆盖授权。',
  ownershipGuidanceTitle: '检测到现有 1.x 数据卷（属主权限需在宿主机处理）',
  ownershipGuidanceDetail:
    '当前 /data 中的数据库锁或密钥文件属于 root (0:0)，v2 Manager 以非特权用户 10002:10002 运行且绝不原地升级 1.x 数据卷。服务已安全停留在只读引导模式，未修改任何现有数据。',
  recoveryGuidanceTitle: '检测到需要宿主机介入的恢复状态',
  recoveryGuidanceDetail:
    '来源不具备直接初始化资格。请先在受信宿主机进行只读预检，保护数据库、data.key 和已有备份；不得从此页面尝试 Fresh/Adopt/Upgrade 写入。',
  unavailableGuidanceTitle: '来源状态暂无法确认',
  unavailableGuidanceDetail:
    '来源只读检查未获得可信分类。请在受信宿主机排查；不能根据此状态认定为 Fresh，也不能据此执行初始化或迁移。',
  ownershipUpgradeTitle: '方式一（推荐）：使用独立新 /data 卷并将原 1.x 卷只读挂到 /legacy 升级',
  ownershipUpgradeDesc:
    '停止当前容器，保持原 1.x（v1.14.10）数据卷只读不变并挂载到 /legacy:ro，为 /data 挂载全新空卷后在宿主机执行离线预检与升级：',
  ownershipChownTitle: '方式二：仅修复已停止数据卷上的数据库文件属主，进入只读升级入口',
  ownershipChownDesc:
    '若需在当前卷上验证原 1.x 管理员密钥并查看升级起始页，请仅修改 usage.sqlite* 与 data.key 的属主（切勿执行 chown -R 10002:10002 /data，/data/runtime 必须保持 0:0 属主），完成后点击下方重新检查：',
  hostPreflightTitle: '下一步：在宿主机执行只读来源预检',
  hostPreflightDesc:
    '把路径替换为真实来源数据库路径。此命令不会授权迁移，且不要在来源写入仍运行时声称已停止。',
  freshHowTitle: '如何获取或恢复 Bootstrap 授权链接？',
  freshHowPrefix: '请在受信宿主机运行初始化命令，并使用输出的带 ',
  freshHowSuffix: ' 片段的完整 URL 重新打开本页面：',
  unconfirmedCardTitle: '来源状态无法确认',
  pendingCardTitle: '正在读取来源状态',
  unconfirmedCardDesc:
    '不提供 Fresh 初始化命令；请在受信宿主机确认来源、日志与服务状态。未知结果不会授予任何写入权限。',
  recheckButton: '重新检查来源状态',
};

const tw: typeof en = {
  languageLabel: '切換語言',
  freshStepTag: 'Bootstrap Gate · 需要安裝主機授權',
  ownershipStepTag: 'Upgrade Gate · 1.x 資料卷引導',
  preflightStepTag: 'Source Preflight · 唯讀來源檢查',
  ownershipPageTitle: '偵測到 1.x 資料卷，請依升級方式掛載或修復檔案擁有者',
  ownershipPageDesc:
    'v2 不會原地改寫掛在 /data 的 1.x 資料卷。請使用獨立的新 v2 資料卷搭配唯讀 /legacy 掛載執行升級，或僅調整資料庫與金鑰檔案擁有者以進入唯讀升級入口。',
  hostPageTitle: '目前來源需要主機預檢，不能直接 Fresh 初始化',
  hostPageDesc: '公開 Discovery 僅提供來源類型提示，不讀取或修改現有資料；後續操作必須在受信任主機執行。',
  unconfirmedPageTitle: '來源狀態無法確認，禁止直接初始化',
  pendingPageTitle: '正在確認來源狀態，禁止直接初始化',
  unconfirmedPageDesc:
    '尚未取得可信的 Fresh 來源分類；請檢查主機上的來源與服務狀態，不能依據目前頁面執行初始化或移轉。',
  freshGuidanceTitle: '偵測到需要主機認領的 Fresh 初始化狀態',
  freshGuidanceDetail:
    '此狀態僅為唯讀識別結果。請從受信任主機取得一次性 Handoff 連結；本頁面不會自行建立或認領安裝。',
  upgradeGuidanceTitle: '偵測到尚未取得升級資格的現有來源',
  upgradeGuidanceDetail:
    '只能在受信任主機檢查原始來源。目前無法進入 Fresh 初始化或 Upgrade 切換；預檢成功也不代表取得匯入或覆寫授權。',
  ownershipGuidanceTitle: '偵測到現有 1.x 資料卷（擁有者權限需在主機處理）',
  ownershipGuidanceDetail:
    '目前 /data 中的資料庫鎖或金鑰檔案屬於 root (0:0)，v2 Manager 以非特權使用者 10002:10002 執行且絕不原地升級 1.x 資料卷。服務已安全停留在唯讀引導模式，未修改任何現有資料。',
  recoveryGuidanceTitle: '偵測到需要主機介入的復原狀態',
  recoveryGuidanceDetail:
    '來源不具備直接初始化資格。請先在受信任主機進行唯讀預檢，保護資料庫、data.key 與既有備份；不得從此頁面嘗試 Fresh/Adopt/Upgrade 寫入。',
  unavailableGuidanceTitle: '來源狀態暫無法確認',
  unavailableGuidanceDetail:
    '來源唯讀檢查未取得可信分類。請在受信任主機排查；不能根據此狀態認定為 Fresh，也不能據此執行初始化或移轉。',
  ownershipUpgradeTitle: '方式一（推薦）：使用獨立新 /data 卷並將原 1.x 卷唯讀掛載至 /legacy 升級',
  ownershipUpgradeDesc:
    '停止目前容器，保持原 1.x（v1.14.10）資料卷唯讀不變並掛載至 /legacy:ro，為 /data 掛載全新空卷後在主機執行離線預檢與升級：',
  ownershipChownTitle: '方式二：僅修復已停止資料卷上的資料庫檔案擁有者，進入唯讀升級入口',
  ownershipChownDesc:
    '若需在目前資料卷上驗證原 1.x 管理員金鑰並檢視升級起始頁，請僅修改 usage.sqlite* 與 data.key 的擁有者（切勿執行 chown -R 10002:10002 /data，/data/runtime 必須維持 0:0 擁有者），完成後點擊下方重新檢查：',
  hostPreflightTitle: '下一步：在主機執行唯讀來源預檢',
  hostPreflightDesc:
    '把路徑替換為真實來源資料庫路徑。此命令不會授權移轉，且不要在來源寫入仍執行時聲稱已停止。',
  freshHowTitle: '如何取得或恢復 Bootstrap 授權連結？',
  freshHowPrefix: '請在受信任主機執行初始化命令，並使用輸出的帶 ',
  freshHowSuffix: ' 片段的完整 URL 重新開啟本頁面：',
  unconfirmedCardTitle: '來源狀態無法確認',
  pendingCardTitle: '正在讀取來源狀態',
  unconfirmedCardDesc:
    '不提供 Fresh 初始化命令；請在受信任主機確認來源、日誌與服務狀態。未知結果不會授予任何寫入權限。',
  recheckButton: '重新檢查來源狀態',
};

const ru: typeof en = {
  languageLabel: 'Сменить язык',
  freshStepTag: 'Bootstrap Gate · Требуется авторизация хоста',
  ownershipStepTag: 'Upgrade Gate · Навигация для тома 1.x',
  preflightStepTag: 'Source Preflight · Проверка источника только для чтения',
  ownershipPageTitle: 'Обнаружен том данных 1.x — смонтируйте для обновления или исправьте владельца файлов',
  ownershipPageDesc:
    'v2 никогда не обновляет том данных 1.x в /data на месте. Используйте отдельный новый том /data с монтированием /legacy только для чтения либо измените владельца только файлов базы Manager для открытия входа обновления.',
  hostPageTitle: 'Этот источник требует предварительной проверки на хосте и не подходит для Fresh-установки',
  hostPageDesc:
    'Публичный Discovery только определяет тип источника без чтения и изменения данных; выполните следующий шаг на хосте развёртывания.',
  unconfirmedPageTitle: 'Состояние источника не подтверждено — инициализация заблокирована',
  pendingPageTitle: 'Проверка состояния источника — инициализация заблокирована',
  unconfirmedPageDesc:
    'Подтверждённая классификация Fresh отсутствует. Проверьте том и журналы службы на хосте развёртывания; не выполняйте инициализацию или миграцию с этой страницы.',
  freshGuidanceTitle: 'Обнаружено состояние Fresh-установки — требуется подтверждение на хосте',
  freshGuidanceDetail:
    'Этот статус получен в режиме только чтения. Получите одноразовую ссылку Handoff на хосте развёртывания; страница сама не создаёт и не подтверждает установку.',
  upgradeGuidanceTitle: 'Обнаружена существующая установка без активного допуска к миграции',
  upgradeGuidanceDetail:
    'Проверяйте исходный том только на хосте развёртывания. Ни Fresh-установка, ни миграция на месте здесь не выполняются; отчёт проверки не даёт права на импорт или перезапись.',
  ownershipGuidanceTitle: 'Обнаружен существующий том данных 1.x (требуется действие с правами на хосте)',
  ownershipGuidanceDetail:
    'Файлы блокировки базы данных или ключа в /data принадлежат root (0:0). v2 Manager работает от непривилегированного пользователя 10002:10002 и никогда не обновляет том 1.x на месте. Запуск безопасно остановлен в режиме чтения без изменения данных.',
  recoveryGuidanceTitle: 'Обнаружено состояние восстановления — требуется вмешательство на хосте',
  recoveryGuidanceDetail:
    'Источник не подходит для Fresh-установки. Сначала выполните проверку только для чтения на хосте развёртывания и сохраните usage.sqlite, data.key и резервные копии; не запускайте запись с этой страницы.',
  unavailableGuidanceTitle: 'Состояние источника временно не подтверждено',
  unavailableGuidanceDetail:
    'Проверка только для чтения не смогла безопасно классифицировать источник. Проверьте его на хосте развёртывания; не считайте неподтверждённый источник пустым и не запускайте инициализацию.',
  ownershipUpgradeTitle:
    'Вариант 1 (рекомендуется): обновление с 1.x v1.14.10 через отдельный новый том /data',
  ownershipUpgradeDesc:
    'Остановите этот контейнер, оставьте исходный том 1.x неизменным в режиме только чтения (/legacy:ro), подключите новый пустой том к /data и выполните предварительную проверку перед upgrade run:',
  ownershipChownTitle:
    'Вариант 2: исправить владельца файлов базы данных на остановленном томе для открытия входа обновления',
  ownershipChownDesc:
    'Чтобы проверить исходный ключ администратора 1.x и открыть стартовую страницу обновления на этом томе, смените владельца только для usage.sqlite* и data.key (никогда не выполняйте chown -R 10002:10002 /data — каталог /data/runtime должен принадлежать 0:0), затем нажмите повторную проверку:',
  hostPreflightTitle: 'Следующий шаг: выполните проверку источника только для чтения на хосте',
  hostPreflightDesc:
    'Замените путь реальным путём к базе данных. Команда не разрешает миграцию; не указывайте, что источник остановлен, пока работает запись.',
  freshHowTitle: 'Как получить или восстановить ссылку авторизации Bootstrap?',
  freshHowPrefix: 'Выполните команду инициализации на хосте развёртывания и откройте эту страницу по полному URL с фрагментом ',
  freshHowSuffix: ' из вывода команды:',
  unconfirmedCardTitle: 'Состояние источника не подтверждено',
  pendingCardTitle: 'Чтение состояния источника',
  unconfirmedCardDesc:
    'Команда инициализации Fresh скрыта до подтверждения источника. Проверьте том, журналы и состояние службы на хосте развёртывания; неизвестный статус не даёт прав на запись.',
  recheckButton: 'Проверить состояние источника снова',
};

export function bootstrapDiscoveryText(key: keyof typeof en, language = fullWebLanguage()): string {
  if (language === 'zh-TW') return tw[key];
  if (language.startsWith('zh')) return zh[key];
  if (language === 'ru') return ru[key];
  return en[key];
}
