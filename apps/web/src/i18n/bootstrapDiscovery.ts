import { fullWebLanguage } from './fullweb';

// Localized copy for the Bootstrap guidance card shown when a 1.x data volume
// is mounted at /data with root-owned marker or key files. Entries are resolved
// at render time so switching the Bootstrap page language re-translates a
// discovery response that was fetched once.
const en = {
  ownershipStepTag: 'Upgrade Gate · 1.x Volume Guidance',
  ownershipPageTitle: '1.x data volume detected — mount for Upgrade or fix file ownership',
  ownershipPageDesc:
    'v2 never upgrades a 1.x data volume mounted at /data in place. Use a separate new v2 /data volume with a read-only /legacy mount to upgrade, or adjust only the Manager database file ownership to open the read-only Upgrade entry.',
  ownershipGuidanceTitle: 'Existing 1.x data volume detected (host ownership action required)',
  ownershipGuidanceDetail:
    'Database lock or key files under /data are owned by root (0:0). v2 Manager runs as unprivileged user 10002:10002 and never upgrades a 1.x volume in place. Startup stopped safely in read-only guidance mode and did not modify the 1.x database and key files.',
  ownershipUpgradeTitle: 'Option 1 (Recommended): Upgrade from 1.x v1.14.10 using a separate v2 /data volume',
  ownershipUpgradeDesc:
    'Stop this container, keep the original 1.x volume untouched and mounted read-only at /legacy:ro, mount a new empty volume at /data, and run the offline preflight and upgrade on the host:',
  ownershipChownTitle:
    'Option 2: Open the read-only Upgrade entry on this stopped volume — fix only the Manager file ownership',
  ownershipChownDesc:
    'To verify your 1.x administrator key and read the operation status on this volume, stop the v2 container, run one one-off container that mounts the volume and changes ownership only on the database and key files (never on /data/runtime, which must stay 0:0), start the v2 container again, and re-check:',
  recheckButton: 'Recheck source status',
};

const zh: typeof en = {
  ownershipStepTag: 'Upgrade Gate · 1.x 数据卷引导',
  ownershipPageTitle: '检测到 1.x 数据卷，请按升级方式挂载或修复文件属主',
  ownershipPageDesc:
    'v2 不会原地改写挂在 /data 的 1.x 数据卷。请使用独立的新 v2 数据卷配合只读 /legacy 挂载执行升级，或仅调整数据库与密钥文件属主以进入只读升级入口。',
  ownershipGuidanceTitle: '检测到现有 1.x 数据卷（属主权限需在宿主机处理）',
  ownershipGuidanceDetail:
    '当前 /data 中的数据库锁或密钥文件属于 root (0:0)，v2 Manager 以非特权用户 10002:10002 运行且绝不原地升级 1.x 数据卷。服务已安全停留在只读引导模式，未修改 1.x 数据库与密钥文件。',
  ownershipUpgradeTitle: '方式一（推荐）：使用独立新 /data 卷并将原 1.x 卷只读挂到 /legacy 升级',
  ownershipUpgradeDesc:
    '停止当前容器，保持原 1.x（v1.14.10）数据卷只读不变并挂载到 /legacy:ro，为 /data 挂载全新空卷后，在宿主机执行离线预检与升级：',
  ownershipChownTitle: '方式二：仅修复已停止数据卷上的数据库文件属主，进入只读升级入口',
  ownershipChownDesc:
    '若需在该卷上验证原 1.x 管理员密钥并查看升级状态：先停止 v2 容器，用一个一次性容器挂载该卷、仅修改 usage.sqlite* 与 data.key 的属主（切勿改动 /data/runtime，它必须保持 0:0），再启动 v2 容器并点击下方重新检查：',
  recheckButton: '重新检查来源状态',
};

