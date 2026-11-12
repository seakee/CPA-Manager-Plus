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
    'Stop this container, keep the original 1.x volume mounted read-only at /legacy:ro, mount a new empty volume at /data, and run the offline preflight and upgrade on the host (if you also mount /legacy:ro into the running v2 container for the web Upgrade entry, first run chown -h 10002:10002 on /legacy/usage.sqlite* and /legacy/data.key or mount a copy readable by 10002:10002):',
  ownershipChownTitle:
    'Option 2: Open the read-only Upgrade entry on this stopped volume — fix only the Manager file ownership',
  ownershipChownDesc:
    'To verify your 1.x administrator key and read the operation status on this volume, stop the v2 container, run one one-off container that mounts the volume and changes ownership only on the database and key files (never on /data/runtime, which must stay 0:0), start the v2 container again, and re-check:',
  recheckButton: 'Recheck source status',
  resumeStepTag: 'Bootstrap Gate · Resume Setup',
  resumePageTitle: 'Installation is not finished — issue a new Handoff link on the host to resume',
  resumePageDesc:
    'Administrator setup is saved, while the Console route and installation completion are still pending. Run cpa-manager-plus initialize on the deployment host to obtain a new resume link without resetting your administrator key.',
  resumeGuidanceTitle: 'Existing unfinished Fresh installation detected',
  resumeGuidanceDetail:
    'The installation checkpoint is preserved on /data, and model traffic stays closed until setup completes. A new one-time Handoff link from the host will resume from the saved step.',
  resumeActionTitle: 'Next step: issue a new Handoff link on the host',
  resumeActionLead:
    'Stop the writable CPAMP service on the host, run the command below to print a new ',
  resumeActionTail:
    ' link, start the service again, and open that full URL in this browser to resume the wizard:',
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
    '停止当前容器，将原 1.x（v1.14.10）数据卷只读挂载到 /legacy:ro，为 /data 挂载全新空卷后，在宿主机执行离线预检与升级（若同时把 /legacy:ro 挂载给运行中的 v2 容器以使用网页端升级入口，还需先将该 1.x 卷或副本上的 usage.sqlite* 与 data.key 执行 chown -h 10002:10002，使 10002:10002 可读）：',
  ownershipChownTitle: '方式二：仅修复已停止数据卷上的数据库文件属主，进入只读升级入口',
  ownershipChownDesc:
    '若需在该卷上验证原 1.x 管理员密钥并查看升级状态：先停止 v2 容器，用一个一次性容器挂载该卷、仅修改 usage.sqlite* 与 data.key 的属主（切勿改动 /data/runtime，它必须保持 0:0），再启动 v2 容器并点击下方重新检查：',
  recheckButton: '重新检查来源状态',
  resumeStepTag: 'Bootstrap Gate · 继续安装',
  resumePageTitle: '安装尚未完成，请在宿主机重新签发 Handoff 链接继续安装',
  resumePageDesc:
    '管理员设置已保存，但控制台路由与安装完成步骤仍未执行完毕。请在部署宿主机运行 cpa-manager-plus initialize 获取新的继续安装链接，无需重置管理员密钥。',
  resumeGuidanceTitle: '检测到未完成的全新安装记录',
  resumeGuidanceDetail:
    '当前 /data 数据卷中已保留先前的安装进度检查点，完成前模型流量保持受控关闭。从宿主机重新签发一次性 Handoff 链接后将从已保存步骤继续。',
  resumeActionTitle: '下一步：在宿主机签发新的 Handoff 链接',
  resumeActionLead: '先在宿主机停止运行中的可写 CPAMP 服务，执行下方命令输出新的 ',
  resumeActionTail: ' 链接，重新启动服务后在浏览器打开该完整 URL 即可恢复向导进度：',
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
    '停止目前容器，將原 1.x（v1.14.10）資料卷唯讀掛載至 /legacy:ro，為 /data 掛載全新空卷後，在主機執行離線預檢與升級（若同時把 /legacy:ro 掛載給執行中的 v2 容器以使用網頁端升級入口，還需先將該 1.x 卷或副本上的 usage.sqlite* 與 data.key 執行 chown -h 10002:10002，使 10002:10002 可讀）：',
  ownershipChownTitle: '方式二：僅修復已停止資料卷上的資料庫檔案擁有者，進入唯讀升級入口',
  ownershipChownDesc:
    '若需在該資料卷上驗證原 1.x 管理員金鑰並檢視升級狀態：先停止 v2 容器，用一個一次性容器掛載該資料卷、僅修改 usage.sqlite* 與 data.key 的擁有者（切勿改動 /data/runtime，它必須維持 0:0），再啟動 v2 容器並點擊下方重新檢查：',
  recheckButton: '重新檢查來源狀態',
  resumeStepTag: 'Bootstrap Gate · 繼續安裝',
  resumePageTitle: '安裝尚未完成，請在主機重新簽發 Handoff 連結繼續安裝',
  resumePageDesc:
    '管理員設定已儲存，但主控台路由與安裝完成步驟仍未執行完畢。請在部署主機執行 cpa-manager-plus initialize 取得新的繼續安裝連結，無需重設管理員金鑰。',
  resumeGuidanceTitle: '偵測到未完成的全新安裝記錄',
  resumeGuidanceDetail:
    '目前 /data 資料卷中已保留先前的安裝進度檢查點，完成前模型流量維持受控關閉。從主機重新簽發一次性 Handoff 連結後將從已儲存步驟繼續。',
  resumeActionTitle: '下一步：在主機簽發新的 Handoff 連結',
  resumeActionLead: '先在主機停止執行中的可寫 CPAMP 服務，執行下方命令輸出新的 ',
  resumeActionTail: ' 連結，重新啟動服務後在瀏覽器開啟該完整 URL 即可恢復精靈進度：',
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
    'Остановите этот контейнер, смонтируйте исходный том 1.x в режиме только чтения (/legacy:ro), подключите новый пустой том к /data и выполните предварительную проверку и обновление на хосте (если /legacy:ro также монтируется в запущенный контейнер v2 для веб-входа обновления, предварительно выполните chown -h 10002:10002 для usage.sqlite* и data.key на этом томе или его копии):',
  ownershipChownTitle:
    'Вариант 2: открыть вход обновления только для чтения на остановленном томе — исправьте только владельца файлов Manager',
  ownershipChownDesc:
    'Чтобы проверить исходный ключ администратора 1.x и увидеть состояние обновления на этом томе: остановите контейнер v2, запустите один разовый контейнер с этим томом и смените владельца только файлов базы данных и ключа (никогда не меняйте /data/runtime — он должен принадлежать 0:0), затем запустите контейнер v2 и нажмите повторную проверку:',
  recheckButton: 'Проверить состояние источника снова',
  resumeStepTag: 'Bootstrap Gate · Продолжение установки',
  resumePageTitle:
    'Установка не завершена — выпустите новую ссылку Handoff на хосте для продолжения',
  resumePageDesc:
    'Настройка администратора сохранена, но маршрут консоли и завершение установки ещё не выполнены. Запустите cpa-manager-plus initialize на хосте развёртывания, чтобы получить новую ссылку и продолжить без сброса ключа администратора.',
  resumeGuidanceTitle: 'Обнаружена незавершённая новая установка',
  resumeGuidanceDetail:
    'Контрольная точка установки сохранена в /data, трафик моделей закрыт до завершения настройки. Новая одноразовая ссылка Handoff с хоста продолжит мастер с сохранённого шага.',
  resumeActionTitle: 'Следующий шаг: выпустить новую ссылку Handoff на хосте',
  resumeActionLead:
    'Остановите запущенный сервис CPAMP на хосте, выполните команду ниже для выпуска новой ссылки ',
  resumeActionTail:
    ', снова запустите сервис и откройте полный URL в браузере для продолжения мастера:',
};

export type BootstrapDiscoveryTextKey = keyof typeof en;

export function bootstrapDiscoveryText(key: BootstrapDiscoveryTextKey, language = fullWebLanguage()): string {
  if (language === 'zh-TW') return tw[key];
  if (language.startsWith('zh')) return zh[key];
  if (language === 'ru') return ru[key];
  return en[key];
}