const tw: typeof en = {
  ownershipStepTag: 'Upgrade Gate · 1.x 資料卷引導',
  ownershipPageTitle: '偵測到 1.x 資料卷，請依升級方式掛載或修復檔案擁有者',
  ownershipPageDesc:
    'v2 不會原地改寫掛在 /data 的 1.x 資料卷。請使用獨立的新 v2 資料卷搭配唯讀 /legacy 掛載執行升級，或僅調整資料庫與金鑰檔案擁有者以進入唯讀升級入口。',
  ownershipGuidanceTitle: '偵測到現有 1.x 資料卷（擁有者權限需在主機處理）',
  ownershipGuidanceDetail:
    '目前 /data 中的資料庫鎖或金鑰檔案屬於 root (0:0)，v2 Manager 以非特權使用者 10002:10002 執行且絕不原地升級 1.x 資料卷。服務已安全停留在唯讀引導模式，未修改 1.x 資料庫與金鑰檔案。',
  ownershipUpgradeTitle: '方式一（推薦）：使用獨立新 /data 卷並將原 1.x 卷唯讀掛載至 /legacy 升級',
  ownershipUpgradeDesc:
    '停止目前容器，保持原 1.x（v1.14.10）資料卷唯讀不變並掛載至 /legacy:ro，為 /data 掛載全新空卷後，在主機執行離線預檢與升級：',
  ownershipChownTitle: '方式二：僅修復已停止資料卷上的資料庫檔案擁有者，進入唯讀升級入口',
  ownershipChownDesc:
    '若需在該資料卷上驗證原 1.x 管理員金鑰並檢視升級狀態：先停止 v2 容器，用一個一次性容器掛載該資料卷、僅修改 usage.sqlite* 與 data.key 的擁有者（切勿改動 /data/runtime，它必須維持 0:0），再啟動 v2 容器並點擊下方重新檢查：',
  recheckButton: '重新檢查來源狀態',
};

const ru: typeof en = {
  ownershipStepTag: 'Upgrade Gate · Навигация для тома 1.x',
  ownershipPageTitle:
    'Обнаружен том данных 1.x — смонтируйте для обновления или исправьте владельца файлов',
  ownershipPageDesc:
    'v2 никогда не обновляет том данных 1.x в /data на месте. Используйте отдельный новый том /data с монтированием /legacy только для чтения либо измените владельца только файлов базы Manager для открытия входа обновления.',
  ownershipGuidanceTitle: 'Обнаружен существующий том данных 1.x (требуется действие с правами на хосте)',
  ownershipGuidanceDetail:
    'Файлы блокировки базы данных или ключа в /data принадлежат root (0:0). v2 Manager работает от непривилегированного пользователя 10002:10002 и никогда не обновляет том 1.x на месте. Запуск безопасно остановлен в режиме чтения, база данных и файлы ключей 1.x не изменялись.',
  ownershipUpgradeTitle:
    'Вариант 1 (рекомендуется): обновление с 1.x v1.14.10 через отдельный новый том /data',
  ownershipUpgradeDesc:
    'Остановите этот контейнер, оставьте исходный том 1.x неизменным в режиме только чтения (/legacy:ro), подключите новый пустой том к /data и выполните предварительную проверку и обновление на хосте:',
  ownershipChownTitle:
    'Вариант 2: открыть вход обновления только для чтения на остановленном томе — исправьте только владельца файлов Manager',
  ownershipChownDesc:
    'Чтобы проверить исходный ключ администратора 1.x и увидеть состояние обновления на этом томе: остановите контейнер v2, запустите один разовый контейнер с этим томом и смените владельца только файлов базы данных и ключа (никогда не меняйте /data/runtime — он должен принадлежать 0:0), затем запустите контейнер v2 и нажмите повторную проверку:',
  recheckButton: 'Проверить состояние источника снова',
};

export type BootstrapDiscoveryTextKey = keyof typeof en;

export function bootstrapDiscoveryText(key: BootstrapDiscoveryTextKey, language = fullWebLanguage()): string {
  if (language === 'zh-TW') return tw[key];
  if (language.startsWith('zh')) return zh[key];
  if (language === 'ru') return ru[key];
  return en[key];
}
